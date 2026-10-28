package intree

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
)

// hostScriptEnviron is the server's own environment in these tests.
var hostScriptEnviron = []string{
	"HOME=/home/someone", "USER=someone", "PATH=/usr/local/bin:/usr/bin:/bin",
	"DEPLOY_TOKEN=tok-from-secret", "GRAIN_ROOT_SECRET=never-to-a-script",
}

type hostScriptFixture struct {
	t    *testing.T
	s    *Server
	ps   *protocol.Server
	data string
}

// newHostScriptFixture configures a host-script server with scripts, as
// Settings would, and attaches to it as Grain does.
func newHostScriptFixture(t *testing.T, scripts ...map[string]any) *hostScriptFixture {
	t.Helper()
	data := t.TempDir()
	s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: data, Environ: func() []string { return hostScriptEnviron }})
	if err != nil {
		t.Fatal(err)
	}
	list := make([]any, len(scripts))
	for i, sc := range scripts {
		list[i] = sc
	}
	ps, err := attachConfigured(t, s, map[string]any{"scripts": list}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &hostScriptFixture{t: t, s: s, ps: ps, data: data}
}

func script(name, body string, extra ...any) map[string]any {
	m := map[string]any{"name": name, "description": "the " + name + " script", "script": body, "timeout": 30}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

// hostScriptCmd is a granted script as a sandbox calls it, `grain
// host-script <name>`, with the grant's placements written to dir, which
// stands in for hostscriptcli.Dir. It runs the real client -- this test
// binary as grain (TestMain) -- or, with fallback, the stand-in grain
// the grant places for images that carry none.
type hostScriptCmd struct {
	dir, name string
	fallback  bool
}

func (c hostScriptCmd) withFallback() hostScriptCmd {
	c.fallback = true
	return c
}

func (c hostScriptCmd) command(args ...string) *exec.Cmd {
	argv := append([]string{"host-script", c.name}, args...)
	var cmd *exec.Cmd
	if c.fallback {
		cmd = exec.Command("sh", append([]string{filepath.Join(c.dir, "grain")}, argv...)...)
	} else {
		exe, err := os.Executable()
		if err != nil {
			panic(err)
		}
		cmd = exec.Command(exe, argv...)
		cmd.Env = append(os.Environ(), grainHelperEnv+"=1")
	}
	cmd.Env = append(cmd.Environ(), hostscriptcli.DirEnv+"="+c.dir)
	return cmd
}

// grant mints with: {script: name} as attempt and writes the placements
// it returns to a temporary directory standing in for the sandbox's
// hostscriptcli.Dir.
func (f *hostScriptFixture) grant(attempt, name string) (hostScriptCmd, *protocol.MintResponse) {
	f.t.Helper()
	resp := mint(f.t, f.ps, "host-script", attempt, map[string]any{"script": name})
	if resp.Status != protocol.StatusOK {
		f.t.Fatalf("mint = %+v", resp)
	}
	sandbox := f.t.TempDir()
	for path, mode := range map[string]string{
		hostscriptcli.Dir + "/" + name + ".json": "0600",
		hostScriptFallback:                       "0755",
		hostScriptSetup:                          "0700",
	} {
		p := placement(f.t, resp, path)
		if p.Mode != mode {
			f.t.Errorf("%s mode = %s, want %s", path, p.Mode, mode)
		}
		if err := os.WriteFile(filepath.Join(sandbox, filepath.Base(path)), []byte(p.Content), 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	for _, p := range resp.Placements {
		if strings.HasPrefix(p.Path, HostScriptBinDir+"/") {
			f.t.Errorf("placement %s: a grant installs no command of its own", p.Path)
		}
	}
	if resp.Setup != hostScriptSetup {
		f.t.Errorf("setup = %q, want %q", resp.Setup, hostScriptSetup)
	}
	return hostScriptCmd{dir: sandbox, name: name}, resp
}

type callResult struct {
	stdout, stderr string
	code           int
}

// call runs a command the way a sandbox process would: no shell string,
// argv as given.
func call(t *testing.T, cmd hostScriptCmd, args ...string) callResult {
	t.Helper()
	c := cmd.command(args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return callResult{out.String(), errb.String(), code}
}

// bothClients is cmd as the real grain and as the fallback, by name.
func bothClients(cmd hostScriptCmd) map[string]hostScriptCmd {
	return map[string]hostScriptCmd{"grain": cmd, "fallback": cmd.withFallback()}
}

func (f *hostScriptFixture) url(attempt string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/runs/%s/exec", f.s.hostScripts.port, attempt)
}

// post calls the endpoint directly, with argv encoded as the client does.
func (f *hostScriptFixture) post(attempt, token string, argv ...string) (int, string) {
	f.t.Helper()
	var body strings.Builder
	for _, a := range argv {
		body.WriteString(hex.EncodeToString([]byte(a)) + "\n")
	}
	req, _ := http.NewRequest(http.MethodPost, f.url(attempt), strings.NewReader(body.String()))
	req.Header.Set("Content-Type", hostscriptcli.ArgvType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (f *hostScriptFixture) token(resp *protocol.MintResponse, attempt string) string {
	var d hostscriptcli.Descriptor
	for _, p := range resp.Placements {
		if strings.HasPrefix(p.Path, hostscriptcli.Dir+"/") && strings.HasSuffix(p.Path, ".json") {
			if err := json.Unmarshal([]byte(p.Content), &d); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	if d.AttemptID != attempt || d.Token == "" {
		f.t.Fatalf("descriptor %+v, want attempt %s's token", d, attempt)
	}
	return d.Token
}

func TestHostScriptBinDirIsTheSandboxBinDir(t *testing.T) {
	if HostScriptBinDir != protocol.SandboxBinDir {
		t.Fatalf("HostScriptBinDir %q, protocol.SandboxBinDir %q: the commands would be placed where grain refuses them or off PATH", HostScriptBinDir, protocol.SandboxBinDir)
	}
}

func TestHostScriptConfigValidation(t *testing.T) {
	ok := script("deploy", "echo hi")
	for name, tc := range map[string]struct {
		scripts []any
		want    string
	}{
		"bad name":         {[]any{script("Deploy!", "x")}, "does not match"},
		"leading dash":     {[]any{script("-x", "x")}, "does not match"},
		"twice":            {[]any{ok, ok}, "configured twice"},
		"no timeout":       {[]any{map[string]any{"name": "a", "description": "d", "script": "x"}}, "timeout"},
		"zero timeout":     {[]any{script("a", "x", "timeout", 0)}, "timeout"},
		"blank script":     {[]any{script("a", "  \n")}, "script is empty"},
		"relative cwd":     {[]any{script("a", "x", "cwd", "work")}, "cwd"},
		"bad env name":     {[]any{script("a", "x", "env", map[string]any{"A-B": "1"})}, "not a variable name"},
		"unbound env ref":  {[]any{script("a", "x", "env", map[string]any{"T": "$env:NOT_SET"})}, "$env:NOT_SET"},
		"zero max_calls":   {[]any{script("a", "x", "max_calls", 0)}, "max_calls"},
		"fractional limit": {[]any{script("a", "x", "max_output", 1.5)}, "max_output"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseHostScripts(map[string]any{"scripts": tc.scripts}, hostScriptEnviron)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	got, err := parseHostScripts(map[string]any{"scripts": []any{script("a", "x", "env", map[string]any{"T": "$env:DEPLOY_TOKEN", "F": "fixed"})}}, hostScriptEnviron)
	if err != nil {
		t.Fatal(err)
	}
	s := got[0]
	if s.Env["T"] != "tok-from-secret" || s.Env["F"] != "fixed" {
		t.Errorf("env = %v: $env: is the server's own variable, anything else is literal", s.Env)
	}
	if s.MaxOutput != DefaultHostScriptMaxOutput || s.MaxCalls != DefaultHostScriptMaxCalls || s.MaxArgs != DefaultHostScriptMaxArgs || s.MaxArgLen != DefaultHostScriptMaxArgLen || s.Timeout != 30*time.Second {
		t.Errorf("limits = %+v, want the defaults and 30s", s)
	}
}

// The configure schema itself refuses what it can before the server sees
// it, as Settings would show.
func TestHostScriptConfigSchemaRefusesAnUnknownField(t *testing.T) {
	s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = attachConfigured(t, s, map[string]any{"scripts": []any{script("a", "x", "shell", "zsh")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("attach = %v, want the unknown field refused", err)
	}
}

func TestHostScriptGrantIsAnEnumOfConfiguredScripts(t *testing.T) {
	f := newHostScriptFixture(t, script("deploy", "true"), script("rollback", "true"))
	schema := f.ps.Methods["host-script"].ArgsSchema
	for _, name := range []string{"deploy", "rollback"} {
		if _, err := protocol.ValidateArgs(schema, map[string]any{"script": name}); err != nil {
			t.Errorf("script %s refused: %v", name, err)
		}
	}
	for _, args := range []map[string]any{{"script": "other"}, {}, {"script": "deploy", "body": "rm -rf /"}} {
		if _, err := protocol.ValidateArgs(schema, args); err == nil {
			t.Errorf("args %v accepted: a grant may only name a configured script", args)
		}
	}
	// No scripts, no method: nothing to grant.
	g := newHostScriptFixture(t)
	if _, ok := g.ps.Methods["host-script"]; ok {
		t.Errorf("a server with no scripts advertises host-script")
	}
}

func TestHostScriptArgvPassesThroughExactly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	f := newHostScriptFixture(t, script("echoargs", `printf '%s\0' "$@"`))
	cmd, _ := f.grant("e1-42-1-0", "echoargs")
	args := []string{
		"plain", "two words", `"double"`, `'single'`, "$(touch " + marker + ")", "`touch " + marker + "`",
		"; touch " + marker, "| touch " + marker, "line1\nline2", "--", "-c", "--help", "", "*", "%s%n\\", "$HOME", "tab\there", "ünïcödé",
	}
	want := strings.Join(args, "\x00") + "\x00"
	for client, cmd := range bothClients(cmd) {
		got := call(t, cmd, args...)
		if got.code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", client, got.code, got.stderr)
		}
		if got.stdout != want {
			t.Errorf("%s: script saw\n%q\nwant\n%q", client, got.stdout, want)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("%s: an argument was executed", client)
		}
	}
}

func TestHostScriptExitCodeStreamsAndHelp(t *testing.T) {
	f := newHostScriptFixture(t,
		script("status", "echo out; echo err >&2; exit 7", "usage", "status [--fail]\nExits 7."),
		script("slow", "echo first; sleep 2; echo second"))
	cmd, resp := f.grant("e1-42-1-0", "status")
	for client, cmd := range bothClients(cmd) {
		got := call(t, cmd, "x")
		if got.code != 7 || got.stdout != "out\n" || got.stderr != "err\n" {
			t.Errorf("%s: got %+v, want exit 7, out on stdout and err on stderr", client, got)
		}
		for _, help := range []callResult{call(t, cmd, "--grain-help"), call(t, hostScriptCmd{dir: cmd.dir, name: "--help", fallback: cmd.fallback}, "status")} {
			if help.code != 0 || !strings.Contains(help.stdout, "status [--fail]\nExits 7.") || !strings.Contains(help.stdout, "the status script") ||
				!strings.Contains(help.stdout, "grain host-script status [arguments...]") {
				t.Errorf("%s: help = %+v", client, help)
			}
		}
		list := call(t, hostScriptCmd{dir: cmd.dir, name: "--list", fallback: cmd.fallback})
		if list.code != 0 || list.stdout != "status\tthe status script\n" {
			t.Errorf("%s: --list = %+v", client, list)
		}
		missing := call(t, hostScriptCmd{dir: cmd.dir, name: "nope", fallback: cmd.fallback})
		if missing.code != 125 || !strings.Contains(missing.stderr, "no host script \"nope\" is granted") {
			t.Errorf("%s: an ungranted script = %+v, want exit 125 saying so", client, missing)
		}
	}
	if n := len(f.s.hostScripts.Audit("e1-42-1-0")); n != 2 {
		t.Errorf("%d audit records, want 2: help and --list are answered locally", n)
	}
	for _, want := range []string{"Host script `status` is granted", "the status script", "`grain host-script status [arguments...]`",
		"`grain host-script status --grain-help`", "status [--fail]", "never through a shell", "never pass a secret"} {
		if !strings.Contains(resp.PromptSection, want) {
			t.Errorf("prompt section %q lacks %q", resp.PromptSection, want)
		}
	}
	if strings.Contains(resp.PromptSection, HostScriptBinDir) {
		t.Errorf("prompt section %q names a per-script command path", resp.PromptSection)
	}

	slow, _ := f.grant("e1-42-1-1", "slow")
	for client, slow := range bothClients(slow) {
		c := slow.command()
		pipe, _ := c.StdoutPipe()
		start := time.Now()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		line, _ := bufio.NewReader(pipe).ReadString('\n')
		if line != "first\n" || time.Since(start) > 1500*time.Millisecond {
			t.Errorf("%s: first line %q after %s: output is not streamed", client, line, time.Since(start))
		}
		io.Copy(io.Discard, pipe)
		c.Wait()
	}
}

func TestHostScriptRefusesBadArgv(t *testing.T) {
	f := newHostScriptFixture(t, script("lim", "true", "max_args", 2, "max_arg_len", 4))
	_, resp := f.grant("e1-42-1-0", "lim")
	tok := f.token(resp, "e1-42-1-0")
	for name, argv := range map[string][]string{
		"too many": {"a", "b", "c"},
		"too long": {"12345"},
		"NUL":      {"a\x00b"},
	} {
		code, body := f.post("e1-42-1-0", tok, argv...)
		if code != http.StatusBadRequest || !strings.HasSuffix(body, "x 125\n") {
			t.Errorf("%s: %d %q, want a framed refusal", name, code, body)
		}
	}
	if code, body := f.post("e1-42-1-0", tok, "ok", "1234"); code != 200 || body != "x 0\n" {
		t.Errorf("within limits: %d %q", code, body)
	}
	recs := f.s.hostScripts.Audit("e1-42-1-0")
	if len(recs) != 4 || recs[0].Refused == "" || recs[3].Refused != "" {
		t.Errorf("audit = %+v, want three refusals and one call", recs)
	}
}

func TestHostScriptTokenAndRelease(t *testing.T) {
	f := newHostScriptFixture(t, script("a", "echo ran"))
	cmd, resp := f.grant("e1-42-1-0", "a")
	_, other := f.grant("e1-42-1-1", "a")
	for name, tok := range map[string]string{"missing": "", "wrong": "nope", "another grant's": f.token(other, "e1-42-1-1")} {
		if code, body := f.post("e1-42-1-0", tok); code != http.StatusUnauthorized || strings.Contains(body, "o ran") {
			t.Errorf("%s token: %d %q, want 401 and no run", name, code, body)
		}
	}
	for client, cmd := range bothClients(cmd) {
		if got := call(t, cmd); got.code != 0 || got.stdout != "ran\n" {
			t.Fatalf("%s: before release: %+v", client, got)
		}
	}
	dir := filepath.Join(f.data, "attempts", "e1-42-1-0")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ps.Release(context.Background(), "host-script", "e1-42-1-0"); err != nil {
		t.Fatal(err)
	}
	for client, cmd := range bothClients(cmd) {
		got := call(t, cmd)
		if got.code != 125 || !strings.Contains(got.stderr, "released") || got.stdout != "" {
			t.Errorf("%s: after release: %+v, want exit 125 saying the grant was released", client, got)
		}
	}
	if code, _ := f.post("e1-42-1-0", f.token(resp, "e1-42-1-0")); code != http.StatusNotFound {
		t.Errorf("after release the endpoint answered %d, want 404", code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the grant's private directory outlived its release: %v", err)
	}
}

func TestHostScriptTimeoutKillsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	f := newHostScriptFixture(t, script("hang", `sleep 300 & echo $! > `+pidFile+`; sleep 300`, "timeout", 1))
	cmd, _ := f.grant("e1-42-1-0", "hang")
	start := time.Now()
	got := call(t, cmd)
	if got.code != 124 || !strings.Contains(got.stderr, "timed out") {
		t.Errorf("got %+v, want exit 124 saying it timed out", got)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("background child %d survived the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	recs := f.s.hostScripts.Audit("e1-42-1-0")
	if len(recs) != 1 || !recs[0].TimedOut || recs[0].ExitCode != 124 {
		t.Errorf("audit = %+v", recs)
	}
}

func TestHostScriptOutputIsCapped(t *testing.T) {
	f := newHostScriptFixture(t, script("loud", `printf 0123456789abcdef; printf 0123456789abcdef >&2`, "max_output", 10))
	cmd, _ := f.grant("e1-42-1-0", "loud")
	got := call(t, cmd)
	marker := "\n[grain: output truncated at 10 bytes]\n"
	if got.stdout != "0123456789"+marker || got.stderr != "0123456789"+marker {
		t.Errorf("got %+v", got)
	}
	if recs := f.s.hostScripts.Audit("e1-42-1-0"); len(recs) != 1 || !recs[0].Truncated {
		t.Errorf("audit = %+v", recs)
	}
}

func TestHostScriptCallLimit(t *testing.T) {
	f := newHostScriptFixture(t, script("twice", "echo ok", "max_calls", 2))
	cmd, _ := f.grant("e1-42-1-0", "twice")
	for i := 0; i < 2; i++ {
		if got := call(t, cmd); got.code != 0 {
			t.Fatalf("call %d: %+v", i+1, got)
		}
	}
	got := call(t, cmd)
	if got.code != 125 || !strings.Contains(got.stderr, "all 2 calls") {
		t.Errorf("third call: %+v", got)
	}
	// The limit is the grant's, not the script's.
	other, _ := f.grant("e1-42-1-1", "twice")
	if got := call(t, other); got.code != 0 {
		t.Errorf("another grant's first call: %+v", got)
	}
}

func TestHostScriptEnvironmentIsScrubbed(t *testing.T) {
	f := newHostScriptFixture(t, script("env", "env", "env", map[string]any{"FIXED": "1", "TOKEN": "$env:DEPLOY_TOKEN"}))
	cmd, _ := f.grant("e1-42-3-0", "env")
	got := call(t, cmd)
	vars := map[string]string{}
	for _, l := range strings.Split(got.stdout, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			vars[k] = v
		}
	}
	for k, want := range map[string]string{"FIXED": "1", "TOKEN": "tok-from-secret", "HOME": "/home/someone", "USER": "someone", "GRAIN_ATTEMPT_ID": "e1-42-3-0", "GRAIN_TASK_ID": "42"} {
		if vars[k] != want {
			t.Errorf("%s = %q, want %q", k, vars[k], want)
		}
	}
	for _, k := range []string{"DEPLOY_TOKEN", "GRAIN_ROOT_SECRET"} {
		if _, ok := vars[k]; ok {
			t.Errorf("%s reached the script: only ambient identity, its env and GRAIN_* do", k)
		}
	}
}

func TestHostScriptRunsInItsScratchDirectoryAndAudits(t *testing.T) {
	f := newHostScriptFixture(t, script("where", "pwd; ls -ld . | cut -c1-10"))
	cmd, _ := f.grant("e1-42-1-0", "where")
	got := call(t, cmd, "--secret-looking-arg")
	want := filepath.Join(f.data, "attempts", "e1-42-1-0", "scratch")
	if !strings.HasPrefix(got.stdout, want+"\n") {
		t.Errorf("cwd %q, want %s", got.stdout, want)
	}
	b, err := os.ReadFile(filepath.Join(f.data, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec HostScriptAudit
	if err := json.Unmarshal(bytes.TrimSpace(b), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Script != "where" || rec.AttemptID != "e1-42-1-0" || rec.Caller != "sandbox" || len(rec.Argv) != 1 || rec.Argv[0] != "--secret-looking-arg" || rec.ExitCode != 0 {
		t.Errorf("audit record %+v", rec)
	}
	// The script file is private to the server's user.
	info, err := os.Stat(filepath.Join(f.data, "attempts", "e1-42-1-0", "where.sh"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("script file %v %v, want mode 0700", info, err)
	}
}

func TestHostScriptFrameIsLiteralForPrintf(t *testing.T) {
	data := []byte("-%s\\\x00\n\"'$`ok")
	frame := hostScriptFrame('o', data)
	fmtArg := strings.TrimSuffix(strings.TrimPrefix(string(frame), "o "), "\n")
	out, err := exec.Command("sh", "-c", `printf "$1"`, "sh", fmtArg).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("printf of the frame = %q, want %q", out, data)
	}
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	for _, d := range [][]byte{data, all} {
		frame := strings.TrimSuffix(strings.TrimPrefix(string(hostScriptFrame('o', d)), "o "), "\n")
		if got := hostscriptcli.DecodeFrame(frame); !bytes.Equal(got, d) {
			t.Errorf("grain host-script decodes the frame of %q as %q", d, got)
		}
	}
}

// A sandbox cannot grow a grant's audit records without bound: refused
// calls, which max_calls does not count, are recorded up to the same
// number and then once more to say the rest are not, and a recorded argv
// is cut to auditMaxArgs arguments of auditMaxArgBytes each.
func TestHostScriptAuditIsBounded(t *testing.T) {
	f := newHostScriptFixture(t, script("lim", "true", "max_calls", 3, "max_args", 100, "max_arg_len", 4000))
	_, resp := f.grant("e1-42-1-0", "lim")
	tok := f.token(resp, "e1-42-1-0")
	for i := 0; i < 50; i++ {
		if code, _ := f.post("e1-42-1-0", tok, "a\x00b"); code != http.StatusBadRequest {
			t.Fatalf("refusal %d answered %d", i, code)
		}
	}
	recs := f.s.hostScripts.Audit("e1-42-1-0")
	if len(recs) != 4 || !strings.Contains(recs[3].Refused, "not recorded") {
		t.Fatalf("%d records after 50 refusals, want 3 and a marker: %+v", len(recs), recs)
	}
	// Calls still run, and are recorded, after refusals stop being.
	big := make([]string, 100)
	for i := range big {
		big[i] = strings.Repeat("x", 4000)
	}
	if code, body := f.post("e1-42-1-0", tok, big...); code != 200 || body != "x 0\n" {
		t.Fatalf("call: %d %q", code, body)
	}
	recs = f.s.hostScripts.Audit("e1-42-1-0")
	last := recs[len(recs)-1]
	if len(recs) != 5 || !last.ArgvTruncated || len(last.Argv) != auditMaxArgs || len(last.Argv[0]) != auditMaxArgBytes {
		t.Errorf("call record: %d records, truncated %v, %d args of %d bytes", len(recs), last.ArgvTruncated, len(last.Argv), len(last.Argv[0]))
	}
	b, err := os.ReadFile(filepath.Join(f.data, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 5 {
		t.Errorf("audit.jsonl has %d lines, want 5", n)
	}
}

// testScript calls test_script over the control plane, as Settings' Test
// button does through the daemon.
func (f *hostScriptFixture) testScript(name string, argv ...string) HostScriptTestResult {
	f.t.Helper()
	list := make([]any, len(argv))
	for i, a := range argv {
		list[i] = a
	}
	raw, err := f.ps.CallTool(context.Background(), HostScriptTestTool, map[string]any{"script": name, "argv": list}, time.Minute)
	if err != nil {
		f.t.Fatal(err)
	}
	var res HostScriptTestResult
	if err := json.Unmarshal(raw, &res); err != nil {
		f.t.Fatalf("test_script answered %s: %v", raw, err)
	}
	return res
}

func TestHostScriptTestScriptRunsOnceOutsideAnyGrant(t *testing.T) {
	f := newHostScriptFixture(t, script("echoargs", `for a in "$@"; do printf '<%s>\n' "$a"; done; echo oops >&2; exit 3`))
	if !f.ps.HasControlTool(HostScriptTestTool) {
		t.Fatalf("control tools = %v, want %s", f.ps.ControlTools, HostScriptTestTool)
	}
	if _, ok := f.ps.Methods["host-script"]; !ok {
		t.Fatalf("methods = %v: test_script must not hide the host-script method", f.ps.Methods)
	}
	res := f.testScript("echoargs", "two words", "$(touch /tmp/pwned)", "")
	if res.Refused != "" || res.ExitCode != 3 {
		t.Fatalf("result = %+v", res)
	}
	if want := "<two words>\n<$(touch /tmp/pwned)>\n<>\n"; res.Stdout != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
	if res.Stderr != "oops\n" {
		t.Errorf("stderr = %q", res.Stderr)
	}
	// The throwaway grant is gone, and its call audited as Settings'.
	if n, r := len(f.s.hostScripts.attempts), len(f.s.hostScripts.retired); n != 0 || r != 0 {
		t.Errorf("%d grants and %d retired grants left behind", n, r)
	}
	recs := readAudit(t, f.data)
	if len(recs) != 1 {
		t.Fatalf("audit = %+v", recs)
	}
	rec := recs[0]
	if rec.Caller != "settings-test" || !strings.HasPrefix(rec.AttemptID, "settings-test-") || rec.ExitCode != 3 || len(rec.Argv) != 3 || rec.Argv[1] != "$(touch /tmp/pwned)" {
		t.Errorf("audit record = %+v", rec)
	}
	if taskOf(rec.AttemptID) != "" {
		t.Errorf("a test's attempt_id %s reads as task %q", rec.AttemptID, taskOf(rec.AttemptID))
	}
}

func TestHostScriptTestScriptRefusals(t *testing.T) {
	f := newHostScriptFixture(t, script("few", "echo hi", "max_args", 1))
	if res := f.testScript("missing"); res.Refused == "" || res.ExitCode != 125 {
		t.Errorf("unknown script = %+v", res)
	}
	if res := f.testScript("few", "a", "b"); !strings.Contains(res.Refused, "exceed") {
		t.Errorf("too many arguments = %+v", res)
	}
	if res := f.testScript("few", "a\x00b"); !strings.Contains(res.Refused, "NUL") {
		t.Errorf("NUL argument = %+v", res)
	}
	if res := f.testScript("few"); res.Refused != "" || res.Stdout != "hi\n" || res.ExitCode != 0 {
		t.Errorf("no arguments = %+v", res)
	}
}

// readAudit is every record in data's audit.jsonl.
func readAudit(t *testing.T, data string) []HostScriptAudit {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []HostScriptAudit
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var rec HostScriptAudit
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

// release_host-script carries the grant's call records back to Grain
// (§4.4.4) -- argv verbatim, refusals with their reason -- including a
// call the release itself cut short, and only this grant's.
func TestHostScriptReleaseReturnsItsAudit(t *testing.T) {
	f := newHostScriptFixture(t, script("a", `[ "$1" = slow ] && exec sleep 30; echo "$@"`))
	cmd, resp := f.grant("e1-42-1-0", "a")
	otherCmd, _ := f.grant("e1-42-1-1", "a")
	if got := call(t, cmd, "--flag", "two words"); got.code != 0 {
		t.Fatalf("call: %+v", got)
	}
	if got := call(t, otherCmd, "other grant"); got.code != 0 {
		t.Fatalf("call: %+v", got)
	}
	if code, _ := f.post("e1-42-1-0", f.token(resp, "e1-42-1-0"), "a\x00b"); code != http.StatusBadRequest {
		t.Fatalf("refused call answered %d", code)
	}
	slow := make(chan callResult)
	go func() { slow <- call(t, cmd, "slow") }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a := f.s.hostScripts.lookup("e1-42-1-0")
		a.mu.Lock()
		n := a.inflight
		a.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slow call never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	r, err := f.ps.Release(context.Background(), "host-script", "e1-42-1-0")
	if err != nil {
		t.Fatal(err)
	}
	<-slow
	if r.AuditRefused != "" || r.AuditOmitted != 0 || len(r.Audit) != 3 {
		t.Fatalf("release audit: %d records, omitted %d, refused %q", len(r.Audit), r.AuditOmitted, r.AuditRefused)
	}
	var recs [3]HostScriptAudit
	for i, raw := range r.Audit {
		if err := json.Unmarshal(raw, &recs[i]); err != nil {
			t.Fatal(err)
		}
		if recs[i].AttemptID != "e1-42-1-0" || recs[i].Script != "a" || recs[i].Caller != "sandbox" {
			t.Errorf("record %d = %+v", i, recs[i])
		}
	}
	if recs[0].ExitCode != 0 || strings.Join(recs[0].Argv, "|") != "--flag|two words" {
		t.Errorf("first call = %+v", recs[0])
	}
	if recs[1].Refused == "" {
		t.Errorf("refused call = %+v, want its reason", recs[1])
	}
	if recs[2].ExitCode == 0 || len(recs[2].Argv) != 1 || recs[2].Argv[0] != "slow" {
		t.Errorf("the call the release cut short = %+v", recs[2])
	}
	// Repeated, the release answers with the same records; the other
	// grant's are its own.
	again, err := f.ps.Release(context.Background(), "host-script", "e1-42-1-0")
	if err != nil || len(again.Audit) != 3 {
		t.Errorf("repeated release: %v, %d records", err, len(again.Audit))
	}
	other, err := f.ps.Release(context.Background(), "host-script", "e1-42-1-1")
	if err != nil || len(other.Audit) != 1 || !strings.Contains(string(other.Audit[0]), "other grant") {
		t.Errorf("other grant's release: %v, %s", err, other.Audit)
	}
}

func TestHostScriptBindsDataPlaneOnlyOnceAScriptIsConfigured(t *testing.T) {
	s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	if s.hostScripts.listening || s.hostScripts.port != 0 {
		t.Fatalf("unconfigured host-script bound data plane at start: listening=%v port=%d", s.hostScripts.listening, s.hostScripts.port)
	}
	if _, err := attachConfigured(t, s, map[string]any{"scripts": []any{}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.hostScripts.listening || s.hostScripts.port != 0 {
		t.Fatalf("empty scripts bound data plane: listening=%v port=%d", s.hostScripts.listening, s.hostScripts.port)
	}
	if _, err := attachConfigured(t, s, map[string]any{"scripts": []any{script("echo-hi", "echo hi")}}, nil); err != nil {
		t.Fatal(err)
	}
	if !s.hostScripts.listening || s.hostScripts.port == 0 {
		t.Fatalf("configured host-script did not bind data plane: listening=%v port=%d", s.hostScripts.listening, s.hostScripts.port)
	}
}
