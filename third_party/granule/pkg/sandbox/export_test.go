package sandbox

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

// Test helpers shared with the external sandbox_test package, whose tests
// (relay_test.go) import pkg/relay, which itself imports this package.
var (
	RunClientSuite       = runClientSuite
	InProcessGuestDialer = inProcessGuestDialer
	CurrentUsername      = currentUsername
)

// TestPKI exposes newTestPKI's CA and certificates to the external tests.
type TestPKI struct{ p *pkiBundle }

func NewTestPKI(t *testing.T) TestPKI { return TestPKI{newTestPKI(t)} }

func (k TestPKI) Pool() *x509.CertPool        { return k.p.caPool }
func (k TestPKI) ServerCert() tls.Certificate { return k.p.serverCert }
func (k TestPKI) CACert() *x509.Certificate   { return k.p.caCert }
func (k TestPKI) ClientCert(t *testing.T, cn string) tls.Certificate {
	return k.p.issueClientCert(t, cn)
}
