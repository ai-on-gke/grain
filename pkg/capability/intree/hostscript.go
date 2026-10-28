package intree

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/hostscriptcli"
)

// The host-script server (docs/capabilities.md, "Host scripts") runs
// operator-defined bash scripts where the server runs -- on the host,
// as the user, through grain hostd (run: host), or in the daemon's
// container -- on behalf of a sandbox. A grant, with: {script: <name>},
// places one descriptor per script at hostscriptcli.Dir/<name>.json in
// the sandbox; `grain host-script <name>` (pkg/hostscriptcli) reads it
// and sends its argv to this server's data-plane
// listener at /runs/<attempt_id>/exec under the grant's own token, and
// the script runs as `bash <private file> "$@"` with that argv exactly,
// never through a shell string. Script text lives only in the server's
// configure config: neither a grant nor anything in the sandbox can supply
// or change it.

// DefaultHostScriptPort is the host-script server's data-plane port
// unless -listen says otherwise: beside the git proxy's 9728 and the
// model proxy's 9729.
const DefaultHostScriptPort = 9733

// HostScriptBinDir is where a grant installs hostScriptFallback as
// `grain` when the sandbox has no grain of its own on PATH; Grain puts
// it first on run_command's PATH.
const HostScriptBinDir = "/home/debian/.grain/bin"

// hostScriptFallback is the stand-in `grain` (hostscriptcli's
// FallbackScript) a grant places beside its descriptor, and
// hostScriptSetup the grant's setup that installs it on PATH -- only
// where the sandbox's image carries no grain (Granulefile.guest-base
// bakes cmd/grain-sandbox in as /usr/local/bin/grain).
const (
	hostScriptFallback = hostscriptcli.Dir + "/grain"
	hostScriptSetup    = hostscriptcli.Dir + "/install-grain"
)

const hostScriptSetupScript = `#!/bin/sh
# grain's host-script capability: put a grain on PATH for
# ` + "`grain host-script`" + ` when this sandbox's image has none of its own.
found=$(command -v grain 2>/dev/null)
if [ -n "$found" ] && [ "$found" != ` + HostScriptBinDir + `/grain ] &&
  timeout 10 "$found" host-script --list >/dev/null 2>&1; then
  exit 0
fi
mkdir -p ` + HostScriptBinDir + ` && cp ` + hostScriptFallback + ` ` + HostScriptBinDir + `/grain.tmp && chmod 0755 ` + HostScriptBinDir + `/grain.tmp && mv -f ` + HostScriptBinDir + `/grain.tmp ` + HostScriptBinDir + `/grain || {
  echo "host-script: could not install grain at ` + HostScriptBinDir + `/grain; ` + "`grain host-script`" + ` will not be found" >&2
}
exit 0
`

// Defaults and ceilings of a script's limits.
const (
	DefaultHostScriptMaxOutput = 1 << 20
	DefaultHostScriptMaxCalls  = 1000
	DefaultHostScriptMaxArgs   = 256
	DefaultHostScriptMaxArgLen = 64 << 10
	// hostScriptKillGrace is how long a timed-out script's process group
	// has between SIGTERM and SIGKILL.
	hostScriptKillGrace = 2 * time.Second
	// hostScriptChunk bounds one output frame's raw bytes.
	hostScriptChunk = 4 << 10
)

var hostScriptName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// envName is a variable name a script's env may set.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hostScriptEnvRef is an env value naming one of the server's own
// environment variables, which the mcpServers entry binds to a secret.
const hostScriptEnvRef = "$env:"

// AmbientIdentityEnv are the variables of the server's own environment a
// script inherits: who it runs as and where that identity's
// configuration lives. Under run: host that is the user's own login; in
// the daemon's container, the container's.
var AmbientIdentityEnv = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "TZ",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "DBUS_SESSION_BUS_ADDRESS",
	"SSH_AUTH_SOCK", "KUBECONFIG", "CLOUDSDK_CONFIG", "GOOGLE_APPLICATION_CREDENTIALS",
	"AWS_PROFILE", "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "DOCKER_HOST",
}

// HostScript is one configured script.
type HostScript struct {
	Name        string
	Description string
	// Usage is shown by `grain host-script <name> --grain-help` and in
	// the prompt.
	Usage string
	// Script is the bash body, run as a file with the caller's argv.
	Script  string
	Timeout time.Duration
	// Env is the script's fixed variables; a value $env:NAME is the
	// server's own NAME, resolved at configure.
	Env map[string]string
	// Cwd is where the script runs; empty is the grant's own scratch
	// directory under the server's data directory.
	Cwd       string
	MaxOutput int
	MaxCalls  int
	MaxArgs   int
	MaxArgLen int
}

