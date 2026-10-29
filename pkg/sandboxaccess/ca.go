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

package sandboxaccess

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// CALifetime is how long a freshly generated deployment CA is valid.
// Rotating it means every live sandbox's client certificate stops
// chaining, so it is long; the key lives in Secret Manager, not on disk.
const CALifetime = 10 * 365 * 24 * time.Hour

// ServerCertLifetime is how long the acceptor's own serving certificate
// is minted for. It is re-minted from the CA at every start.
const ServerCertLifetime = 30 * 24 * time.Hour

// CA is the per-deployment certificate authority every sandbox's client
// certificate, and the acceptor's own serving certificate, chain to.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

// NewCA generates a fresh ECDSA P-256 CA for deployment.
func NewCA(deployment string, now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sandboxaccess: generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "grain sandbox access CA " + deployment},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CALifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sandboxaccess: self-signing CA: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return ParseCA(append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...))
}

// ParseCA reads a CA from the PEM bundle PEM returns: one CERTIFICATE
// block and one EC PRIVATE KEY block, in either order.
func ParseCA(bundle []byte) (*CA, error) {
	ca := &CA{}
	for rest := bundle; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("sandboxaccess: parsing CA certificate: %w", err)
			}
			ca.cert = cert
			ca.certPEM = pem.EncodeToMemory(block)
		case "EC PRIVATE KEY":
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("sandboxaccess: parsing CA key: %w", err)
			}
			ca.key = key
			ca.keyPEM = pem.EncodeToMemory(block)
		}
	}
	if ca.cert == nil || ca.key == nil {
		return nil, errors.New("sandboxaccess: CA bundle needs a CERTIFICATE and an EC PRIVATE KEY block")
	}
	if !ca.cert.IsCA {
		return nil, errors.New("sandboxaccess: CA bundle's certificate is not a CA")
	}
	pub, ok := ca.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&ca.key.PublicKey) {
		return nil, errors.New("sandboxaccess: CA bundle's key does not match its certificate")
	}
	return ca, nil
}

// PEM is the bundle a CAStore keeps: certificate, then private key.
func (c *CA) PEM() []byte { return append(append([]byte{}, c.certPEM...), c.keyPEM...) }

// CertPEM is the CA certificate alone -- what a wrapper is handed to
// verify the acceptor with. Never the key.
func (c *CA) CertPEM() []byte { return append([]byte{}, c.certPEM...) }

// Pool is a pool holding only this CA.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

// ServerCertificate mints the acceptor's serving certificate for hosts
// (DNS names or IP literals), valid from now for ServerCertLifetime.
func (c *CA) ServerCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "grain-access"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(ServerCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	addHosts(tmpl, hosts)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sandboxaccess: minting server certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// clientCertificate mints a client certificate for one sandbox:
// CN = sandbox, a URI SAN of SandboxURI(deployment, sandbox), valid over
// [notBefore, notAfter].
func (c *CA) clientCertificate(deployment, sandbox string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sandbox},
		URIs:         []*url.URL{SandboxURI(deployment, sandbox)},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxaccess: minting client certificate for %q: %w", sandbox, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("sandboxaccess: certificate serial: %w", err)
	}
	return serial, nil
}

// ErrNoCA is what a CAStore's Load returns when no CA has been stored.
var ErrNoCA = errors.New("sandboxaccess: no CA stored")

// CAStore keeps the deployment CA's PEM bundle somewhere safer than the
// daemon's disk: Secret Manager in a GKE deployment (SecretManagerStore).
type CAStore interface {
	// Load returns the stored bundle, or ErrNoCA if there is none.
	Load(ctx context.Context) ([]byte, error)
	// Save stores bundle as the current CA.
	Save(ctx context.Context, bundle []byte) error
}

// LoadOrCreateCA returns the CA store holds, generating and saving one
// for deployment the first time.
func LoadOrCreateCA(ctx context.Context, store CAStore, deployment string, now time.Time) (*CA, error) {
	bundle, err := store.Load(ctx)
	if err == nil {
		return ParseCA(bundle)
	}
	if !errors.Is(err, ErrNoCA) {
		return nil, err
	}
	ca, err := NewCA(deployment, now)
	if err != nil {
		return nil, err
	}
	if err := store.Save(ctx, ca.PEM()); err != nil {
		return nil, fmt.Errorf("sandboxaccess: saving new CA: %w", err)
	}
	return ca, nil
}
