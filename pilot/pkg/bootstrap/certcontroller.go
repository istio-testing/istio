// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bootstrap

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"istio.io/istio/pilot/pkg/features"
	tb "istio.io/istio/pilot/pkg/trustbundle"
	"istio.io/istio/pkg/config/constants"
	"istio.io/istio/pkg/log"
	"istio.io/istio/pkg/security"
	"istio.io/istio/pkg/sleep"
	"istio.io/istio/security/pkg/k8s/chiron"
	"istio.io/istio/security/pkg/pki/ca"
	certutil "istio.io/istio/security/pkg/util"
)

const (
	// defaultCertGracePeriodRatio is the default length of certificate rotation grace period,
	// configured as the ratio of the certificate TTL.
	defaultCertGracePeriodRatio = 0.5

	// the interval polling root cert and re sign istiod cert when it changes.
	rootCertPollingInterval = 60 * time.Second

	// Default CA certificate path
	// Currently, custom CA path is not supported; no API to get custom CA cert yet.
	defaultCACertPath = "./var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// initDNSCerts will create the certificates to be used by Istiod GRPC server and webhooks.
// If the certificate creation fails - for example no support in K8S - returns an error.
// Will use the mesh.yaml DiscoveryAddress to find the default expected address of the control plane,
// with an environment variable allowing override.
//
// Controlled by features.IstiodService env variable, which defines the name of the service to use in the DNS
// cert, or empty for disabling this feature.
//
// TODO: If the discovery address in mesh.yaml is set to port 15012 (XDS-with-DNS-certs) and the name
// matches the k8s namespace, failure to start DNS server is a fatal error.
func (s *Server) initDNSCerts() error {
	var certChain, keyPEM, caBundle []byte
	var err error
	pilotCertProviderName := features.PilotCertProvider
	if strings.HasPrefix(pilotCertProviderName, constants.CertProviderKubernetesSignerPrefix) && s.RA != nil {
		signerName := strings.TrimPrefix(pilotCertProviderName, constants.CertProviderKubernetesSignerPrefix)
		log.Infof("Generating K8S-signed cert for %v using signer %v", s.dnsNames, signerName)
		certChain, keyPEM, _, err = chiron.GenKeyCertK8sCA(s.kubeClient.Kube(),
			strings.Join(s.dnsNames, ","), "", signerName, true, SelfSignedCACertTTL.Get())
		if err != nil {
			return fmt.Errorf("failed generating key and cert by kubernetes: %v", err)
		}
		caBundle, err = s.RA.GetRootCertFromMeshConfig(signerName)
		if err != nil {
			return err
		}
		// MeshConfig:Add callback for mesh config update
		s.environment.AddMeshHandler(func() {
			newCaBundle, _ := s.RA.GetRootCertFromMeshConfig(signerName)
			if newCaBundle != nil && !bytes.Equal(newCaBundle, s.istiodCertBundleWatcher.GetKeyCertBundle().CABundle) {
				newCertChain, newKeyPEM, _, err := chiron.GenKeyCertK8sCA(s.kubeClient.Kube(),
					strings.Join(s.dnsNames, ","), "", signerName, true, SelfSignedCACertTTL.Get())
				if err != nil {
					log.Fatalf("failed regenerating key and cert for istiod by kubernetes: %v", err)
				}
				s.istiodCertBundleWatcher.SetAndNotify(newKeyPEM, newCertChain, newCaBundle)
			}
		})

		s.addStartFunc("certificate rotation", func(stop <-chan struct{}) error {
			go func() {
				// Track TTL of DNS cert and renew cert in accordance to grace period.
				s.RotateDNSCertForK8sCA(stop, "", signerName, true, SelfSignedCACertTTL.Get())
			}()
			return nil
		})
	} else if pilotCertProviderName == constants.CertProviderKubernetes {
		log.Infof("Generating K8S-signed cert for %v", s.dnsNames)
		certChain, keyPEM, _, err = chiron.GenKeyCertK8sCA(s.kubeClient.Kube(),
			strings.Join(s.dnsNames, ","), defaultCACertPath, "", true, SelfSignedCACertTTL.Get())
		if err != nil {
			return fmt.Errorf("failed generating key and cert by kubernetes: %v", err)
		}
		caBundle, err = os.ReadFile(defaultCACertPath)
		if err != nil {
			return fmt.Errorf("failed reading %s: %v", defaultCACertPath, err)
		}

		s.addStartFunc("certificate rotation", func(stop <-chan struct{}) error {
			go func() {
				// Track TTL of DNS cert and renew cert in accordance to grace period.
				s.RotateDNSCertForK8sCA(stop, defaultCACertPath, "", true, SelfSignedCACertTTL.Get())
			}()
			return nil
		})
	} else if pilotCertProviderName == constants.CertProviderIstiod {
		certChain, keyPEM, err = s.CA.GenKeyCert(s.dnsNames, SelfSignedCACertTTL.Get(), false)
		if err != nil {
			return fmt.Errorf("failed generating istiod key cert %v", err)
		}
		log.Infof("Generating istiod-signed cert for %v:\n %s", s.dnsNames, certChain)

		fileBundle, err := detectSigningCABundle()
		if err != nil {
			return fmt.Errorf("unable to determine signing file format %v", err)
		}

		istioGenerated, detectedSigningCABundle := false, false
		if _, err := os.Stat(fileBundle.SigningKeyFile); err == nil {
			detectedSigningCABundle = true
			if _, err := os.Stat(path.Join(LocalCertDir.Get(), ca.IstioGenerated)); err == nil {
				istioGenerated = true
			}
		}
		// check if signing key file exists the cert dir and if the istio-generated file
		// exists (only if USE_CACERTS_FOR_SELF_SIGNED_CA is enabled)
		if !detectedSigningCABundle || (features.UseCacertsForSelfSignedCA && istioGenerated) {
			log.Infof("Use istio-generated cacerts at %v or istio-ca-secret", fileBundle.SigningKeyFile)

			caBundle = s.CA.GetCAKeyCertBundle().GetRootCertPem()
			s.addStartFunc("certificate rotation", func(stop <-chan struct{}) error {
				go func() {
					// regenerate istiod key cert when root cert changes.
					s.watchRootCertAndGenKeyCert(stop)
				}()
				return nil
			})
		} else {
			log.Infof("Use plugged-in cert at %v", fileBundle.SigningKeyFile)

			caBundle, err = os.ReadFile(fileBundle.RootCertFile)
			if err != nil {
				return fmt.Errorf("failed reading %s: %v", fileBundle.RootCertFile, err)
			}
		}
	} else {
		customCACertPath := security.DefaultRootCertFilePath
		log.Infof("User specified cert provider: %v, mounted in a well known location %v",
			features.PilotCertProvider, customCACertPath)
		caBundle, err = os.ReadFile(customCACertPath)
		if err != nil {
			return fmt.Errorf("failed reading %s: %v", customCACertPath, err)
		}
	}
	s.istiodCertBundleWatcher.SetAndNotify(keyPEM, certChain, caBundle)
	return nil
}

// TODO(hzxuzonghu): support async notification instead of polling the CA root cert.
func (s *Server) watchRootCertAndGenKeyCert(stop <-chan struct{}) {
	caBundle := s.CA.GetCAKeyCertBundle().GetRootCertPem()
	for {
		if !sleep.Until(stop, rootCertPollingInterval) {
			return
		}
		newRootCert := s.CA.GetCAKeyCertBundle().GetRootCertPem()
		if !bytes.Equal(caBundle, newRootCert) {
			caBundle = newRootCert
			certChain, keyPEM, err := s.CA.GenKeyCert(s.dnsNames, SelfSignedCACertTTL.Get(), false)
			if err != nil {
				log.Errorf("failed generating istiod key cert %v", err)
			} else {
				s.istiodCertBundleWatcher.SetAndNotify(keyPEM, certChain, caBundle)
				log.Infof("regenerated istiod dns cert: %s", certChain)
			}
		}
	}
}

func (s *Server) RotateDNSCertForK8sCA(stop <-chan struct{},
	defaultCACertPath string,
	signerName string,
	approveCsr bool,
	requestedLifetime time.Duration,
) {
	certUtil := certutil.NewCertUtil(int(defaultCertGracePeriodRatio * 100))
	for {
		waitTime, _ := certUtil.GetWaitTime(s.istiodCertBundleWatcher.GetKeyCertBundle().CertPem, time.Now(), time.Duration(0))
		if !sleep.Until(stop, waitTime) {
			return
		}
		certChain, keyPEM, _, err := chiron.GenKeyCertK8sCA(s.kubeClient.Kube(),
			strings.Join(s.dnsNames, ","), defaultCACertPath, signerName, approveCsr, requestedLifetime)
		if err != nil {
			log.Errorf("failed regenerating key and cert for istiod by kubernetes: %v", err)
			continue
		}
		s.istiodCertBundleWatcher.SetAndNotify(keyPEM, certChain, s.istiodCertBundleWatcher.GetCABundle())
	}
}

// updatePluggedinRootCertAndGenKeyCert when intermediate CA is updated, it generates new dns certs and notifies keycertbundle about the changes
func (s *Server) updatePluggedinRootCertAndGenKeyCert() error {
	caBundle := s.CA.GetCAKeyCertBundle().GetRootCertPem()
	certChain, keyPEM, err := s.CA.GenKeyCert(s.dnsNames, SelfSignedCACertTTL.Get(), false)
	if err != nil {
		return err
	}

	if features.MultiRootMesh {
		// Trigger trust anchor update, this will send PCDS to all sidecars.
		log.Debugf("Update trust anchor with new root cert")
		err = s.workloadTrustBundle.UpdateTrustAnchor(&tb.TrustAnchorUpdate{
			TrustAnchorConfig: tb.TrustAnchorConfig{Certs: []string{string(caBundle)}},
			Source:            tb.SourceIstioCA,
		})
		if err != nil {
			log.Errorf("failed to update trust anchor from source Istio CA, err: %v", err)
			return err
		}
	}

	s.istiodCertBundleWatcher.SetAndNotify(keyPEM, certChain, caBundle)
	return nil
}

// initCertificateWatches sets up watches for the plugin dns certs.
func (s *Server) initCertificateWatches(tlsOptions TLSOptions) error {
	if err := s.istiodCertBundleWatcher.SetFromFilesAndNotify(tlsOptions.KeyFile, tlsOptions.CertFile, tlsOptions.CaCertFile); err != nil {
		return fmt.Errorf("set keyCertBundle failed: %v", err)
	}
	// TODO: Setup watcher for root and restart server if it changes.
	for _, file := range []string{tlsOptions.CertFile, tlsOptions.KeyFile} {
		log.Infof("adding watcher for certificate %s", file)
		if err := s.fileWatcher.Add(file); err != nil {
			return fmt.Errorf("could not watch %v: %v", file, err)
		}
	}
	s.addStartFunc("certificate rotation", func(stop <-chan struct{}) error {
		go func() {
			var keyCertTimerC <-chan time.Time
			for {
				select {
				case <-keyCertTimerC:
					keyCertTimerC = nil
					if err := s.istiodCertBundleWatcher.SetFromFilesAndNotify(tlsOptions.KeyFile, tlsOptions.CertFile, tlsOptions.CaCertFile); err != nil {
						log.Errorf("Setting keyCertBundle failed: %v", err)
					}
				case <-s.fileWatcher.Events(tlsOptions.CertFile):
					if keyCertTimerC == nil {
						keyCertTimerC = time.After(watchDebounceDelay)
					}
				case <-s.fileWatcher.Events(tlsOptions.KeyFile):
					if keyCertTimerC == nil {
						keyCertTimerC = time.After(watchDebounceDelay)
					}
				case err := <-s.fileWatcher.Errors(tlsOptions.CertFile):
					log.Errorf("error watching %v: %v", tlsOptions.CertFile, err)
				case err := <-s.fileWatcher.Errors(tlsOptions.KeyFile):
					log.Errorf("error watching %v: %v", tlsOptions.KeyFile, err)
				case <-stop:
					return
				}
			}
		}()
		return nil
	})
	return nil
}

func (s *Server) reloadIstiodCert(watchCh <-chan struct{}, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		case <-watchCh:
			if err := s.loadIstiodCert(); err != nil {
				log.Errorf("reload istiod cert failed: %v", err)
			}
		}
	}
}

// loadIstiodCert load IstiodCert received from watchCh once
func (s *Server) loadIstiodCert() error {
	keyCertBundle := s.istiodCertBundleWatcher.GetKeyCertBundle()
	keyPair, err := tls.X509KeyPair(keyCertBundle.CertPem, keyCertBundle.KeyPem)
	if err != nil {
		return fmt.Errorf("istiod loading x509 key pairs failed: %v", err)
	}
	for _, c := range keyPair.Certificate {
		x509Cert, err := x509.ParseCertificates(c)
		if err != nil {
			// This can rarely happen, just in case.
			return fmt.Errorf("x509 cert - ParseCertificates() error: %v", err)
		}
		for _, c := range x509Cert {
			log.Infof("x509 cert - Issuer: %q, Subject: %q, SN: %x, NotBefore: %q, NotAfter: %q",
				c.Issuer, c.Subject, c.SerialNumber,
				c.NotBefore.Format(time.RFC3339), c.NotAfter.Format(time.RFC3339))
		}
	}

	log.Info("Istiod certificates are reloaded")
	s.certMu.Lock()
	s.istiodCert = &keyPair
	s.certMu.Unlock()
	return nil
}
