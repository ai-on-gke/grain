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
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path"
	"time"
)

// MaxCredentialLife caps how long a sandbox's client certificate and
// token are minted for, whatever life the sandbox asks for
// (docs/design/grain-on-gke.md, "Credentials, per sandbox").
const MaxCredentialLife = 7 * 24 * time.Hour

// MountDir is where the wrapper container finds a sandbox's
// credentials: the Secret grain-access-<pod>, mounted read-only into
// the wrapper container only. The guest never sees it.
const MountDir = "/var/run/granule/access"

// The Secret's keys, and so the file names under MountDir.
const (
	KeyToken = "token"
	KeyCert  = "tls.crt"
	KeyKey   = "tls.key"
	KeyCA    = "ca.crt"
)

// SecretName is the Kubernetes Secret a pod's credentials live in.
func SecretName(pod string) string { return "grain-access-" + pod }

// SandboxURI is the URI SAN a sandbox's client certificate carries:
// grain-sandbox://<deployment>/<sandbox>.
func SandboxURI(deployment, sandbox string) *url.URL {
	return &url.URL{Scheme: "grain-sandbox", Host: deployment, Path: "/" + sandbox}
}

// Credentials are what one sandbox's wrapper needs to dial the
// acceptor. Token is held nowhere else: the acceptor keeps only
// TokenHash(Token).
type Credentials struct {
	Sandbox  string
	Token    string
	CertPEM  []byte
	KeyPEM   []byte
	CAPEM    []byte
	NotAfter time.Time
}

// SecretData is Credentials as the Secret's data, keyed by file name.
func (c Credentials) SecretData() map[string][]byte {
	return map[string][]byte{
		KeyToken: []byte(c.Token),
		KeyCert:  c.CertPEM,
		KeyKey:   c.KeyPEM,
		KeyCA:    c.CAPEM,
	}
}

// WrapperEnv is the dial-out configuration granule-shim reads
// (cmd/granule-shim/access.go's accessFromEnv): where to dial, as which
// sandbox, and the files under MountDir to authenticate with.
// controllerIPs are the acceptor's addresses as the guest would see
// them -- the Service's ClusterIP and the grain pod's IP -- which
// granule's netshim drops guest-originated packets to in
// CHV_NET_MODE=inline (pkg/netshim/splice.go, controllerDropRules).
func WrapperEnv(dial, sandbox string, controllerIPs []string) map[string]string {
	env := map[string]string{
		"GRANULE_ACCESS_DIAL":       dial,
		"GRANULE_ACCESS_SANDBOX_ID": sandbox,
		"GRANULE_ACCESS_TOKEN_FILE": path.Join(MountDir, KeyToken),
		"GRANULE_ACCESS_CA_FILE":    path.Join(MountDir, KeyCA),
		"GRANULE_ACCESS_CERT_FILE":  path.Join(MountDir, KeyCert),
		"GRANULE_ACCESS_KEY_FILE":   path.Join(MountDir, KeyKey),
	}
	if len(controllerIPs) > 0 {
		ips := ""
		for i, ip := range controllerIPs {
			if i > 0 {
				ips += ","
			}
			ips += ip
		}
		env["GRANULE_ACCESS_CONTROLLER_IPS"] = ips
	}
	return env
}

// TokenHash is the SHA-256 of a token, hex-encoded: what is stored for
// a sandbox in place of the token itself.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newToken is 256 random bits, hex-encoded.
func newToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("sandboxaccess: generating token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func addHosts(tmpl *x509.Certificate, hosts []string) {
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
}
