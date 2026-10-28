package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// ServerConfig is one mcpServers entry (§3.1.1), keyed by capability_id in
// grain/config: either a stdio server (Command, Args, Env) or an HTTP one
// (ServerURL, Headers). Values in Env and Headers may hold $secret:<name>
// references, resolved only in memory by Resolve.
type ServerConfig struct {
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	ServerURL string            `json:"serverUrl,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
	// Run is where a stdio server runs: "" in the daemon's own
	// environment, or RunHost on the bare machine, through grain hostd,
	// for a daemon in the release container. A host server's Command is
	// the name of a capability entry on hostd's allowlist rather than a
	// binary: the daemon names an entry, and the allowlist says what that
	// starts.
	Run string `json:"run,omitempty"`
	// Config is the server's own configuration, for a server that
	// advertises the configure tool (§4.5): validated against that tool's
	// config schema and sent to it after every initialize. A secret field
	// holds only a $secret:<name> reference, resolved in memory by
	// ResolveConfig.
	Config map[string]any `json:"config,omitempty"`
	// Builtin names the in-tree capability server (pkg/capability/intree)
	// this entry is, for an entry grain registers itself under that
	// server's canonical capability_id: one that cannot be renamed or
	// removed, only disabled. Empty for an operator's own entry.
	Builtin string `json:"builtin,omitempty"`
	// Aliases are capability_ids grants may still name for this entry:
	// the operator-chosen ids of the hand-added entries a built-in
	// adopted, which keep resolving to it.
	Aliases []string `json:"aliases,omitempty"`
	// Notice is a one-time message Settings shows on the entry until it
	// is dismissed -- what adopting a hand-added entry changed.
	Notice string `json:"notice,omitempty"`
}

// RunHost is the ServerConfig.Run of a stdio server started through grain
// hostd.
const RunHost = "host"

// Stdio reports whether the entry is a stdio server.
func (c ServerConfig) Stdio() bool { return c.Command != "" }

// OnHost reports whether the entry is a stdio server started through
// grain hostd.
func (c ServerConfig) OnHost() bool { return c.Stdio() && c.Run == RunHost }

// ParseServers decodes an mcpServers object -- the value of the
// "mcpServers" key, or a whole document holding one -- and validates every
// entry (§3.1.1). Unknown entry fields are refused, so a misspelt
// serverUrl cannot leave an entry silently transportless.
func ParseServers(data []byte) (map[string]ServerConfig, error) {
	var doc struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err == nil && len(doc.MCPServers) > 0 {
		data = doc.MCPServers
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mcpServers: %v", err)
	}
	out := make(map[string]ServerConfig, len(raw))
	for id, entry := range raw {
		dec := json.NewDecoder(bytes.NewReader(entry))
		dec.DisallowUnknownFields()
		var c ServerConfig
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("mcpServers[%q]: %v", id, err)
		}
		if err := c.Validate(id); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, nil
}

// Validate checks one entry: its key is a capability_id (§2.1.2), it names
// exactly one transport and does not mix their fields (§3.1.1), a relative
// command does not escape the grain/config root, and serverUrl is http(s).
func (c ServerConfig) Validate(id string) error {
	if err := ValidateCapabilityID(id); err != nil {
		return fmt.Errorf("mcpServers: %v", err)
	}
	switch {
	case c.Command != "" && c.ServerURL != "":
		return fmt.Errorf("mcpServers[%q]: names both command and serverUrl", id)
	case c.Command == "" && c.ServerURL == "":
		return fmt.Errorf("mcpServers[%q]: names neither command nor serverUrl", id)
	case c.Command != "" && len(c.Headers) > 0:
		return fmt.Errorf("mcpServers[%q]: headers belong to an HTTP server, not a stdio one", id)
	case c.ServerURL != "" && (len(c.Args) > 0 || len(c.Env) > 0):
		return fmt.Errorf("mcpServers[%q]: args and env belong to a stdio server, not an HTTP one", id)
	}
	switch {
	case c.Run != "" && c.Run != RunHost:
		return fmt.Errorf("mcpServers[%q]: run %q: the only run is %q, through grain hostd", id, c.Run, RunHost)
	case c.Run == RunHost && c.Command == "":
		return fmt.Errorf("mcpServers[%q]: run: host is for a stdio server; an HTTP server on the host is reached at its serverUrl", id)
	case c.Run == RunHost:
		if !validHostdEntryName(c.Command) {
			return fmt.Errorf("mcpServers[%q]: with run: host, command names grain hostd's capability entry "+
				"(letters, digits, '-', '_' and '.'), not a path: got %q", id, c.Command)
		}
	case c.Command != "":
		if _, err := c.resolveCommand(""); err != nil {
			return fmt.Errorf("mcpServers[%q]: %v", id, err)
		}
	}
	for _, alias := range c.Aliases {
		if err := ValidateCapabilityID(alias); err != nil {
			return fmt.Errorf("mcpServers[%q]: alias: %v", id, err)
		}
		if alias == id {
			return fmt.Errorf("mcpServers[%q]: an entry cannot be an alias of itself", id)
		}
	}
	if c.ServerURL != "" {
		u, err := url.Parse(c.ServerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("mcpServers[%q]: serverUrl %q must be an http:// or https:// URL", id, c.ServerURL)
		}
		if u.User != nil {
			return fmt.Errorf("mcpServers[%q]: serverUrl must not carry userinfo; put credentials in headers", id)
		}
	}
	for name, v := range c.Env {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf("mcpServers[%q]: env name %q is not a valid variable name", id, name)
		}
		if err := checkSecretRef(v); err != nil {
			return fmt.Errorf("mcpServers[%q]: env %s: %v", id, name, err)
		}
	}
	for name, v := range c.Headers {
		if err := checkSecretRef(v); err != nil {
			return fmt.Errorf("mcpServers[%q]: header %s: %v", id, name, err)
		}
	}
	if err := checkConfigRefs(c.Config); err != nil {
		return fmt.Errorf("mcpServers[%q]: %v", id, err)
	}
	return nil
}

// validHostdEntryName is hostd's own rule for an allowlist entry's name.
func validHostdEntryName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// resolveCommand turns Command into the path Grain executes: a bare name
// (grain) is looked up on PATH when spawned, a clean absolute path is used
// as written, and a relative path is resolved under configRoot, the
// checked-out grain/config, and must not escape it.
func (c ServerConfig) resolveCommand(configRoot string) (string, error) {
	cmd := c.Command
	if !strings.Contains(cmd, "/") {
		return cmd, nil
	}
	if filepath.IsAbs(cmd) {
		if filepath.Clean(cmd) != cmd {
			return "", fmt.Errorf("command %q is not a clean absolute path", cmd)
		}
		return cmd, nil
	}
	for _, seg := range strings.Split(cmd, "/") {
		if seg == ".." {
			return "", fmt.Errorf("command %q must not contain .. segments", cmd)
		}
	}
	clean := path.Clean(cmd)
	if configRoot == "" {
		return clean, nil
	}
	return filepath.Join(configRoot, filepath.FromSlash(clean)), nil
}

const secretPrefix = "$secret:"

func checkSecretRef(v string) error {
	if !strings.Contains(v, secretPrefix) {
		return nil
	}
	_, refs := splitSecretRefs(v)
	for _, r := range refs {
		if r == "" {
			return fmt.Errorf("%q has an empty $secret: name", v)
		}
	}
	return nil
}

// splitSecretRefs finds every $secret:<name> in v. A name runs to the next
// whitespace, so "Bearer $secret:tok" references tok.
func splitSecretRefs(v string) ([]string, []string) {
	var lits, refs []string
	for {
		i := strings.Index(v, secretPrefix)
		if i < 0 {
			lits = append(lits, v)
			return lits, refs
		}
		lits = append(lits, v[:i])
		rest := v[i+len(secretPrefix):]
		end := strings.IndexAny(rest, " \t\r\n")
		if end < 0 {
			end = len(rest)
		}
		refs = append(refs, rest[:end])
		v = rest[end:]
	}
}

// SecretLookup returns the value stored under a secret name (<name> or
// <secret>/<key>) in Grain's encrypted secrets store, and false when it is
// absent.
type SecretLookup func(name string) (string, bool)

// SecretRefs lists, sorted, every secret name the entry references.
func (c ServerConfig) SecretRefs() []string {
	seen := map[string]bool{}
	for _, m := range []map[string]string{c.Env, c.Headers} {
		for _, v := range m {
			_, refs := splitSecretRefs(v)
			for _, r := range refs {
				seen[r] = true
			}
		}
	}
	for _, v := range c.Config {
		if name, ok := configSecretRef(v); ok {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Resolve replaces every $secret:<name> in Env and Headers with its value
// (§3.1.2). A missing or empty secret refuses the attach, naming the secret
// and never any value. The returned copy must be kept in memory only.
func (c ServerConfig) Resolve(lookup SecretLookup) (ServerConfig, error) {
	var missing []string
	resolve := func(in map[string]string) map[string]string {
		if in == nil {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			lits, refs := splitSecretRefs(v)
			var b strings.Builder
			for i, lit := range lits {
				b.WriteString(lit)
				if i < len(refs) {
					val, ok := lookup(refs[i])
					if !ok || val == "" {
						missing = append(missing, refs[i])
						continue
					}
					b.WriteString(val)
				}
			}
			out[k] = b.String()
		}
		return out
	}
	r := c
	r.Args = append([]string(nil), c.Args...)
	r.Env = resolve(c.Env)
	r.Headers = resolve(c.Headers)
	if len(missing) > 0 {
		sort.Strings(missing)
		return ServerConfig{}, fmt.Errorf("capability server references secrets that are missing or empty: %s", strings.Join(dedupe(missing), ", "))
	}
	return r, nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// inheritedEnv are the variables a stdio server receives from Grain's own
// environment (§3.1.3); HOME is replaced by a private directory.
var inheritedEnv = []string{"PATH", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TMPDIR", "TERM", "XDG_RUNTIME_DIR", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// ProbeEnv is set to "1" in the environment of a stdio server Grain
// starts only to probe it -- Settings' Test -- while the entry's own
// instance may already be attached beside it (§3.1.3). A probe never
// mints, so a server whose data plane listens on a fixed port binds an
// ephemeral one instead of failing on the port the attached instance
// holds. It is hostd's name for it too, which every host capability
// entry accepts.
const ProbeEnv = hostd.ProbeEnv

// childEnv builds a stdio server's isolated environment: the base
// variables from environ, LC_*, HOME=home, overlaid with the resolved env.
func childEnv(environ []string, home string, env map[string]string) []string {
	keep := map[string]bool{}
	for _, k := range inheritedEnv {
		keep[k] = true
	}
	vars := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && (keep[k] || strings.HasPrefix(k, "LC_")) {
			vars[k] = v
		}
	}
	vars["HOME"] = home
	for k, v := range env {
		vars[k] = v
	}
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

var osEnviron = os.Environ
