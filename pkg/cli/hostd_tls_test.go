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

package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHostdClientTLSConfig(t *testing.T) {
	t.Parallel()

	// Without a CA file: the system roots.
	cfg, err := hostdClientTLS("", "server.domain.com")
	if err != nil {
		t.Fatalf("hostdClientTLS without CA: %v", err)
	}
	if cfg.ServerName != "server.domain.com" || cfg.RootCAs != nil {
		t.Errorf("unexpected TLS config: %+v", cfg)
	}

	// With a valid CA PEM.
	dir := t.TempDir()
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Custom CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caBytes, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPriv.PublicKey, caPriv)
	caFile := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes}), 0644)

	cfg2, err := hostdClientTLS(caFile, "custom.local")
	if err != nil {
		t.Fatalf("hostdClientTLS with CA: %v", err)
	}
	if cfg2.ServerName != "custom.local" || cfg2.RootCAs == nil {
		t.Errorf("unexpected TLS config with CA: %+v", cfg2)
	}

	// With a file that holds no PEM.
	badCAFile := filepath.Join(dir, "bad-ca.pem")
	_ = os.WriteFile(badCAFile, []byte("not a pem certificate"), 0644)
	if _, err := hostdClientTLS(badCAFile, "custom.local"); err == nil {
		t.Error("expected error on non-PEM CA file, got nil")
	}
}