// HostScriptAudit is the record of one call (or one refused call).
type HostScriptAudit struct {
	Time      time.Time `json:"time"`
	AttemptID string    `json:"attempt_id"`
	Script    string    `json:"script"`
	// Argv is logged verbatim: a caller must never pass a secret as an
	// argument.
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Caller     string   `json:"caller"`
	// ArgvTruncated says Argv was cut to auditMaxArgs arguments of
	// auditMaxArgBytes each, so one call cannot write megabytes.
	ArgvTruncated bool   `json:"argv_truncated,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	Refused       string `json:"refused,omitempty"`
}

// HostScriptConfig configures the host-script server.
type HostScriptConfig struct {
	Scripts []HostScript
	// Listen is the data-plane listener's loopback address.
	Listen string
	// DataDir holds each grant's private script file and scratch
	// directory, and audit.jsonl.
	DataDir string
	// Environ is the server's own environment, which ambient identity
	// and $env: references come from; os.Environ unless a test says.
	Environ func() []string
	Now     func() time.Time
	Log     *log.Logger
}

// hostScriptConfigSchema is the server's configure schema (§4.5).
var hostScriptConfigSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"scripts": map[string]any{
			"type":        "array",
			"title":       "Scripts",
			"description": "The bash scripts a grant may let a sandbox run as `grain host-script <name>`, each run with the caller's argv exactly.",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"name", "description", "script", "timeout"},
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "pattern": hostScriptName.String(), "description": "The command's name in the sandbox, and what a grant's with: {script} names."},
					"description": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "What the script does, for the agent's prompt."},
					"usage":       map[string]any{"type": "string", "format": "textarea", "maxLength": 4096, "description": "Usage text, shown by `grain host-script <script> --grain-help` and in the prompt."},
					"script":      map[string]any{"type": "string", "format": "code", "minLength": 1, "maxLength": 1 << 20, "description": "The bash body. It runs as `bash <file> \"$@\"` and must parse and validate its own arguments, which are untrusted."},
					"timeout":     map[string]any{"type": "integer", "minimum": 1, "maximum": 86400, "description": "Seconds before the script's process group is killed."},
					"env": map[string]any{
						"type":                 "object",
						"description":          "Fixed variables. A value $env:NAME is the server's own variable NAME, which its mcpServers env binds to $secret:<name>.",
						"additionalProperties": map[string]any{"type": "string", "examples": []any{"$env:DEPLOY_TOKEN"}},
					},
					"cwd":         map[string]any{"type": "string", "pattern": "^/", "description": "Absolute working directory; defaults to a per-grant scratch directory."},
					"max_output":  map[string]any{"type": "integer", "minimum": 1, "description": "Bytes of stdout, and of stderr, returned before a truncation marker."},
					"max_calls":   map[string]any{"type": "integer", "minimum": 1, "description": "Calls one grant may make."},
					"max_args":    map[string]any{"type": "integer", "minimum": 0, "description": "Arguments one call may pass."},
					"max_arg_len": map[string]any{"type": "integer", "minimum": 1, "description": "Bytes one argument may hold."},
				},
			},
		},
	},
}

