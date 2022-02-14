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

package ra

import (
	"fmt"
	"strings"
	"sync"
	"time"

	cert "k8s.io/api/certificates/v1"
	clientset "k8s.io/client-go/kubernetes"

	meshconfig "istio.io/api/mesh/v1alpha1"
	"istio.io/istio/security/pkg/k8s/chiron"
	"istio.io/istio/security/pkg/pki/ca"
	raerror "istio.io/istio/security/pkg/pki/error"
	"istio.io/istio/security/pkg/pki/util"
)

// KubernetesRA integrated with an external CA using Kubernetes CSR API
type KubernetesRA struct {
	csrInterface                 clientset.Interface
	keyCertBundle                *util.KeyCertBundle
	raOpts                       *IstioRAOptions
	caCertificatesFromMeshConfig map[string]string
	certSignerDomain             string
	// mutex protects the R/W to caCertificatesFromMeshConfig.
	mutex sync.RWMutex
}

// NewKubernetesRA : Create a RA that interfaces with K8S CSR CA
func NewKubernetesRA(raOpts *IstioRAOptions) (*KubernetesRA, error) {
	keyCertBundle, err := util.NewKeyCertBundleWithRootCertFromFile(raOpts.CaCertFile)
	if err != nil {
		return nil, raerror.NewError(raerror.CAInitFail, fmt.Errorf("error processing Certificate Bundle for Kubernetes RA"))
	}
	istioRA := &KubernetesRA{
		csrInterface:                 raOpts.K8sClient,
		raOpts:                       raOpts,
		keyCertBundle:                keyCertBundle,
		certSignerDomain:             raOpts.CertSignerDomain,
		caCertificatesFromMeshConfig: make(map[string]string),
	}
	return istioRA, nil
}

func (r *KubernetesRA) kubernetesSign(csrPEM []byte, caCertFile string, certSigner string,
	requestedLifetime time.Duration) ([]byte, error) {
	certSignerDomain := r.certSignerDomain
	if certSignerDomain == "" && certSigner != "" {
		return nil, raerror.NewError(raerror.CertGenError, fmt.Errorf("certSignerDomain is requiered for signer %s", certSigner))
	}
	if certSignerDomain != "" && certSigner != "" {
		certSigner = certSignerDomain + "/" + certSigner
	} else {
		certSigner = r.raOpts.CaSigner
	}
	usages := []cert.KeyUsage{
		cert.UsageDigitalSignature,
		cert.UsageKeyEncipherment,
		cert.UsageServerAuth,
		cert.UsageClientAuth,
	}
	certChain, _, err := chiron.SignCSRK8s(r.csrInterface, csrPEM, certSigner,
		nil, usages, "", caCertFile, true, false, requestedLifetime)
	if err != nil {
		return nil, raerror.NewError(raerror.CertGenError, err)
	}
	return certChain, err
}

// Sign takes a PEM-encoded CSR and cert opts, and returns a certificate signed by k8s CA.
func (r *KubernetesRA) Sign(csrPEM []byte, certOpts ca.CertOpts) ([]byte, error) {
	_, err := preSign(r.raOpts, csrPEM, certOpts.SubjectIDs, certOpts.TTL, certOpts.ForCA)
	if err != nil {
		return nil, err
	}
	certSigner := certOpts.CertSigner

	return r.kubernetesSign(csrPEM, r.raOpts.CaCertFile, certSigner, certOpts.TTL)
}

// SignWithCertChain is similar to Sign but returns the leaf cert and the entire cert chain.
func (r *KubernetesRA) SignWithCertChain(csrPEM []byte, certOpts ca.CertOpts) ([]string, error) {
	cert, err := r.Sign(csrPEM, certOpts)
	if err != nil {
		return nil, err
	}
	chainPem := r.GetCAKeyCertBundle().GetCertChainPem()
	if len(chainPem) > 0 {
		cert = append(cert, chainPem...)
	}
	respCertChain := []string{string(cert)}
	var rootCert, rootCertFromMeshConfig, rootCertFromCertChain []byte
	certSigner := r.certSignerDomain + "/" + certOpts.CertSigner
	if len(r.GetCAKeyCertBundle().GetRootCertPem()) == 0 {
		rootCertFromCertChain, err = util.FindRootCertFromCertificateChainBytes(cert)
		if err != nil {
			return nil, fmt.Errorf("failed to find root cert from signed cert-chain (%v)", err.Error())
		}
		rootCertFromMeshConfig, err = r.GetRootCertFromMeshConfig(certSigner)
		if err != nil {
			return nil, fmt.Errorf("failed to find root cert from mesh config (%v)", err.Error())
		}
		if rootCertFromMeshConfig != nil {
			rootCert = rootCertFromMeshConfig
		} else if rootCertFromCertChain != nil {
			rootCert = rootCertFromCertChain
		}
		if verifyErr := util.VerifyCertificate(nil, cert, rootCert, nil); verifyErr != nil {
			return nil, fmt.Errorf("root cert from signed cert-chain is invalid %v ", verifyErr)
		}
		respCertChain = append(respCertChain, string(rootCert))
	}
	return respCertChain, nil
}

// GetCAKeyCertBundle returns the KeyCertBundle for the CA.
func (r *KubernetesRA) GetCAKeyCertBundle() *util.KeyCertBundle {
	return r.keyCertBundle
}

func (r *KubernetesRA) SetCACertificatesFromMeshConfig(caCertificates []*meshconfig.MeshConfig_CertificateData) {
	r.mutex.Lock()
	for _, pemCert := range caCertificates {
		// TODO:  take care of spiffe bundle format as well
		cert := pemCert.GetPem()
		certSigners := pemCert.CertSigners
		if len(certSigners) != 0 {
			certSigner := strings.Join(certSigners, ",")
			if cert != "" {
				r.caCertificatesFromMeshConfig[certSigner] = cert
			}
		}
	}
	r.mutex.Unlock()
}

func (r *KubernetesRA) GetRootCertFromMeshConfig(signerName string) ([]byte, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	caCertificates := r.caCertificatesFromMeshConfig
	if len(caCertificates) == 0 {
		return nil, fmt.Errorf("no caCertificates defined in mesh config")
	}
	for signers, caCertificate := range caCertificates {
		signerList := strings.Split(signers, ",")
		if len(signerList) == 0 {
			continue
		}
		for _, signer := range signerList {
			if signer == signerName {
				return []byte(caCertificate), nil
			}
		}
	}
	return nil, fmt.Errorf("failed to find root cert for signer: %v in mesh config", signerName)
}
