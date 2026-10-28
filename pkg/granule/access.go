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

package granule

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/sandbox"
)

// Constants for granule listen-mode sandbox access and granule relay.
const (
	DefaultAccessPort      = 7443
	DefaultAccessListen    = "tcp://0.0.0.0:7443"
	DefaultAccessMountPath = "/var/run/granule/access"

	DefaultRelayPort       = 8443
	DefaultRelayServerName = "grain-sandbox-relay"
	DefaultRelayDir        = "/var/lib/grain/gke-sandboxes"
	DefaultRelayConfigPath = "/var/lib/grain/gke-sandboxes/relay.json"

	SandboxAccessLabel        = "granule.gke.io/sandbox"
	SandboxAccessValue        = "true"
	SandboxAccessLabelValue   = "true"
	SandboxNameLabel          = "granule.gke.io/sandbox-name"
	SandboxNetworkAccessLabel = "granule.gke.io/network-access"
	SandboxNetworkOpenValue   = "open"
)

// SandboxSecretName returns the name of the per-pod Secret that carries a
// sandbox pod's access token and TLS keypair.
func SandboxSecretName(podName string) string {
	return podName + "-access"
}

// RelayConfig describes how an off-cluster grain controller connects to
// `granule relay` (serialized at /var/lib/grain/gke-sandboxes/relay.json).
type RelayConfig struct {
	Addr       string `json:"addr"`
	CAFile     string `json:"ca_file,omitempty"`
	CACertPEM  string `json:"ca_cert_pem,omitempty"`
	CertFile   string `json:"cert_file,omitempty"`
	CertPEM    string `json:"cert_pem,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	KeyPEM     string `json:"key_pem,omitempty"`
	Token      string `json:"token,omitempty"`
	TokenFile  string `json:"token_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

// LoadRelayConfig reads and parses a relay.json configuration file.
func LoadRelayConfig(path string) (*RelayConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg RelayConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing relay config %s: %w", path, err)
	}
	cfg.Addr = strings.TrimSpace(cfg.Addr)
	if cfg.Addr == "" {
		return nil, fmt.Errorf("relay config %s has empty addr", path)
	}
	return &cfg, nil
}

// SaveRelayConfig writes cfg as indented JSON to path with mode 0600.
func SaveRelayConfig(path string, cfg RelayConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// RelayCredentials builds sandbox.RelayCredentials from c, loading any
// referenced CA, client mTLS certificate/key, and controller token files.
func (c RelayConfig) RelayCredentials() (sandbox.RelayCredentials, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	if sni := strings.TrimSpace(c.ServerName); sni != "" {
		tlsCfg.ServerName = sni
	}

	caBytes := []byte(c.CACertPEM)
	if len(caBytes) == 0 && c.CAFile != "" {
		var err error
		caBytes, err = os.ReadFile(c.CAFile)
		if err != nil {
			return sandbox.RelayCredentials{}, fmt.Errorf("reading relay CA file %s: %w", c.CAFile, err)
		}
	}
	if len(caBytes) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return sandbox.RelayCredentials{}, errors.New("relay CA contains no valid certificates")
		}
		tlsCfg.RootCAs = pool
	}

	certBytes := []byte(c.CertPEM)
	keyBytes := []byte(c.KeyPEM)
	if len(certBytes) == 0 && c.CertFile != "" {
		var err error
		certBytes, err = os.ReadFile(c.CertFile)
		if err != nil {
			return sandbox.RelayCredentials{}, fmt.Errorf("reading relay client cert %s: %w", c.CertFile, err)
		}
	}
	if len(keyBytes) == 0 && c.KeyFile != "" {
		var err error
		keyBytes, err = os.ReadFile(c.KeyFile)
		if err != nil {
			return sandbox.RelayCredentials{}, fmt.Errorf("reading relay client key %s: %w", c.KeyFile, err)
		}
	}
	if len(certBytes) > 0 || len(keyBytes) > 0 {
		cert, err := tls.X509KeyPair(certBytes, keyBytes)
		if err != nil {
			return sandbox.RelayCredentials{}, fmt.Errorf("loading relay client keypair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	token := strings.TrimSpace(c.Token)
	if token == "" && c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return sandbox.RelayCredentials{}, fmt.Errorf("reading relay token file %s: %w", c.TokenFile, err)
		}
		token = strings.TrimSpace(string(b))
	}

	return sandbox.RelayCredentials{
		Token:     token,
		TLSConfig: tlsCfg,
	}, nil
}

