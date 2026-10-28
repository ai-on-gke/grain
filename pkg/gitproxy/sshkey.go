package gitproxy

// SSH key credentials: the third auth type beside a PAT (<name>.token)
// and a GitHub App (<name>.app.json), stored as <name>.ssh.json:
//
//	{
//	  "host": "git.example.com",
//	  "port": 22,
//	  "user": "git",
//	  "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...",
//	  "passphrase": "",
//	  "known_hosts": "git.example.com ssh-ed25519 AAAA...",
//	  "path_prefix": "",
//	  "api_credential": "team-x-api"
//	}
//
// The key never leaves the controller. Sandboxes go on speaking smart
// HTTP to the git proxy exactly as they do for a token; the proxy runs
// the upstream half of each request over SSH instead (sshforward.go).
//
// The host key is pinned, never learned: known_hosts is required, except
// on github.com, where GitHub's published host keys are the default
// (githubKnownHosts). A server presenting any other key is refused.
//
// An SSH key reaches git and nothing else. Pull requests, checks and
// merges need a forge API credential, so an SSH credential on a
// GitHub-kind host may name api_credential: a PAT or App credential on
// the same host, used for every REST call the SSH credential would
// otherwise have been asked to make (APICredential). Without one the
// repository is git-only, and grain says so where it would open or merge
// a pull request (GitOnlyReason).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bwsalmon/grain/pkg/model"
)

// githubKnownHosts is GitHub's published SSH host keys for github.com
// (docs.github.com, "GitHub's SSH key fingerprints"): the default
// known_hosts of an SSH credential on github.com. Only the Ed25519 and
// ECDSA keys are pinned; the client offers only the algorithms of the
// keys it has pinned, so the RSA key is never asked for.
const githubKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
`

// GitHubKnownHosts is githubKnownHosts, for a UI offering it as the
// default.
func GitHubKnownHosts() string { return githubKnownHosts }

// SSHKeyFile is <name>.ssh.json as written and read.
type SSHKeyFile struct {
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user,omitempty"`
	PrivateKey    string `json:"private_key"`
	Passphrase    string `json:"passphrase,omitempty"`
	KnownHosts    string `json:"known_hosts,omitempty"`
	PathPrefix    string `json:"path_prefix,omitempty"`
	APICredential string `json:"api_credential,omitempty"`
}

// SSHCredential is a loaded SSH key credential: everything a request
// needs to run git-upload-pack or git-receive-pack on its host.
type SSHCredential struct {
	// Host is the normalized git host the key is for.
	Host string
	// Addr is the host:port dialled.
	Addr string
	// User is the SSH login, "git" unless the file says otherwise.
	User string
	// PathPrefix is put before owner/repo.git in the remote command, for
	// a server whose repositories are not at the login's root.
	PathPrefix string
	// APICredential names the PAT or App credential REST calls for this
	// credential's repositories use instead, "" when there is none.
	APICredential string

	signer   ssh.Signer
	hostKeys []ssh.PublicKey
}