// parseHostScripts reads configure's scripts into HostScripts, with
// limits defaulted, names unique and $env: references resolved against
// environ.
func parseHostScripts(config map[string]any, environ []string) ([]HostScript, error) {
	own := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			own[k] = v
		}
	}
	list, _ := config["scripts"].([]any)
	var out []HostScript
	seen := map[string]bool{}
	for i, item := range list {
		m, _ := item.(map[string]any)
		s := HostScript{
			MaxOutput: DefaultHostScriptMaxOutput,
			MaxCalls:  DefaultHostScriptMaxCalls,
			MaxArgs:   DefaultHostScriptMaxArgs,
			MaxArgLen: DefaultHostScriptMaxArgLen,
		}
		s.Name, _ = m["name"].(string)
		s.Description, _ = m["description"].(string)
		s.Usage, _ = m["usage"].(string)
		s.Script, _ = m["script"].(string)
		s.Cwd, _ = m["cwd"].(string)
		if !hostScriptName.MatchString(s.Name) {
			return nil, fmt.Errorf("host-script: scripts[%d]: name %q does not match %s", i, s.Name, hostScriptName)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("host-script: script %q is configured twice", s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Description) == "" {
			return nil, fmt.Errorf("host-script: %s: description is required", s.Name)
		}
		if strings.TrimSpace(s.Script) == "" {
			return nil, fmt.Errorf("host-script: %s: script is empty", s.Name)
		}
		if strings.ContainsRune(s.Script, 0) || strings.ContainsRune(s.Usage, 0) || strings.ContainsRune(s.Description, 0) {
			return nil, fmt.Errorf("host-script: %s: script, usage and description may not hold a NUL byte", s.Name)
		}
		if s.Cwd != "" && (!filepath.IsAbs(s.Cwd) || filepath.Clean(s.Cwd) != s.Cwd) {
			return nil, fmt.Errorf("host-script: %s: cwd %q is not a clean absolute path", s.Name, s.Cwd)
		}
		secs, _ := configNumber(m["timeout"])
		if secs < 1 || secs > 86400 || secs != float64(int(secs)) {
			return nil, fmt.Errorf("host-script: %s: timeout must be a whole number of seconds from 1 to 86400", s.Name)
		}
		s.Timeout = time.Duration(secs) * time.Second
		for key, dst := range map[string]*int{"max_output": &s.MaxOutput, "max_calls": &s.MaxCalls, "max_args": &s.MaxArgs, "max_arg_len": &s.MaxArgLen} {
			if v, ok := configNumber(m[key]); ok {
				if v < 0 || v != float64(int(v)) || (v == 0 && key != "max_args") {
					return nil, fmt.Errorf("host-script: %s: %s must be a positive whole number", s.Name, key)
				}
				*dst = int(v)
			}
		}
		if env, ok := m["env"].(map[string]any); ok {
			s.Env = map[string]string{}
			for k, v := range env {
				val, _ := v.(string)
				if !envName.MatchString(k) {
					return nil, fmt.Errorf("host-script: %s: env name %q is not a variable name", s.Name, k)
				}
				if ref, ok := strings.CutPrefix(val, hostScriptEnvRef); ok {
					resolved, found := own[ref]
					if !found {
						return nil, fmt.Errorf("host-script: %s: env %s references $env:%s, which the server's own environment does not set; bind it in the mcpServers entry's env", s.Name, k, ref)
					}
					val = resolved
				}
				if strings.ContainsRune(val, 0) {
					return nil, fmt.Errorf("host-script: %s: env %s holds a NUL byte", s.Name, k)
				}
				s.Env[k] = val
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// configNumber is a config number: float64 from JSON, int from Go.
func configNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

// hostScriptAttempt is one live grant.
type hostScriptAttempt struct {
	id      string
	script  HostScript
	token   string
	dir     string // private: the script file and the scratch directory
	file    string
	scratch string

	ctx    context.Context // cancelled at release, killing calls in flight
	cancel context.CancelFunc

	mu    sync.Mutex
	calls int
	// refusals counts refused calls, which max_calls does not: past
	// the same number they are refused unrecorded (record).
	refusals int
	audit    []HostScriptAudit
	// inflight is how many calls are being served; once released is
	// set no more are admitted, and drained is closed when the last of
	// them has recorded itself (release).
	inflight int
	released bool
	drained  chan struct{}
}

// admit counts one call in, or reports that the grant has been released.
func (a *hostScriptAttempt) admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.released {
		return false
	}
	a.inflight++
	return true
}

// done counts one admitted call out.
func (a *hostScriptAttempt) done() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inflight--
	if a.released && a.inflight == 0 {
		close(a.drained)
	}
}

// hostScripts is the data plane: every live grant, by attempt_id.
type hostScripts struct {
	cfg       HostScriptConfig
	srv       *Server
	port      int
	listening bool

	mu       sync.Mutex
	attempts map[string]*hostScriptAttempt
	// scripts are the scripts configured now, by name, for test_script.
	scripts map[string]HostScript
	// retired is each released grant, until its audit has been read
	// into the release_ reply (newHostScriptMethod's Audit).
	retired map[string]*hostScriptAttempt
	auditMu sync.Mutex
}

func (h *hostScripts) lookup(id string) *hostScriptAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attempts[id]
}

// Audit is attemptID's call records so far.
func (h *hostScripts) Audit(attemptID string) []HostScriptAudit {
	a := h.lookup(attemptID)
	if a == nil {
		return nil
	}
	return a.records()
}

// takeRetired is a released grant's call records, which it forgets: the
// audit its release_ reply carries back to Grain (§4.4.4).
func (h *hostScripts) takeRetired(attemptID string) []HostScriptAudit {
	h.mu.Lock()
	a := h.retired[attemptID]
	delete(h.retired, attemptID)
	h.mu.Unlock()
	if a == nil {
		return nil
	}
	return a.records()
}

func (a *hostScriptAttempt) records() []HostScriptAudit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]HostScriptAudit(nil), a.audit...)
}

