package pkgcache

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
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