// ParseKnownHostsFor is the pinned keys in knownHosts for addr
// (host:port): every entry whose host patterns name it, in the form
// known_hosts writes ("host" on port 22, "[host]:port" otherwise). A
// hashed or wildcard entry is refused rather than skipped, so a
// known_hosts that pins nothing usable says so instead of reading as
// empty.
func ParseKnownHostsFor(knownHosts, addr string) ([]ssh.PublicKey, error) {
	want := knownhosts.Normalize(addr)
	var keys []ssh.PublicKey
	rest := []byte(knownHosts)
	for len(bytes.TrimSpace(rest)) > 0 {
		marker, hosts, key, _, next, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			if strings.Contains(err.Error(), "EOF") {
				break
			}
			return nil, fmt.Errorf("known_hosts: %v", err)
		}
		rest = next
		if marker != "" {
			return nil, fmt.Errorf("known_hosts: @%s entries are not supported", marker)
		}
		for _, h := range hosts {
			if strings.HasPrefix(h, "|") || strings.ContainsAny(h, "*?!") {
				return nil, fmt.Errorf("known_hosts: hashed and wildcard host patterns are not supported (%q); "+
					"write the host name plainly", h)
			}
			if knownhosts.Normalize(h) == want {
				keys = append(keys, key)
				break
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("known_hosts pins no host key for %s -- grain never trusts a host key on first use; "+
			"add the server's key (ssh-keyscan %s, checked against what its operator publishes)", want, hostName(addr))
	}
	return keys, nil
}

// sshAddr is host[:port] as the address dialled: port when set, else
// any port the host itself carries, else 22.
func sshAddr(host string, port int) string {
	name := hostName(host)
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(name, strconv.Itoa(port))
}

// parseSSHKeyFile checks f and turns it into an SSHCredential, filling in
// GitHub's host keys on github.com when f pins none.
func parseSSHKeyFile(f SSHKeyFile) (SSHKeyFile, *SSHCredential, error) {
	host, err := model.NormalizeGitHost(f.Host)
	if err != nil {
		return f, nil, err
	}
	if host == "" {
		return f, nil, fmt.Errorf("an SSH credential's host is required")
	}
	f.Host = host
	if f.Port < 0 || f.Port > 65535 {
		return f, nil, fmt.Errorf("SSH port %d is out of range", f.Port)
	}
	f.User = strings.TrimSpace(f.User)
	if f.User == "" {
		f.User = "git"
	}
	if strings.ContainsAny(f.User, "@: \t\r\n") {
		return f, nil, fmt.Errorf("SSH user %q is not a login name", f.User)
	}
	f.PathPrefix = strings.TrimSpace(f.PathPrefix)
	if strings.ContainsAny(f.PathPrefix, "'\\\r\n") || strings.Contains(f.PathPrefix, "..") {
		return f, nil, fmt.Errorf("SSH path prefix %q may not contain quotes, backslashes, newlines or ..", f.PathPrefix)
	}
	if strings.TrimSpace(f.PrivateKey) == "" {
		return f, nil, fmt.Errorf("a private key is required")
	}
	if !strings.HasSuffix(f.PrivateKey, "\n") {
		f.PrivateKey += "\n"
	}
	var signer ssh.Signer
	if f.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(f.PrivateKey), []byte(f.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(f.PrivateKey))
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if ok := asPassphraseMissing(err, &missing); ok {
			return f, nil, fmt.Errorf("the private key is encrypted: give its passphrase")
		}
		return f, nil, fmt.Errorf("the private key will not parse: %v", err)
	}
	if strings.TrimSpace(f.KnownHosts) == "" && hostName(host) == model.DefaultGitHost && f.Port == 0 {
		f.KnownHosts = githubKnownHosts
	}
	addr := sshAddr(host, f.Port)
	keys, err := ParseKnownHostsFor(f.KnownHosts, addr)
	if err != nil {
		return f, nil, err
	}
	f.APICredential = strings.TrimSpace(f.APICredential)
	return f, &SSHCredential{
		Host: host, Addr: addr, User: f.User, PathPrefix: f.PathPrefix,
		APICredential: f.APICredential, signer: signer, hostKeys: keys,
	}, nil
}

// ParseSSHKey checks f as SetSSHKey would and returns it as a Credential
// named name, without writing anything: what a Test of a key not yet
// saved runs with.
func ParseSSHKey(name string, f SSHKeyFile) (Credential, error) {
	_, cred, err := parseSSHKeyFile(f)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Name: name, SSH: cred}, nil
}

func asPassphraseMissing(err error, target **ssh.PassphraseMissingError) bool {
	if e, ok := err.(*ssh.PassphraseMissingError); ok {
		*target = e
		return true
	}
	return false
}

// sshFile is name's SSH key path.
func (c *CredentialSet) sshFile(name string) string {
	return filepath.Join(c.dir, name+".ssh.json")
}

// IsSSH reports whether name is backed by a <name>.ssh.json file.
func (c *CredentialSet) IsSSH(name string) bool {
	if namesOneFile(name) != nil {
		return false
	}
	_, err := os.Stat(c.sshFile(name))
	return err == nil
}

