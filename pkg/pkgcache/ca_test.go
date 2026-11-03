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
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCAGenerationAndDynamicCert(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	if len(ca.RootCertPEM()) == 0 {
		t.Fatal("RootCertPEM returned empty")
	}

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(ca.RootCertPEM()) {
		t.Fatal("failed to append root cert to pool")
	}

	cert, err := ca.GetCertificate(&tls.ClientHelloInfo{
		ServerName: "deb.debian.org",
	})
	if err != nil {
		t.Fatalf("GetCertificate(deb.debian.org): %v", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if leaf.Subject.CommonName != "deb.debian.org" {
		t.Errorf("leaf CommonName = %q, want deb.debian.org", leaf.Subject.CommonName)
	}

	opts := x509.VerifyOptions{
		Roots:   rootPool,
		DNSName: "deb.debian.org",
	}
	if _, err := leaf.Verify(opts); err != nil {
		t.Fatalf("leaf cert verification failed: %v", err)
	}
}

func TestCAClientServerHandshake(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from intercepted domain"))
	}))
	ts.TLS = ca.TLSConfig()
	ts.StartTLS()
	defer ts.Close()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootPool,
				ServerName: "registry.npmjs.org",
			},
		},
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = "registry.npmjs.org"

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestLoadOrCreateCAPersistsAndReusesAcrossRestarts(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "package-cache-ca.pem")

	ca1, err := LoadOrCreateCA(caPath)
	if err != nil {
		t.Fatalf("first LoadOrCreateCA: %v", err)
	}
	info, err := os.Stat(caPath)
	if err != nil {
		t.Fatalf("stat %s: %v", caPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s permissions = %04o, want 0600", caPath, perm)
	}

	// Build a sandbox trust pool from the first daemon's Root CA only.
	sandboxPool := x509.NewCertPool()
	if !sandboxPool.AppendCertsFromPEM(ca1.RootCertPEM()) {
		t.Fatal("failed to append ca1 RootCertPEM")
	}

	// Simulate a daemon restart loading the persisted CA from disk.
	ca2, err := LoadOrCreateCA(caPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateCA: %v", err)
	}
	if !bytes.Equal(ca1.RootCertPEM(), ca2.RootCertPEM()) {
		t.Fatal("reloaded CA RootCertPEM differs from original persisted CA")
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok:" + r.Host))
	}))
	ts.TLS = ca2.TLSConfig()
	ts.StartTLS()
	defer ts.Close()

	for _, host := range []string{"proxy.golang.org", "ghcr.io"} {
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:    sandboxPool,
					ServerName: host,
				},
			},
		}
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/check", nil)
		if err != nil {
			t.Fatalf("NewRequest(%s): %v", host, err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HTTPS fetch for %s after CA reload failed: %v", host, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HTTPS fetch for %s status = %d, want 200", host, resp.StatusCode)
		}
	}
}

func TestLoadOrCreateCARenewsNearExpiryCA(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "package-cache-ca.pem")
	t0 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

	ca1, err := loadOrCreateCAAt(caPath, t0)
	if err != nil {
		t.Fatalf("initial loadOrCreateCAAt: %v", err)
	}

	// Just inside the 30-day renewal window before 10-year expiry, a restart
	// mints and persists a replacement CA.
	nearExpiry := ca1.rootCert.NotAfter.Add(-caRenewWindow + time.Hour)
	ca2, err := loadOrCreateCAAt(caPath, nearExpiry)
	if err != nil {
		t.Fatalf("renewal loadOrCreateCAAt: %v", err)
	}
	if bytes.Equal(ca1.RootCertPEM(), ca2.RootCertPEM()) {
		t.Fatal("expected near-expiry CA to be renewed, but RootCertPEM was unchanged")
	}

	ca3, err := loadOrCreateCAAt(caPath, nearExpiry.Add(time.Minute))
	if err != nil {
		t.Fatalf("post-renewal loadOrCreateCAAt: %v", err)
	}
	if !bytes.Equal(ca2.RootCertPEM(), ca3.RootCertPEM()) {
		t.Fatal("expected renewed CA to be persisted and reused")
	}
}

func TestLoadOrCreateCARejectsCorruptBundle(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "package-cache-ca.pem")
	if err := os.WriteFile(caPath, []byte("not a valid PEM bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCA(caPath); err == nil {
		t.Fatal("LoadOrCreateCA accepted a corrupt PEM bundle")
	}
}
