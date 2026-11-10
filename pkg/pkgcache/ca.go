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
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// caLifetime is how long a newly generated Root CA certificate is valid.
	caLifetime = 10 * 365 * 24 * time.Hour
	// caRenewWindow is how close to NotAfter a persisted Root CA may get
	// before LoadOrCreateCA mints and persists a replacement on startup.
	caRenewWindow = 30 * 24 * time.Hour
)

// CA manages an internal Grain Root Certificate Authority and dynamically
// generates leaf certificates for transparent TLS interception of package
// and dependency domains.
type CA struct {
	rootCert *x509.Certificate
	rootKey  *ecdsa.PrivateKey
	rootPEM  []byte
	keyPEM   []byte

	mu    sync.RWMutex
	certs map[string]*tls.Certificate
}

// NewCA creates a new in-memory Root Certificate Authority.
func NewCA() (*CA, error) {
	return newCA(time.Now())
}

func newCA(now time.Time) (*CA, error) {
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
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(caLifetime),
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

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pkgcache: marshaling CA private key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	return &CA{
		rootCert: cert,
		rootKey:  key,
		rootPEM:  certPEM,
		keyPEM:   keyPEM,
		certs:    make(map[string]*tls.Certificate),
	}, nil
}

// ParseCA reads a Root CA from a PEM bundle holding one CERTIFICATE block and
// one EC PRIVATE KEY block.
func ParseCA(bundle []byte) (*CA, error) {
	var (
		cert    *x509.Certificate
		key     *ecdsa.PrivateKey
		certPEM []byte
		keyPEM  []byte
	)
	for rest := bundle; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			parsed, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("pkgcache: parsing CA certificate: %w", err)
			}
			cert = parsed
			certPEM = pem.EncodeToMemory(block)
		case "EC PRIVATE KEY":
			parsed, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("pkgcache: parsing CA private key: %w", err)
			}
			key = parsed
			keyPEM = pem.EncodeToMemory(block)
		}
	}
	if cert == nil || key == nil {
		return nil, errors.New("pkgcache: CA bundle must contain CERTIFICATE and EC PRIVATE KEY blocks")
	}
	if !cert.IsCA {
		return nil, errors.New("pkgcache: CA bundle certificate is not a CA")
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("pkgcache: CA bundle private key does not match certificate")
	}
	return &CA{
		rootCert: cert,
		rootKey:  key,
		rootPEM:  certPEM,
		keyPEM:   keyPEM,
		certs:    make(map[string]*tls.Certificate),
	}, nil
}

// LoadOrCreateCA loads the persisted Root CA PEM bundle from path if it exists
// and is not within caRenewWindow of expiry, or generates and atomically saves
// a fresh Root CA to path (mode 0600). An empty path returns an in-memory CA.
func LoadOrCreateCA(path string) (*CA, error) {
	return loadOrCreateCAAt(path, time.Now())
}

func loadOrCreateCAAt(path string, now time.Time) (*CA, error) {
	if strings.TrimSpace(path) == "" {
		return newCA(now)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		ca, parseErr := ParseCA(data)
		if parseErr != nil {
			return nil, fmt.Errorf("pkgcache: loading CA from %s: %w", path, parseErr)
		}
		if ca.rootCert.NotAfter.After(now.Add(caRenewWindow)) {
			return ca, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("pkgcache: reading CA from %s: %w", path, err)
	}
	ca, err := newCA(now)
	if err != nil {
		return nil, err
	}
	if err := SaveCA(path, ca); err != nil {
		return nil, err
	}
	return ca, nil
}

// SaveCA atomically writes ca's PEM bundle (certificate + private key) to path
// with owner-only permissions (0600).
func SaveCA(path string, ca *CA) error {
	if ca == nil {
		return errors.New("pkgcache: cannot save nil CA")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("pkgcache: creating CA directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".package-cache-ca-*")
	if err != nil {
		return fmt.Errorf("pkgcache: creating temporary CA file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("pkgcache: setting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(ca.PEM()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("pkgcache: writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pkgcache: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("pkgcache: installing %s: %w", path, err)
	}
	return nil
}

// NeedsRenewal reports whether c is within caRenewWindow of expiry at now,
// the point LoadOrCreateCA replaces a persisted CA rather than loading it.
func (c *CA) NeedsRenewal(now time.Time) bool {
	return !c.rootCert.NotAfter.After(now.Add(caRenewWindow))
}

// PEM returns the PEM-encoded CA certificate followed by its EC private key.
func (c *CA) PEM() []byte {
	out := make([]byte, 0, len(c.rootPEM)+len(c.keyPEM))
	out = append(out, c.rootPEM...)
	out = append(out, c.keyPEM...)
	return out
}

// RootCertPEM returns the PEM-encoded root certificate.
func (c *CA) RootCertPEM() []byte {
	return append([]byte(nil), c.rootPEM...)
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