// Auth types, as AuthType reports them and the UI's typed editor names
// them.
const (
	AuthTypePAT       = "pat"
	AuthTypeSSH       = "ssh"
	AuthTypeApp       = "app"
	AuthTypeAnonymous = "none"
)

// AuthType is which kind of credential name is: AuthTypeSSH, AuthTypeApp
// or AuthTypePAT for its file, AuthTypeAnonymous for "anonymous", and ""
// for nothing configured. load prefers them in that order when a name
// somehow has more than one file.
func (c *CredentialSet) AuthType(name string) string {
	switch {
	case name == "anonymous":
		return AuthTypeAnonymous
	case c.IsSSH(name):
		return AuthTypeSSH
	case c.IsAmbient(name):
		return AuthTypeAmbient
	case c.IsApp(name):
		return AuthTypeApp
	case c.exists(name):
		return AuthTypePAT
	}
	return ""
}

// loadSSHCredential reads name.ssh.json. ok is false when there is no
// such file; a file that is there but unusable is an error, which load
// turns into a refusal rather than an anonymous request.
func (c *CredentialSet) loadSSHCredential(name string) (*SSHCredential, bool, error) {
	data, err := os.ReadFile(c.sshFile(name))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var f SSHKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, true, fmt.Errorf("%s.ssh.json is not valid JSON: %v", name, err)
	}
	_, cred, err := parseSSHKeyFile(f)
	if err != nil {
		return nil, true, fmt.Errorf("%s.ssh.json: %v", name, err)
	}
	return cred, true, nil
}

// SetSSHKey writes f as name's credential: <name>.ssh.json, mode 0600,
// replacing any token or App credential of the same name, and a host
// record (kind) beside it. The file is checked first -- the key parses
// with its passphrase, known_hosts pins the host's key, and an API
// credential, if named, exists on the same host -- so nothing unusable
// is ever written.
func (c *CredentialSet) SetSSHKey(name string, f SSHKeyFile, kind model.GitHostKind) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	f, cred, err := parseSSHKeyFile(f)
	if err != nil {
		return err
	}
	if f.APICredential != "" {
		if err := c.checkAPIPair(name, f.APICredential, cred.Host); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.dir, name+".ssh.json", append(data, '\n')); err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if err := c.removeKinds(name, ".token", ".app.json", ".ambient.json"); err != nil {
		return err
	}
	if err := c.SetHost(name, CredentialHost{Host: cred.Host, Kind: kind}); err != nil {
		return err
	}
	c.forget(name)
	return nil
}

// checkAPIPair is whether api may be name's paired API credential: a PAT
// or App credential that exists, on the same host.
func (c *CredentialSet) checkAPIPair(name, api, host string) error {
	if api == name {
		return fmt.Errorf("an SSH credential cannot be its own API credential")
	}
	switch c.AuthType(api) {
	case AuthTypePAT, AuthTypeApp:
	case "":
		return fmt.Errorf("API credential %q is not configured", api)
	default:
		return fmt.Errorf("API credential %q is not a token or GitHub App", api)
	}
	if h := c.HostOf(api); !h.Recorded && hostName(host) != hostName(c.defaultHost()) ||
		h.Recorded && hostName(h.Host) != hostName(host) {
		return fmt.Errorf("API credential %q is for %s, not %s", api, h.Host, host)
	}
	return nil
}

// SetPAT writes token as name's credential, replacing an SSH key or App
// credential of the same name: the typed editor's PAT save, where
// SetToken alone refuses to overwrite an App.
func (c *CredentialSet) SetPAT(name, token string) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("a token value is required")
	}
	if err := c.removeKinds(name, ".ssh.json", ".app.json", ".ambient.json"); err != nil {
		return err
	}
	return c.SetToken(name, token)
}

// SetApp writes a GitHub App credential, <name>.app.json, replacing a
// token or SSH key of the same name. The private key must parse; an empty
// installationID leaves installation discovery per-repository at mint time.
func (c *CredentialSet) SetApp(name, appID, installationID, privateKey string) error {
	return c.SetAppWithWebhookSecret(name, appID, installationID, privateKey, "")
}

