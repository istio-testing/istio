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

package signer

import (
	"bytes"
	"crypto"
	"fmt"
	"io/ioutil"
	"sync/atomic"
	"time"

	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"

	"istio.io/istio/pkg/test/cert/ca"
	"istio.io/istio/pkg/test/csrctrl/authority"
)

func newCAProvider(signerRoot, signerName string) (*caProvider, error) {
	strRoot := signerRoot + "/" + signerName + "/"
	caLoader, err := ca.NewRoot(strRoot)
	if err != nil {
		return nil, fmt.Errorf("error reading CA cert file %s: %v", strRoot, err)
	}

	ret := &caProvider{
		caLoader: caLoader,
	}
	if err := ret.setCA(); err != nil {
		return nil, err
	}

	return ret, nil
}

type caProvider struct {
	caValue  atomic.Value
	caLoader ca.Root
}

// currentCertContent retrieve current certificate content from cert file
func (p *caProvider) currentCertContent() ([]byte, error) {
	certBytes, err := ioutil.ReadFile(p.caLoader.CertFile)
	if err != nil {
		return []byte(""), fmt.Errorf("error reading CA from cert file %s: %v", p.caLoader.CertFile, err)
	}
	return certBytes, nil
}

// currentKeyContent retrieve current private key content from key file
func (p *caProvider) currentKeyContent() ([]byte, error) {
	keyBytes, err := ioutil.ReadFile(p.caLoader.KeyFile)
	if err != nil {
		return []byte(""), fmt.Errorf("error reading private key from key file %s: %v", p.caLoader.KeyFile, err)
	}
	return keyBytes, nil
}

// setCA unconditionally stores the current cert/key content
func (p *caProvider) setCA() error {
	certPEM, cerr := p.currentCertContent()
	if cerr != nil {
		return cerr
	}

	keyPEM, kerr := p.currentKeyContent()
	if kerr != nil {
		return kerr
	}

	certs, err := cert.ParseCertsPEM(certPEM)
	if err != nil {
		return fmt.Errorf("error reading CA cert file %q: %v", p.caLoader.CertFile, err)
	}
	if len(certs) != 1 {
		return fmt.Errorf("error reading CA cert file %q: expected 1 certificate, found %d", p.caLoader.CertFile, len(certs))
	}

	key, err := keyutil.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return fmt.Errorf("error reading CA key file %q: %v", p.caLoader.KeyFile, err)
	}
	priv, ok := key.(crypto.Signer)
	if !ok {
		return fmt.Errorf("error reading CA key file %q: key did not implement crypto.Signer", p.caLoader.KeyFile)
	}

	ca := &authority.CertificateAuthority{
		RawCert: certPEM,
		RawKey:  keyPEM,

		Certificate: certs[0],
		PrivateKey:  priv,
		Backdate:    5 * time.Minute,
	}
	p.caValue.Store(ca)

	return nil
}

// currentCA provides the current value of the CA.
// It always check for a stale value.  This is cheap because it's all an in memory cache of small slices.
func (p *caProvider) currentCA() (*authority.CertificateAuthority, error) {
	certPEM, cerr := p.currentCertContent()
	if cerr != nil {
		return nil, cerr
	}

	keyPEM, kerr := p.currentKeyContent()
	if kerr != nil {
		return nil, kerr
	}
	currCA := p.caValue.Load().(*authority.CertificateAuthority)
	if bytes.Equal(currCA.RawCert, certPEM) && bytes.Equal(currCA.RawKey, keyPEM) {
		return currCA, nil
	}

	// the bytes weren't equal, so we have to set and then load
	if err := p.setCA(); err != nil {
		return currCA, err
	}
	return p.caValue.Load().(*authority.CertificateAuthority), nil
}