func (h *hostScripts) mint(attemptID string, s HostScript) (*protocol.MintResponse, error) {
	if !pathSafe(attemptID) {
		return refusedResponse("attempt_id " + attemptID + " cannot name a directory"), nil
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(h.cfg.DataDir, "attempts", attemptID)
	os.RemoveAll(dir)
	scratch := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, s.Name+".sh")
	if err := os.WriteFile(file, []byte(s.Script), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &hostScriptAttempt{id: attemptID, script: s, token: token, dir: dir, file: file, scratch: scratch, ctx: ctx, cancel: cancel, drained: make(chan struct{})}
	h.mu.Lock()
	if old := h.attempts[attemptID]; old != nil {
		old.cancel()
	}
	h.attempts[attemptID] = a
	h.mu.Unlock()

	descriptor, err := hostscriptcli.Marshal(hostscriptcli.Descriptor{
		Name: s.Name, Description: s.Description, Usage: s.Usage,
		Port: h.port, AttemptID: attemptID, Token: token,
	})
	if err != nil {
		return nil, err
	}
	return &protocol.MintResponse{
		Status: protocol.StatusOK,
		Ports:  []protocol.Port{{HostPort: h.port}},
		Placements: []protocol.Placement{
			{Path: hostscriptcli.Dir + "/" + s.Name + ".json", Mode: "0600", Content: string(descriptor)},
			// Identical in every grant, so placed once however many
			// scripts a run is granted (§5.1.2).
			{Path: hostScriptFallback, Mode: "0755", Content: hostscriptcli.FallbackScript},
			{Path: hostScriptSetup, Mode: "0700", Content: hostScriptSetupScript},
		},
		Setup:         hostScriptSetup,
		PromptSection: hostScriptPrompt(s),
	}, nil
}

// releaseDrainTimeout is how long a release waits for the calls it
// cancelled to record themselves before it gives up on including them.
const releaseDrainTimeout = 10 * time.Second

// release cancels attemptID's calls in flight, waits for them to record
// how they ended, and removes the grant's files. Its records stay in
// retired until the release_ reply has read them.
func (h *hostScripts) release(attemptID string) {
	h.mu.Lock()
	a := h.attempts[attemptID]
	delete(h.attempts, attemptID)
	h.mu.Unlock()
	if a == nil {
		return
	}
	a.mu.Lock()
	a.released = true
	if a.inflight == 0 {
		close(a.drained)
	}
	a.mu.Unlock()
	a.cancel()
	select {
	case <-a.drained:
	case <-time.After(releaseDrainTimeout):
		h.cfg.Log.Printf("host-script: release %s: calls still running after %s; their records are not in the release's audit", attemptID, releaseDrainTimeout)
	}
	os.RemoveAll(a.dir)
	h.mu.Lock()
	h.retired[attemptID] = a
	h.mu.Unlock()
}

func (h *hostScripts) closeAll() error {
	h.mu.Lock()
	ids := make([]string, 0, len(h.attempts))
	for id := range h.attempts {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.release(id)
	}
	return nil
}

// hostScriptPrompt is a grant's prompt section: which script is
// granted, what it does and how to call it through the grain CLI.
func hostScriptPrompt(s HostScript) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host script `%s` is granted to this sandbox: %s\n", s.Name, s.Description)
	fmt.Fprintf(&b, "Run it with `grain host-script %s [arguments...]`; `grain host-script %s --grain-help` prints its usage, and `grain host-script --list` lists every host script granted here. ", s.Name, s.Name)
	b.WriteString("It runs outside this sandbox, on grain's side, with the arguments you pass exactly as given -- literally, never through a shell; stdout, stderr and its exit status come back as a local command's would. Stdin is not forwarded. ")
	b.WriteString("Every call is logged with its arguments, so never pass a secret as one.\n")
	if s.Usage != "" {
		usage := s.Usage
		if len(usage) > 2048 {
			usage = usage[:2048] + "\n[usage truncated; run `grain host-script " + s.Name + " --grain-help` for all of it]"
		}
		b.WriteString("Usage:\n" + usage + "\n")
	}
	return b.String()
}

// hostScriptFrame is one output frame: tag, a space, data as a printf
// format, and a newline. Letters, digits, space and punctuation other
// than %, \ and - pass as themselves; every other byte is a three-digit
// octal escape.
func hostScriptFrame(tag byte, data []byte) []byte {
	out := make([]byte, 0, 3+len(data)*2)
	out = append(out, tag, ' ')
	for _, c := range data {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c >= ' ' && c <= '~' && c != '%' && c != '\\' && c != '-':
			out = append(out, c)
		default:
			out = append(out, '\\', '0'+(c>>6), '0'+((c>>3)&7), '0'+(c&7))
		}
	}
	return append(out, '\n')
}

// frameOut serializes the frames of one call onto its response -- or,
// for test_script, collects each stream's bytes in stdout and stderr.
type frameOut struct {
	mu sync.Mutex
	w  io.Writer
	f  http.Flusher

	collect        bool
	stdout, stderr bytes.Buffer
}

func (o *frameOut) frame(tag byte, data []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.collect {
		if tag == 'o' {
			o.stdout.Write(data)
		} else {
			o.stderr.Write(data)
		}
		return
	}
	for len(data) > 0 {
		n := min(len(data), hostScriptChunk)
		o.w.Write(hostScriptFrame(tag, data[:n]))
		data = data[n:]
	}
	if o.f != nil {
		o.f.Flush()
	}
}

