// Copyright 2026 Google LLC
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

package pkgcache

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// CA manages an internal Grain Root Certificate Authority and dynamically
// generates leaf certificates for transparent TLS interception of package
// and dependency domains.
type CA struct {
	rootCert *x509.Certificate
	rootKey  *ecdsa.PrivateKey
	rootPEM  []byte

	mu    sync.RWMutex
	certs map[string]*tls.Certificate
}

// NewCA creates a new in-memory Root Certificate Authority.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: generating CA private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: generating CA serial number: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Grain Internal Root CA",
			Organization: []string{"Grain"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: creating CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: parsing generated CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	return &CA{
		rootCert: cert,
		rootKey:  key,
		rootPEM:  certPEM,
		certs:    make(map[string]*tls.Certificate),
	}, nil
}

// RootCertPEM returns the PEM-encoded root certificate.
func (c *CA) RootCertPEM() []byte {
	return c.rootPEM
}

// GetCertificate returns a certificate for the given ClientHelloInfo, dynamically
// issuing a new certificate signed by the Root CA if one does not already exist.
func (c *CA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		host = "172.17.0.1"
	}
	// Strip port if present
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	c.mu.RLock()
	if cert, ok := c.certs[host]; ok {
		c.mu.RUnlock()
		return cert, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double check after acquiring write lock
	if cert, ok := c.certs[host]; ok {
		return cert, nil
	}

	cert, err := c.issueCertificate(host)
	if err != nil {
		return nil, err
	}

	c.certs[host] = cert
	return cert, nil
}

// issueCertificate issues a new certificate for host signed by Root CA.
func (c *CA) issueCertificate(host string) (*tls.Certificate, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: generating leaf key for %s: %w", host, err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: generating serial number for %s: %w", host, err)
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"Grain"},
		},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
		if !strings.HasPrefix(host, "*.") {
			// Add wildcard if top-level subdomain
			parts := strings.Split(host, ".")
			if len(parts) > 2 {
				tmpl.DNSNames = append(tmpl.DNSNames, "*."+strings.Join(parts[1:], "."))
			}
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, c.rootCert, &leafKey.PublicKey, c.rootKey)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: signing certificate for %s: %w", host, err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{derBytes, c.rootCert.Raw},
		PrivateKey:  leafKey,
	}

	return tlsCert, nil
}

// TLSConfig returns a tls.Config configured for dynamic certificate issuance.
func (c *CA) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: c.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}