// SetAppWithWebhookSecret is SetApp with an optional webhook secret stored
// alongside the App's credentials in <name>.app.json.
func (c *CredentialSet) SetAppWithWebhookSecret(name, appID, installationID, privateKey, webhookSecret string) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	appID, installationID = strings.TrimSpace(appID), strings.TrimSpace(installationID)
	if _, err := strconv.ParseInt(appID, 10, 64); err != nil {
		return fmt.Errorf("the App ID must be a number")
	}
	if installationID != "" {
		if _, err := strconv.ParseInt(installationID, 10, 64); err != nil {
			return fmt.Errorf("the installation ID must be a number")
		}
	}
	if _, err := ParseAppPrivateKey([]byte(privateKey)); err != nil {
		return err
	}
	payload := map[string]string{
		"app_id":      appID,
		"private_key": privateKey,
	}
	if installationID != "" {
		payload["installation_id"] = installationID
	}
	if strings.TrimSpace(webhookSecret) != "" {
		payload["webhook_secret"] = strings.TrimSpace(webhookSecret)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.dir, name+".app.json", append(data, '\n')); err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if err := c.removeKinds(name, ".token", ".ssh.json", ".ambient.json"); err != nil {
		return err
	}
	c.forget(name)
	return nil
}

// removeKinds deletes name's credential files with the given suffixes,
// when they exist.
func (c *CredentialSet) removeKinds(name string, suffixes ...string) error {
	for _, suffix := range suffixes {
		if err := os.Remove(filepath.Join(c.dir, name+suffix)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replacing credential %q: %w", name, err)
		}
	}
	c.forget(name)
	return nil
}

// SSHKeyInfo is what may be said about name's SSH key without its
// material: the host, login and fingerprint, and the paired API
// credential. ok is false when name is not an SSH credential.
type SSHKeyInfo struct {
	Host          string `json:"host"`
	User          string `json:"user"`
	Fingerprint   string `json:"fingerprint"`
	APICredential string `json:"apiCredential,omitempty"`
	Error         string `json:"error,omitempty"`
}

// SSHKeyInfoOf is name's SSHKeyInfo.
func (c *CredentialSet) SSHKeyInfoOf(name string) (SSHKeyInfo, bool) {
	if !c.IsSSH(name) {
		return SSHKeyInfo{}, false
	}
	cred, _, err := c.loadSSHCredential(name)
	if err != nil {
		return SSHKeyInfo{Error: err.Error()}, true
	}
	return SSHKeyInfo{
		Host: cred.Host, User: cred.User, APICredential: cred.APICredential,
		Fingerprint: ssh.FingerprintSHA256(cred.signer.PublicKey()),
	}, true
}

// APICredential is the credential REST calls go out under on name's
// behalf: name itself for a token or App, its paired api_credential for
// an SSH key. ok is false for an SSH key with no pair, whose
// repositories are git-only (GitOnlyReason).
func (c *CredentialSet) APICredential(cred Credential) (Credential, bool) {
	if cred.SSH == nil {
		return cred, true
	}
	if cred.SSH.APICredential == "" {
		return Credential{Name: cred.Name}, false
	}
	api, ok := c.Get(cred.SSH.APICredential)
	if !ok || api.SSH != nil {
		return Credential{Name: cred.Name}, false
	}
	return api, true
}

// GitOnlyReason is the sentence a pull request, check or merge on
// owner/repo is refused with when cred is an SSH key with no API
// credential paired to it, or "" when REST calls can go ahead.
func GitOnlyReason(cred Credential, owner, repo string) string {
	if cred.SSH == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s is reached with SSH key credential %q, which covers git only: grain can clone, "+
		"fetch and push it but cannot open, check or merge a pull request there -- pair the SSH key with a "+
		"token or GitHub App on %s in Settings → Git (/settings/git)", owner, repo, cred.Name, cred.SSH.Host)
}
