package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRelayKeyPair writes a self-signed certificate and its key to dir and
// returns their paths, so runRelay gets past loading -tls-cert/-tls-key.
func writeRelayKeyPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "granule-relay-test"},
		DNSNames:     []string{"granule-relay-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func runRelayCLI(args ...string) (int, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"relay"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestRelay_FlagValidation(t *testing.T) {
	// Keep every case off the in-cluster path, whatever the test host is.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	dir := t.TempDir()
	cert, key := writeRelayKeyPair(t, dir)
	notPEM := filepath.Join(dir, "not-a-ca.pem")
	if err := os.WriteFile(notPEM, []byte("this is not PEM\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no flags", nil, "-tls-cert and -tls-key are required"},
		{"cert without key", []string{"-tls-cert", cert}, "-tls-cert and -tls-key are required"},
		{"key without cert", []string{"-tls-key", key}, "-tls-cert and -tls-key are required"},
		{"unexpected argument", []string{"-tls-cert", cert, "-tls-key", key, "extra-arg"}, `unexpected arguments ["extra-arg"]`},
		{"unreadable key pair", []string{"-tls-cert", filepath.Join(dir, "missing.crt"), "-tls-key", key}, "loading -tls-cert/-tls-key"},
		{"missing client CA", []string{"-tls-cert", cert, "-tls-key", key, "-client-ca", filepath.Join(dir, "missing-ca.pem")}, "reading -client-ca"},
		{"client CA without certificates", []string{"-tls-cert", cert, "-tls-key", key, "-client-ca", notPEM}, "holds no certificates"},
		{"missing token file", []string{"-tls-cert", cert, "-tls-key", key, "-token-file", filepath.Join(dir, "missing-token")}, "reading -token-file"},
		{"outside a cluster", []string{"-tls-cert", cert, "-tls-key", key}, "not running in a cluster"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runRelayCLI(tc.args...)
			if code == 0 {
				t.Fatalf("granule relay %v exited 0, want failure; output:\n%s", tc.args, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("granule relay %v output = %q, want it to contain %q", tc.args, out, tc.want)
			}
		})
	}
}

// The controller token must not outlive flag handling in the relay's
// environment, where anything it execs or anything reading
// /proc/self/environ would see it -- even when startup then fails.
func TestRelay_ScrubsTokenFromEnvironment(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	dir := t.TempDir()
	cert, key := writeRelayKeyPair(t, dir)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"token from the environment", []string{"-tls-cert", cert, "-tls-key", key}},
		{"token file overrides the environment", []string{"-tls-cert", cert, "-tls-key", key, "-token-file", filepath.Join(dir, "missing-token")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GRANULE_RELAY_TOKEN", "relay-test-secret")
			code, out := runRelayCLI(tc.args...)
			if code == 0 {
				t.Fatalf("granule relay exited 0 outside a cluster; output:\n%s", out)
			}
			if v, ok := os.LookupEnv("GRANULE_RELAY_TOKEN"); ok {
				t.Errorf("$GRANULE_RELAY_TOKEN still set after granule relay (%q)", v)
			}
			if strings.Contains(out, "relay-test-secret") {
				t.Errorf("granule relay printed the token:\n%s", out)
			}
		})
	}
}
