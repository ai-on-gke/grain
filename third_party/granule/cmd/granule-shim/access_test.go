package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearAccessEnv(t *testing.T) {
	for _, k := range []string{envAccessListen, envAccessDial, envAccessSandboxID, envAccessToken, envAccessTokenFile,
		envAccessTLSCertFile, envAccessTLSKeyFile, envAccessCAFile, envAccessCertFile, envAccessKeyFile,
		envAccessControllerIPs, envNetshimControllers, envNetshimAccessPorts} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestAccessFromEnv_Disabled(t *testing.T) {
	clearAccessEnv(t)
	a, err := accessFromEnv()
	if err != nil || a.enabled() {
		t.Fatalf("accessFromEnv() = %+v, %v; want disabled", a, err)
	}
}

func TestAccessFromEnv_ScrubsTokenAndUnlinksFile(t *testing.T) {
	clearAccessEnv(t)
	t.Setenv(envAccessListen, "tcp://0.0.0.0:7443")
	t.Setenv(envAccessToken, "from-env")
	t.Setenv(envAccessTLSCertFile, "/c.pem")
	t.Setenv(envAccessTLSKeyFile, "/k.pem")
	a, err := accessFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if a.Token != "from-env" {
		t.Fatalf("Token = %q", a.Token)
	}
	if _, ok := os.LookupEnv(envAccessToken); ok {
		t.Fatalf("%s still set after accessFromEnv", envAccessToken)
	}

	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAccessTokenFile, tokFile)
	a, err = accessFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if a.Token != "from-file" {
		t.Fatalf("Token = %q, want from-file", a.Token)
	}
	if _, err := os.Stat(tokFile); !os.IsNotExist(err) {
		t.Fatalf("token file still present after accessFromEnv: %v", err)
	}
	if _, ok := os.LookupEnv(envAccessTokenFile); ok {
		t.Fatalf("%s still set after accessFromEnv", envAccessTokenFile)
	}
}

func TestAccessFromEnv_TCPListenRequiresTokenAndTLS(t *testing.T) {
	clearAccessEnv(t)
	t.Setenv(envAccessListen, "tcp://0.0.0.0:7443")
	t.Setenv(envAccessTLSCertFile, "/c.pem")
	t.Setenv(envAccessTLSKeyFile, "/k.pem")
	if _, err := accessFromEnv(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("tcp listen without token: err = %v", err)
	}
	clearAccessEnv(t)
	t.Setenv(envAccessListen, "tcp://0.0.0.0:7443")
	t.Setenv(envAccessToken, "t")
	if _, err := accessFromEnv(); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("tcp listen without TLS: err = %v", err)
	}
	// A unix socket on the host may run as before.
	clearAccessEnv(t)
	t.Setenv(envAccessListen, "unix:/run/granule/access.sock")
	if _, err := accessFromEnv(); err != nil {
		t.Fatalf("unix listen: %v", err)
	}
}

func TestExportNetshimAccess(t *testing.T) {
	clearAccessEnv(t)
	t.Setenv(envAccessListen, "tcp://0.0.0.0:7443")
	t.Setenv(envAccessToken, "t")
	t.Setenv(envAccessTLSCertFile, "/c.pem")
	t.Setenv(envAccessTLSKeyFile, "/k.pem")
	t.Setenv(envAccessDial, "https://10.9.0.4:8443")
	t.Setenv(envAccessSandboxID, "sbx")
	t.Setenv(envAccessControllerIPs, "10.9.0.5")
	t.Setenv(envNetshimControllers, "10.9.0.5,fd00::1")
	a, err := accessFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := exportNetshimAccess(a); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(envNetshimAccessPorts); got != "7443" {
		t.Errorf("%s = %q, want 7443", envNetshimAccessPorts, got)
	}
	if got := os.Getenv(envNetshimControllers); got != "10.9.0.5,fd00::1,10.9.0.4" {
		t.Errorf("%s = %q", envNetshimControllers, got)
	}
}