// RelayPKI holds the mTLS CA, server cert/key, client cert/key, and
// controller bearer token minted for `granule relay`.
type RelayPKI struct {
	CAFile     string
	CAKeyFile  string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
	TokenFile  string

	CACertPEM     string
	ServerCertPEM string
	ServerKeyPEM  string
	ClientCertPEM string
	ClientKeyPEM  string
	Token         string
}

// ServerTLSConfig builds the *tls.Config for a `granule relay` server using
// the minted server certificate and client CA (mTLS).
func (p *RelayPKI) ServerTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(p.ServerCertPEM), []byte(p.ServerKeyPEM))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(p.CACertPEM)) {
		return nil, errors.New("granule: relay CA PEM holds no certificates")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// MintRelayPKI idempotently loads or generates the relay mTLS CA, server
// certificate, client certificate, and controller token under dir.
func MintRelayPKI(dir, namespace string) (*RelayPKI, error) {
	if namespace == "" {
		namespace = "grain"
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating relay directory %s: %w", dir, err)
	}
	pki := &RelayPKI{
		CAFile:     filepath.Join(dir, "ca.crt"),
		CAKeyFile:  filepath.Join(dir, "ca.key"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"),
		ClientKey:  filepath.Join(dir, "client.key"),
		TokenFile:  filepath.Join(dir, "token"),
	}

	// Reuse existing credentials if all files already exist and are non-empty.
	if caB, err1 := os.ReadFile(pki.CAFile); err1 == nil && len(caB) > 0 {
		sCertB, err2 := os.ReadFile(pki.ServerCert)
		sKeyB, err3 := os.ReadFile(pki.ServerKey)
		cCertB, err4 := os.ReadFile(pki.ClientCert)
		cKeyB, err5 := os.ReadFile(pki.ClientKey)
		tokB, err6 := os.ReadFile(pki.TokenFile)
		if err2 == nil && err3 == nil && err4 == nil && err5 == nil && err6 == nil &&
			len(sCertB) > 0 && len(sKeyB) > 0 && len(cCertB) > 0 && len(cKeyB) > 0 && len(strings.TrimSpace(string(tokB))) > 0 {
			pki.CACertPEM = string(caB)
			pki.ServerCertPEM = string(sCertB)
			pki.ServerKeyPEM = string(sKeyB)
			pki.ClientCertPEM = string(cCertB)
			pki.ClientKeyPEM = string(cKeyB)
			pki.Token = strings.TrimSpace(string(tokB))
			return pki, nil
		}
	}

	ca, err := NewSandboxCA()
	if err != nil {
		return nil, fmt.Errorf("generating relay CA: %w", err)
	}

	dnsNames := []string{
		DefaultRelayServerName,
		DefaultRelayServerName + "." + namespace,
		DefaultRelayServerName + "." + namespace + ".svc",
		DefaultRelayServerName + "." + namespace + ".svc.cluster.local",
		"localhost",
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	serverCertPEM, serverKeyPEM, err := ca.issueCert(DefaultRelayServerName, dnsNames, ips, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, fmt.Errorf("generating relay server cert: %w", err)
	}
	clientCertPEM, clientKeyPEM, err := ca.issueCert("grain-controller", nil, nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, fmt.Errorf("generating relay client cert: %w", err)
	}
	token, err := MintToken()
	if err != nil {
		return nil, fmt.Errorf("minting relay token: %w", err)
	}

	if err := os.WriteFile(pki.CAFile, ca.CAPEM(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.CAKeyFile, ca.KeyPEM(), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.ServerCert, serverCertPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.ServerKey, serverKeyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.ClientCert, clientCertPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.ClientKey, clientKeyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pki.TokenFile, []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}

	pki.CACertPEM = string(ca.CAPEM())
	pki.ServerCertPEM = string(serverCertPEM)
	pki.ServerKeyPEM = string(serverKeyPEM)
	pki.ClientCertPEM = string(clientCertPEM)
	pki.ClientKeyPEM = string(clientKeyPEM)
	pki.Token = token
	return pki, nil
}

// MintToken generates a 32-byte (64-hex-character) cryptographically random
// bearer token.
func MintToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// SandboxCA mints short-lived TLS server certificates for sandbox pods and
// verifies them from the controller or forked mcpserver processes.
type SandboxCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

// NewSandboxCA generates a new ECDSA P-256 root CA for sandbox access TLS.
func NewSandboxCA() (*SandboxCA, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-5 * time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "grain-sandbox-access-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &SandboxCA{
		cert:    cert,
		key:     priv,
		certPEM: certPEM,
		keyPEM:  keyPEM,
	}, nil
}

// CAPEM returns the PEM-encoded CA certificate.
func (ca *SandboxCA) CAPEM() []byte {
	return append([]byte(nil), ca.certPEM...)
}

// KeyPEM returns the PEM-encoded CA private key.
func (ca *SandboxCA) KeyPEM() []byte {
	return append([]byte(nil), ca.keyPEM...)
}

// IssueSandboxCert signs a server TLS certificate for a sandbox pod.
func (ca *SandboxCA) IssueSandboxCert(sandboxID, podName string) (certPEM, keyPEM []byte, err error) {
	var dns []string
	seen := map[string]bool{}
	for _, name := range []string{podName, sandboxID, "granule-sandbox", "localhost"} {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			dns = append(dns, name)
		}
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	cn := podName
	if cn == "" {
		cn = sandboxID
	}
	return ca.issueCert(cn, dns, ips, x509.ExtKeyUsageServerAuth)
}

func (ca *SandboxCA) issueCert(commonName string, dnsNames []string, ips []net.IP, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().Add(-5 * time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now,
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &priv.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// ClientTLSConfigFromCAPEM builds a TLS 1.3 client config that verifies the
// sandbox's server certificate against caPEM and serverName.
func ClientTLSConfigFromCAPEM(caPEM []byte, serverName string) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("sandbox CA PEM contains no valid certificates")
	}
	if serverName == "" {
		serverName = "granule-sandbox"
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
	}, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

const (
	AccessModeDirect = "direct"
	AccessModeRelay  = "relay"
)

// SandboxAccessSpec holds everything needed to connect to a running
// kubernetes-granule sandbox pod over direct Dial or DialRelay, both in-process
// and from a forked `grain mcpserver` or across a daemon restart (`Readopt`).
type SandboxAccessSpec struct {
	Mode       string       `json:"mode"` // "direct" or "relay"
	PodName    string       `json:"pod_name"`
	SandboxID  string       `json:"sandbox_id"`
	PodIP      string       `json:"pod_ip,omitempty"`
	Port       int          `json:"port"`
	Token      string       `json:"token"`
	CACertPEM  string       `json:"ca_cert_pem"`
	CertPEM    string       `json:"cert_pem,omitempty"`
	KeyPEM     string       `json:"key_pem,omitempty"`
	User       string       `json:"user,omitempty"`
	Namespace  string       `json:"namespace,omitempty"`
	Kubeconfig string       `json:"kubeconfig,omitempty"`
	Kubectl    string       `json:"kubectl,omitempty"`
	Relay      *RelayConfig `json:"relay,omitempty"`
}

// privateAccessDir creates dir (0700) if it is missing and refuses one
// another account could write to. An access spec carries a sandbox's
// bearer token and names the CA and address the controller trusts, so a
// directory someone else pre-created (the default lives under the
// shared os.TempDir) must not be able to hand the controller a spec of
// its own or read the ones written there. Ownership is checked outright
// rather than left to Chmod, which root passes for any directory and
// which is skipped for one already at 0700.
func privateAccessDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("sandbox access directory %s is not a directory", dir)
	}
	if !ownedByUs(fi) {
		return fmt.Errorf("sandbox access directory %s is owned by another account", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("sandbox access directory %s: %w", dir, err)
		}
	}
	return nil
}

// WriteSandboxAccessSpec writes spec to path with mode 0600, in a
// directory only this account may use.
func WriteSandboxAccessSpec(path string, spec SandboxAccessSpec) error {
	if err := privateAccessDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Remove first so a leftover file (or a symlink planted in its
	// place) cannot keep a wider mode or redirect the write.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadSandboxAccessSpec reads a SandboxAccessSpec from path, refusing a
// symlink, a file group or other may access, one in a directory they may
// write to, or a file or directory another account owns --
// WriteSandboxAccessSpec never leaves any of those.
func ReadSandboxAccessSpec(path string) (*SandboxAccessSpec, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("sandbox access spec %s is not a private regular file (mode %s)", path, fi.Mode())
	}
	if !ownedByUs(fi) {
		return nil, fmt.Errorf("sandbox access spec %s is owned by another account", path)
	}
	if di, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, err
	} else if di.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("sandbox access spec %s is in a directory group or other may write to (mode %s)", path, di.Mode())
	} else if !ownedByUs(di) {
		return nil, fmt.Errorf("sandbox access spec %s is in a directory another account owns", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec SandboxAccessSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing sandbox access spec %s: %w", path, err)
	}
	if spec.Port <= 0 {
		spec.Port = DefaultAccessPort
	}
	return &spec, nil
}

// ClientOptions builds the sandbox.DialOptions used to authenticate to the
// sandbox described by spec.
func (s SandboxAccessSpec) ClientOptions() (sandbox.DialOptions, error) {
	serverName := s.PodName
	if serverName == "" {
		serverName = s.SandboxID
	}
	tlsCfg, err := ClientTLSConfigFromCAPEM([]byte(s.CACertPEM), serverName)
	if err != nil {
		return sandbox.DialOptions{}, err
	}
	sandboxID := s.SandboxID
	if sandboxID == "" {
		sandboxID = s.PodName
	}
	return sandbox.DialOptions{
		SandboxID: sandboxID,
		Token:     s.Token,
		TLSConfig: tlsCfg,
	}, nil
}

// PersistentRelayPool maintains one shared outbound `sandbox.RelayClient`
// connection to `granule relay` and automatically reconnects if the session
// drops.
type PersistentRelayPool struct {
	cfg RelayConfig

	mu     sync.Mutex
	client *sandbox.RelayClient
}

// NewPersistentRelayPool creates a relay connection pool for cfg.
func NewPersistentRelayPool(cfg RelayConfig) *PersistentRelayPool {
	return &PersistentRelayPool{cfg: cfg}
}

// Connect opens a multiplexed stream through `granule relay` to target and
// completes the end-to-end TLS + bearer-token handshake with the sandbox pod.
func (p *PersistentRelayPool) Connect(ctx context.Context, target string, opts sandbox.DialOptions) (*sandbox.Client, error) {
	rc, err := p.getOrDial(ctx, false)
	if err != nil {
		return nil, err
	}
	client, err := rc.Client(ctx, target, opts)
	if err == nil {
		return client, nil
	}
	if !isClosedRelayErr(err) {
		return nil, err
	}
	rc, err = p.getOrDial(ctx, true)
	if err != nil {
		return nil, err
	}
	return rc.Client(ctx, target, opts)
}

func (p *PersistentRelayPool) getOrDial(ctx context.Context, forceNew bool) (*sandbox.RelayClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !forceNew && p.client != nil {
		return p.client, nil
	}
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
	creds, err := p.cfg.RelayCredentials()
	if err != nil {
		return nil, err
	}
	rc, err := sandbox.DialRelay(ctx, p.cfg.Addr, creds)
	if err != nil {
		return nil, err
	}
	p.client = rc
	return rc, nil
}

// Close closes the underlying RelayClient connection if open.
func (p *PersistentRelayPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return nil
	}
	err := p.client.Close()
	p.client = nil
	return err
}

func isClosedRelayErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "session closed") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "use of closed network connection")
}