func (o *frameOut) exit(code int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.w, "x %d\n", code)
	if o.f != nil {
		o.f.Flush()
	}
}

// cappedStream is one of a call's two output streams: written through
// as frames until limit bytes, then a truncation marker once.
type cappedStream struct {
	out       *frameOut
	tag       byte
	limit     int
	written   int
	truncated bool
}

func (c *cappedStream) Write(p []byte) (int, error) {
	n := len(p)
	if c.truncated {
		return n, nil
	}
	if left := c.limit - c.written; len(p) > left {
		p = p[:left]
		c.truncated = true
	}
	c.written += len(p)
	if len(p) > 0 {
		c.out.frame(c.tag, p)
	}
	if c.truncated {
		c.out.frame(c.tag, []byte(fmt.Sprintf("\n[grain: output truncated at %d bytes]\n", c.limit)))
	}
	return n, nil
}

// fail answers a call grain refuses: the reason on stderr and exit 125,
// framed so the client reports it as such, under status.
func fail(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	o := &frameOut{w: w}
	o.frame('e', []byte("grain: "+reason+"\n"))
	o.exit(125)
}

// decodeArgv reads an exec body: one hex-encoded argument per line.
func decodeArgv(body []byte, s HostScript) ([]string, string) {
	if len(body) == 0 {
		return nil, ""
	}
	text := string(body)
	if !strings.HasSuffix(text, "\n") {
		return nil, "the request body is not newline-terminated argument lines"
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > s.MaxArgs {
		return nil, fmt.Sprintf("%d arguments exceed %s's limit of %d", len(lines), s.Name, s.MaxArgs)
	}
	argv := make([]string, len(lines))
	for i, l := range lines {
		b, err := hex.DecodeString(l)
		if err != nil {
			return nil, fmt.Sprintf("argument %d is not hex-encoded", i+1)
		}
		if len(b) > s.MaxArgLen {
			return nil, fmt.Sprintf("argument %d is %d bytes, over %s's limit of %d", i+1, len(b), s.Name, s.MaxArgLen)
		}
		for _, c := range b {
			if c == 0 {
				return nil, fmt.Sprintf("argument %d holds a NUL byte", i+1)
			}
		}
		argv[i] = string(b)
	}
	return argv, ""
}

// taskOf is the task segment of an attempt_id
// <deployment>-<task>-<attempt>-<index>.
func taskOf(attemptID string) string {
	parts := strings.Split(attemptID, "-")
	if len(parts) < 4 {
		return ""
	}
	return parts[len(parts)-3]
}

// scriptEnv is the whole environment a script runs with: the ambient
// identity variables of the server's own environment, the script's env,
// and GRAIN_TASK_ID / GRAIN_ATTEMPT_ID. Nothing else of the server's
// environment -- its $secret: bindings included -- reaches it.
func scriptEnv(environ []string, s HostScript, attemptID string) []string {
	keep := map[string]bool{}
	for _, k := range AmbientIdentityEnv {
		keep[k] = true
	}
	vars := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && (keep[k] || strings.HasPrefix(k, "LC_")) {
			vars[k] = v
		}
	}
	if vars["PATH"] == "" {
		vars["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	for k, v := range s.Env {
		vars[k] = v
	}
	vars["GRAIN_ATTEMPT_ID"] = attemptID
	vars["GRAIN_TASK_ID"] = taskOf(attemptID)
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (h *hostScripts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutPrefix(r.URL.Path, "/runs/")
	if ok {
		id, ok = strings.CutSuffix(id, "/exec")
	}
	if !ok || !pathSafe(id) || r.URL.RawQuery != "" {
		fail(w, http.StatusNotFound, "no such host-script endpoint")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a := h.lookup(id)
	if a == nil || !a.admit() {
		fail(w, http.StatusNotFound, "this grant's host script is not available; the grant was released")
		return
	}
	defer a.done()
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.token)) != 1 {
		fail(w, http.StatusUnauthorized, "missing or wrong token for this grant")
		return
	}
	s := a.script
	limit := int64(s.MaxArgs)*(2*int64(s.MaxArgLen)+1) + 1
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "reading the arguments: "+err.Error())
		return
	}
	start := h.cfg.Now()
	rec := HostScriptAudit{Time: start.UTC(), AttemptID: id, Script: s.Name, Caller: "sandbox", ExitCode: 125}
	if int64(len(body)) > limit {
		rec.Refused = "the arguments exceed the script's limits"
		h.record(a, rec)
		fail(w, http.StatusRequestEntityTooLarge, rec.Refused)
		return
	}
	argv, refusal := decodeArgv(body, s)
	rec.Argv = argv
	if refusal != "" {
		rec.Refused = refusal
		h.record(a, rec)
		fail(w, http.StatusBadRequest, refusal)
		return
	}
	a.mu.Lock()
	over := a.calls >= s.MaxCalls
	if !over {
		a.calls++
	}
	a.mu.Unlock()
	if over {
		rec.Refused = fmt.Sprintf("this grant has made all %d calls %s allows", s.MaxCalls, s.Name)
		h.record(a, rec)
		fail(w, http.StatusTooManyRequests, rec.Refused)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	out := &frameOut{w: w, f: flusher}
	code, timedOut, truncated := h.run(r.Context(), a, argv, out)
	out.exit(code)
	rec.ExitCode, rec.TimedOut, rec.Truncated = code, timedOut, truncated
	rec.DurationMS = h.cfg.Now().Sub(start).Milliseconds()
	h.record(a, rec)
}

