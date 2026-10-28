package hostinit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// System is every external effect Init can have: running a command,
// fetching a URL, and knowing whether this process can make a root-only
// change outright. Kept to these three so a test can fake the whole of a
// bare host's state with a small, explicit table, the same shape
// gcpsetup's own Admin interface holds its GCP calls to.
type System interface {
	// Run executes name with args, in dir (the current directory if
	// empty), with env appended to this process's own environment (each
	// entry "KEY=value"), and returns combined stdout+stderr with
	// trailing whitespace trimmed. A non-zero exit is an error whose
	// message includes that output, which is what every caller in this
	// package inspects to tell "not installed" from "installed but
	// failed" from "that's not even the command we meant" -- so a fake
	// System's error strings matter, not only its bool/err split.
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)
	// HTTPGet fetches url and returns its body, erroring on anything
	// other than a 200.
	HTTPGet(ctx context.Context, url string) ([]byte, error)
	// HTTPRequest performs method against url, with body as the request
	// body (nil for none, which omits Content-Type too), and returns the
	// response status code and body. Only a transport failure -- a
	// refused connection, a timeout, a malformed URL -- is an error: a
	// non-2xx status is for the caller to read off the returned code,
	// the way net/http itself leaves it, because this is used for calls
	// (the loopback register and team-settings APIs) whose non-200
	// responses -- 409 Conflict, chiefly -- mean something a caller has
	// to tell apart from a transport failure outright.
	HTTPRequest(ctx context.Context, method, url string, body []byte) (status int, respBody []byte, err error)
	// IsRoot reports whether this process can make a root-only change
	// (an apt install, a systemd unit, setcap) outright, rather than
	// only describing the command an operator with more access would
	// run for it.
	IsRoot() bool
}

// RealSystem is System against the real host: os/exec and net/http.
type RealSystem struct{}

// sbinDirs are where RealSystem.Run looks for a command its PATH does
// not have (getcap, useradd, usermod), a var so a test can point it at a
// directory of its own.
var sbinDirs = []string{"/usr/local/sbin", "/usr/sbin", "/sbin"}

func (RealSystem) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	fullEnv := os.Environ()
	if len(env) > 0 {
		fullEnv = mergeEnv(fullEnv, env)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = fullEnv
	// exec.CommandContext resolves name to an absolute path via LookPath
	// against this process's own PATH, before cmd.Env is ever consulted --
	// so a name found only on a PATH this call's own env adds (e.g. a
	// toolchain install that only now has a bin dir on PATH) would
	// otherwise fail to resolve even though the child's environment would
	// have found it fine. Re-resolve against fullEnv's PATH so the lookup
	// and the child's environment agree on what's on PATH.
	//
	// A name on neither PATH is then looked for in sbinDirs: Debian keeps
	// /usr/sbin off an ordinary user's PATH, so a non-root re-run could not
	// find getcap there, and so reported a CAP_NET_BIND_SERVICE an earlier
	// root run had already granted as a manual step, forever.
	if !strings.ContainsRune(name, os.PathSeparator) {
		lp, err := lookPathIn(name, envValue(fullEnv, "PATH"))
		if err != nil {
			lp, err = lookPathIn(name, strings.Join(sbinDirs, string(os.PathListSeparator)))
		}
		if err == nil {
			cmd.Path = lp
			cmd.Err = nil
		}
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := strings.TrimSpace(out.String())
	if err != nil {
		if output != "" {
			return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, output)
		}
		return output, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}

func (RealSystem) HTTPGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	withLocalAPIToken(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (RealSystem) HTTPRequest(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	withLocalAPIToken(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

func (RealSystem) IsRoot() bool { return os.Geteuid() == 0 }

// envValue returns the value of key in env ("KEY=value" entries), or "" if
// env sets no such key.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// lookPathIn finds name on path the way exec.LookPath finds it on
// os.Getenv("PATH") -- path is an explicit PATH value to search instead,
// so a caller can resolve a name against a PATH it is about to hand a
// child process rather than against this process's own.
func lookPathIn(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", fs.ErrNotExist, name)
}

// mergeEnv layers overrides onto base, dropping any base entry whose key
// an override also sets -- a plain append would leave both the old and
// new value of, say, PATH in the slice, and which one a child process's
// libc getenv prefers is not something this package wants to depend on.
func mergeEnv(base, overrides []string) []string {
	keys := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			keys[kv[:i]] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i >= 0 && keys[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, overrides...)
}

// withLocalAPIToken adds the daemon's local API token to a request for
// a loopback address, when $GRAIN_LOCAL_API_TOKEN_FILE names one this
// process can read (cmd/grain's init sets it to -data-dir's): what lets
// the steps that provision over the API reach a daemon that wants the
// access code from every caller, loopback included. pkg/ui's localtoken.go is the other end; the
// two names are spelled out here rather than imported so this package
// keeps out of pkg/ui.
func withLocalAPIToken(req *http.Request) {
	path := strings.TrimSpace(os.Getenv("GRAIN_LOCAL_API_TOKEN_FILE"))
	if path == "" || req.Header.Get("Authorization") != "" {
		return
	}
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}