// DialDirect resolves podName to a pod IP (using resolveIP or fixedIP) and
// dials the sandbox's TCP listener directly over TLS 1.3.
func DialDirect(ctx context.Context, podName string, port int, fixedIP string, resolveIP func(context.Context, string) (string, error), opts sandbox.DialOptions) (*sandbox.Client, error) {
	if port <= 0 {
		port = DefaultAccessPort
	}
	ip := strings.TrimSpace(fixedIP)
	if ip == "" && resolveIP != nil {
		var err error
		ip, err = resolveIP(ctx, podName)
		if err != nil {
			return nil, fmt.Errorf("resolving pod IP for %s: %w", podName, err)
		}
	}
	if ip == "" {
		return nil, fmt.Errorf("pod %s has no IP address yet", podName)
	}
	addr := ip
	if !strings.Contains(addr, "://") {
		if _, _, splitErr := net.SplitHostPort(ip); splitErr != nil {
			addr = net.JoinHostPort(ip, strconv.Itoa(port))
		}
		addr = "tls://" + addr
	}
	return sandbox.Dial(ctx, addr, opts)
}

// RelayConfigPath returns the path to relay.json inside dir.
func RelayConfigPath(dir string) string {
	return filepath.Join(dir, "relay.json")
}

// MintSandbox generates a per-sandbox bearer token and server TLS keypair for
// podName signed by ca.
func (ca *SandboxCA) MintSandbox(podName string) (*SandboxAccessSpec, error) {
	tok, err := MintToken()
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := ca.IssueSandboxCert(podName, podName)
	if err != nil {
		return nil, err
	}
	return &SandboxAccessSpec{
		PodName:   podName,
		SandboxID: podName,
		Port:      DefaultAccessPort,
		Token:     tok,
		CACertPEM: string(ca.CAPEM()),
		CertPEM:   string(certPEM),
		KeyPEM:    string(keyPEM),
	}, nil
}

// ServerTLSConfig returns a TLS 1.3 server configuration for the sandbox's
// minted server certificate and private key.
func (s SandboxAccessSpec) ServerTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(s.CertPEM), []byte(s.KeyPEM))
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}