// run runs the script with argv, streaming its output to out, and
// returns its exit status: 128+n for a signal, 124 for a timeout.
func (h *hostScripts) run(reqCtx context.Context, a *hostScriptAttempt, argv []string, out *frameOut) (code int, timedOut, truncated bool) {
	s := a.script
	cwd := s.Cwd
	if cwd == "" {
		cwd = a.scratch
	}
	// bash takes the file, and every argument after it is a positional
	// parameter, a leading "--" or "-c" included.
	cmd := exec.Command("bash", append([]string{a.file}, argv...)...)
	cmd.Dir = cwd
	cmd.Env = scriptEnv(h.cfg.Environ(), s, a.id)
	cmd.Stdin = nil
	stdout := &cappedStream{out: out, tag: 'o', limit: s.MaxOutput}
	stderr := &cappedStream{out: out, tag: 'e', limit: s.MaxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A background child still holding the output open does not hold the
	// call: the group is killed when bash exits, and WaitDelay bounds the
	// copy after that.
	cmd.WaitDelay = hostScriptKillGrace
	if err := cmd.Start(); err != nil {
		out.frame('e', []byte("grain: starting bash: "+err.Error()+"\n"))
		return 126, false, false
	}
	pgid := cmd.Process.Pid
	kill := func(sig syscall.Signal) { syscall.Kill(-pgid, sig) }
	done := make(chan struct{})
	var mu sync.Mutex
	stopped := ""
	go func() {
		timer := time.NewTimer(s.Timeout)
		defer timer.Stop()
		why := ""
		select {
		case <-done:
			return
		case <-timer.C:
			why = "timeout"
		case <-reqCtx.Done():
			why = "caller"
		case <-a.ctx.Done():
			why = "released"
		}
		mu.Lock()
		stopped = why
		mu.Unlock()
		kill(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(hostScriptKillGrace):
		}
		kill(syscall.SIGKILL)
	}()
	err := cmd.Wait()
	close(done)
	kill(syscall.SIGKILL)
	mu.Lock()
	why := stopped
	mu.Unlock()
	truncated = stdout.truncated || stderr.truncated
	switch why {
	case "timeout":
		out.frame('e', []byte(fmt.Sprintf("\ngrain: %s timed out after %s; its process group was killed\n", s.Name, s.Timeout)))
		return 124, true, truncated
	case "released":
		out.frame('e', []byte(fmt.Sprintf("\ngrain: %s was stopped: the grant was released\n", s.Name)))
		return 125, false, truncated
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, false, truncated
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), false, truncated
		}
		return exitErr.ExitCode(), false, truncated
	case errors.Is(err, exec.ErrWaitDelay):
		return cmd.ProcessState.ExitCode(), false, truncated
	default:
		out.frame('e', []byte("grain: "+err.Error()+"\n"))
		return 125, false, truncated
	}
}

// What one audit record keeps of a call's argv.
const (
	auditMaxArgs     = 64
	auditMaxArgBytes = 1 << 10
)

// auditArgv is argv as an audit record keeps it, and whether it was cut.
func auditArgv(argv []string) ([]string, bool) {
	cut := len(argv) > auditMaxArgs
	if cut {
		argv = argv[:auditMaxArgs]
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		if len(a) > auditMaxArgBytes {
			a, cut = a[:auditMaxArgBytes], true
		}
		out[i] = a
	}
	return out, cut
}

// record keeps one call's audit record: on the grant, in the server's
// log, and appended to <data-dir>/audit.jsonl. Both are bounded per
// grant: calls by max_calls, and refused calls -- which max_calls does
// not count -- by a budget of the same size, past which one record says
// further refusals go unrecorded and the rest are refused silently.
func (h *hostScripts) record(a *hostScriptAttempt, rec HostScriptAudit) {
	rec.Argv, rec.ArgvTruncated = auditArgv(rec.Argv)
	a.mu.Lock()
	if rec.Refused != "" {
		a.refusals++
		switch {
		case a.refusals > a.script.MaxCalls+1:
			a.mu.Unlock()
			return
		case a.refusals == a.script.MaxCalls+1:
			rec.Argv, rec.ArgvTruncated = nil, false
			rec.Refused = fmt.Sprintf("%d calls of this grant have been refused; further refusals are not recorded", a.script.MaxCalls)
		}
	}
	a.audit = append(a.audit, rec)
	a.mu.Unlock()
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	h.cfg.Log.Printf("host-script: call %s", line)
	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	f, err := os.OpenFile(filepath.Join(h.cfg.DataDir, "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		h.cfg.Log.Printf("host-script: audit log: %v", err)
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	w.Write(append(line, '\n'))
	w.Flush()
}

// HostScriptTestTool is the control-plane tool Settings' Test button
// calls (POST /api/capability-servers/test-script): it runs one
// configured script once with an argv, outside any grant, and answers
// with a HostScriptTestResult.
const HostScriptTestTool = "test_script"

// hostScriptTestCaller is what a test_script call's audit record names
// as its caller, where a grant's call says "sandbox".
const hostScriptTestCaller = "settings-test"

// HostScriptTestTimeout bounds one test_script run whatever the script's
// own timeout: the control plane answers one call at a time, so a test
// holds up the server's mints while it runs.
const HostScriptTestTimeout = 60 * time.Second

// hostScriptTestArgs is test_script's arguments.
var hostScriptTestArgs = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"script"},
	"properties": map[string]any{
		"script": map[string]any{"type": "string"},
		"argv":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}

// HostScriptTestResult is test_script's answer. Refused says the call was
// not run, and why: an unknown script, or an argv over its limits.
type HostScriptTestResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Refused    string `json:"refused,omitempty"`
}

// testScript runs a test_script call: the named script in a throwaway
// grant of its own -- attempt_id settings-test-<hex>, which no task's
// attempt_id can be, since those have four parts -- under the same
// limits, environment and audit a grant's call has (its timeout capped
// at HostScriptTestTimeout), its record's caller settings-test; the grant
// is released when the call returns.
func (h *hostScripts) testScript(ctx context.Context, args map[string]any) ([]byte, error) {
	name, _ := args["script"].(string)
	var argv []string
	if raw, ok := args["argv"].([]any); ok {
		for i, v := range raw {
			a, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("argv[%d] is not a string", i)
			}
			argv = append(argv, a)
		}
	} else if args["argv"] != nil {
		return nil, fmt.Errorf("argv is not an array of strings")
	}
	h.mu.Lock()
	s, ok := h.scripts[name]
	h.mu.Unlock()
	if !ok {
		return json.Marshal(HostScriptTestResult{ExitCode: 125, Refused: "no script " + strconv.Quote(name) + " is configured; save the server's configuration first"})
	}
	s.Timeout = min(s.Timeout, HostScriptTestTimeout)
	suffix, err := randomToken()
	if err != nil {
		return nil, err
	}
	id := hostScriptTestCaller + "-" + suffix[:16]
	if _, err := h.mint(id, s); err != nil {
		return nil, err
	}
	// No release_ reply reads this grant's records back, so they are
	// dropped from retired here; audit.jsonl and the log keep them.
	defer func() {
		h.release(id)
		h.takeRetired(id)
	}()
	a := h.lookup(id)
	start := h.cfg.Now()
	rec := HostScriptAudit{Time: start.UTC(), AttemptID: id, Script: s.Name, Argv: argv, Caller: hostScriptTestCaller, ExitCode: 125}
	refusal := ""
	switch {
	case len(argv) > s.MaxArgs:
		refusal = fmt.Sprintf("%d arguments exceed %s's limit of %d", len(argv), s.Name, s.MaxArgs)
	default:
		for i, arg := range argv {
			if len(arg) > s.MaxArgLen {
				refusal = fmt.Sprintf("argument %d is %d bytes, over %s's limit of %d", i+1, len(arg), s.Name, s.MaxArgLen)
				break
			}
			if strings.ContainsRune(arg, 0) {
				refusal = fmt.Sprintf("argument %d holds a NUL byte", i+1)
				break
			}
		}
	}
	if refusal != "" {
		rec.Refused = refusal
		h.record(a, rec)
		return json.Marshal(HostScriptTestResult{ExitCode: 125, Refused: refusal})
	}
	out := &frameOut{collect: true}
	code, timedOut, truncated := h.run(ctx, a, argv, out)
	rec.ExitCode, rec.TimedOut, rec.Truncated = code, timedOut, truncated
	rec.DurationMS = h.cfg.Now().Sub(start).Milliseconds()
	h.record(a, rec)
	return json.Marshal(HostScriptTestResult{
		Stdout:     out.stdout.String(),
		Stderr:     out.stderr.String(),
		ExitCode:   code,
		DurationMS: rec.DurationMS,
		TimedOut:   timedOut,
		Truncated:  truncated,
	})
}

// newHostScriptMethod is the host-script method over scripts, or nil
// when none is configured: a grant's with: {script} is an enum of their
// names.
func newHostScriptMethod(h *hostScripts, scripts []HostScript) (*protocol.Method, error) {
	if len(scripts) == 0 {
		h.mu.Lock()
		h.scripts = nil
		h.mu.Unlock()
		return nil, nil
	}
	byName := map[string]HostScript{}
	names := make([]any, 0, len(scripts))
	for _, s := range scripts {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	h.mu.Lock()
	h.scripts = byName
	h.mu.Unlock()
	return protocol.NewMethod(protocol.MethodSpec{
		Name:        "host-script",
		Description: "Install one of this deployment's host scripts as a sandbox command that runs it on grain's side with the caller's arguments, until release or the grant's deadline.",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"script"},
			"properties": map[string]any{
				"script": map[string]any{"type": "string", "enum": names, "description": "The configured script to install."},
			},
		},
		Now: h.cfg.Now,
		Mint: func(_ context.Context, attemptID string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
			name, _ := args["script"].(string)
			s, ok := byName[name]
			if !ok {
				return refusedResponse("unknown script " + name), nil
			}
			return h.mint(attemptID, s)
		},
		Release: func(_ context.Context, attemptID string) error {
			h.release(attemptID)
			return nil
		},
		Audit: func(attemptID string) ([]any, int) {
			recs := h.takeRetired(attemptID)
			out := make([]any, len(recs))
			for i, rec := range recs {
				// An argv of control bytes can escape past the
				// record limit; the record without it still says a
				// call was made.
				if b, err := json.Marshal(rec); err == nil && len(b) > protocol.MaxAuditRecordBytes {
					rec.Argv, rec.ArgvTruncated = nil, true
				}
				out[i] = rec
			}
			return out, 0
		},
	})
}

// NewHostScript builds the host-script server, listening on cfg.Listen
// and configured with cfg.Scripts until configure says otherwise.
func NewHostScript(cfg HostScriptConfig) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:" + strconv.Itoa(DefaultHostScriptPort)
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = log.New(io.Discard, "", 0)
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("host-script: a data directory is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("host-script: data directory: %w", err)
	}
	h := &hostScripts{cfg: cfg, attempts: map[string]*hostScriptAttempt{}, retired: map[string]*hostScriptAttempt{}}
	var methods []*protocol.Method
	m, err := newHostScriptMethod(h, cfg.Scripts)
	if err != nil {
		return nil, err
	}
	if m != nil {
		methods = append(methods, m)
	}
	s, err := newServer("host-script", cfg.Log, methods...)
	if err != nil {
		return nil, err
	}
	h.srv = s
	ensureListen := func() error {
		if h.listening {
			return nil
		}
		addr, err := s.listen(cfg.Listen, h)
		if err != nil {
			return fmt.Errorf("host-script: %w", err)
		}
		h.port = addr.Port
		h.listening = true
		return nil
	}
	if m != nil {
		if err := ensureListen(); err != nil {
			return nil, err
		}
	}
	s.onClose(h.closeAll)
	s.hostScripts = h
	if err := s.HandleTool(HostScriptTestTool, "Run one configured script once with argv, outside any grant, and return its stdout, stderr, exit code and duration. Grain's Settings calls it; a sandbox cannot.", hostScriptTestArgs, h.testScript); err != nil {
		s.Close(context.Background())
		return nil, err
	}
	err = s.configurable(hostScriptConfigSchema, func(_ context.Context, config map[string]any) ([]*protocol.Method, error) {
		scripts, err := parseHostScripts(config, cfg.Environ())
		if err != nil {
			return nil, err
		}
		m, err := newHostScriptMethod(h, scripts)
		if err != nil || m == nil {
			return nil, err
		}
		if err := ensureListen(); err != nil {
			return nil, err
		}
		return []*protocol.Method{m}, nil
	})
	if err != nil {
		s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

// defaultHostScriptDataDir is the server's data directory when -data-dir
// is not given: the user's own cache directory, so under run: host it is
// the user's, and in the container the container's.
func defaultHostScriptDataDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "grain", "host-script")
	}
	return filepath.Join(os.TempDir(), "grain-host-script")
}

func init() {
	register(Builder{
		Name:        "host-script",
		NeedsConfig: true,
		Host:        true,
		Summary:     "operator-defined bash scripts, which a sandbox runs here as `grain host-script <name>` (mint_host-script)",
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("host-script", logOutput(logger))
			listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(DefaultHostScriptPort), "the data-plane listener's loopback address")
			dataDir := fs.String("data-dir", "", "where each grant's script file, scratch directory and audit.jsonl live (default: the user's cache directory)")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			if *dataDir == "" {
				*dataDir = defaultHostScriptDataDir()
			}
			return NewHostScript(HostScriptConfig{Listen: listenAddr(env, *listen), DataDir: *dataDir, Log: logger})
		},
	})
}
