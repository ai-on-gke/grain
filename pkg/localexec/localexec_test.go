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

package localexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// folder writes a declaration and files into a new folder, each file
// with mode 0755 when its content starts with #!, 0644 otherwise, and
// loads the declaration.
func folder(t *testing.T, decl string, files map[string]string) *Declaration {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local-scripts.yaml"), []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(content, "#!") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Load(filepath.Join(dir, "local-scripts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func snapshot(t *testing.T, d *Declaration) []Snapshotted {
	t.Helper()
	s, err := d.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshotWhichScripts(t *testing.T) {
	d := folder(t, `scripts:
  list:
    - path: tools/deploy
      description: Deploy a preview.
      approve: true
      timeout: 1m
      env: {DEPLOY_TOKEN: deploy-token}
`, map[string]string{
		"tools/deploy":        "#!/bin/sh\n# grain-args: <branch>\necho deploy\n",
		"scripts/Logs.Tail":   "#!/bin/sh\n# grain: Tail the logs\n# grain:   of a service.\n# grain-args: <service> [-n lines]\necho logs\n",
		"scripts/silo-style":  "#!/bin/sh\n# silo: Written for Silo.\n# silo-args: <x>\n",
		"scripts/plain":       "#!/bin/sh\necho plain\n",
		"scripts/.hidden":     "#!/bin/sh\necho hidden\n",
		"scripts/notexec":     "echo not executable\n",
		"scripts/sub/nested":  "#!/bin/sh\necho nested\n",
		"scripts/late":        "#!/bin/sh\n" + strings.Repeat("true\n", 25) + "# grain: too late\n",
		"scripts/sub/.hidden": "#!/bin/sh\n",
	})
	scripts := snapshot(t, d)
	var names []string
	byName := map[string]Snapshotted{}
	for _, s := range scripts {
		names = append(names, s.Name)
		byName[s.Name] = s
	}
	if got := strings.Join(names, " "); got != "Logs.Tail late plain silo-style deploy" {
		t.Fatalf("scripts %q: want executables directly in scripts/, without dotfiles or subdirectories, then listed ones", got)
	}
	dep := byName["deploy"]
	if dep.Tool != "local_deploy" || !dep.Approve || dep.Timeout != time.Minute || dep.Env["DEPLOY_TOKEN"] != "deploy-token" {
		t.Errorf("deploy: %+v", dep)
	}
	if dep.Description != "Deploy a preview.\nUsage: deploy <branch>\nEach call waits for a person to approve it." {
		t.Errorf("deploy description %q", dep.Description)
	}
	l := byName["Logs.Tail"]
	if l.Tool != "local_logs_tail" || l.Description != "Tail the logs of a service.\nUsage: Logs.Tail <service> [-n lines]" {
		t.Errorf("Logs.Tail: tool %s description %q", l.Tool, l.Description)
	}
	if s := byName["silo-style"]; s.Description != "Written for Silo.\nUsage: silo-style <x>" {
		t.Errorf("silo-style description %q", s.Description)
	}
	if p := byName["plain"]; p.Description != "Run plain on the grain node host." || p.Timeout != DefaultTimeout || p.Approve {
		t.Errorf("plain: %+v", p)
	}
	if p := byName["late"]; p.Description != "Run late on the grain node host." {
		t.Errorf("a # grain: line past line 20 was read: %q", p.Description)
	}
	if p := byName["plain"]; p.Size != int64(len("#!/bin/sh\necho plain\n")) || p.Mode.Perm() != 0o755 || len(p.SHA256) != 64 {
		t.Errorf("plain snapshot: %+v", p)
	}
	if got := d.Secrets(); len(got) != 1 || got[0] != "deploy-token" {
		t.Errorf("secrets %q", got)
	}
}

func TestLoadRefusals(t *testing.T) {
	for _, tc := range []struct{ decl, want string }{
		{"scripts: {dirs: x}\n", "dirs"},
		{"timeout: soon\n", "positive duration"},
		{"scripts: {list: [{description: x}]}\n", "no path"},
		{"scripts: {list: [{path: a, env: {\"BAD NAME\": s}}]}\n", "environment variable name"},
		{"output_cap: -1\n", "negative"},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "local-scripts.yaml")
		os.WriteFile(p, []byte(tc.decl), 0o644)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err %v, want %q", tc.decl, err, tc.want)
		}
	}
}

func TestSnapshotRefusals(t *testing.T) {
	files := func() map[string]string { return map[string]string{"scripts/a": "#!/bin/sh\necho a\n"} }

	t.Run("symlink out of the folder", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "evil")
		os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755)
		d := folder(t, "", files())
		if err := os.Symlink(outside, filepath.Join(d.Folder, "scripts", "evil")); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Snapshot(); err == nil || !strings.Contains(err.Error(), "outside the declaration's folder") {
			t.Errorf("snapshot: %v", err)
		}
	})
	t.Run("symlink inside the folder", func(t *testing.T) {
		d := folder(t, "", files())
		os.MkdirAll(filepath.Join(d.Folder, "lib"), 0o755)
		os.WriteFile(filepath.Join(d.Folder, "lib", "real"), []byte("#!/bin/sh\n"), 0o755)
		if err := os.Symlink("../lib/real", filepath.Join(d.Folder, "scripts", "linked")); err != nil {
			t.Fatal(err)
		}
		if s := snapshot(t, d); len(s) != 2 {
			t.Errorf("scripts %+v", s)
		}
	})
	t.Run("group-writable parent", func(t *testing.T) {
		d := folder(t, "", files())
		os.Chmod(filepath.Join(d.Folder, "scripts"), 0o775)
		_, err := d.Snapshot()
		if err == nil || !strings.Contains(err.Error(), "its directory") || !strings.Contains(err.Error(), "writable by group or others") {
			t.Errorf("snapshot: %v", err)
		}
	})
	t.Run("world-writable folder", func(t *testing.T) {
		d := folder(t, "", files())
		os.Chmod(d.Folder, 0o757)
		if _, err := d.Snapshot(); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
			t.Errorf("snapshot: %v", err)
		}
	})
	t.Run("group-writable file", func(t *testing.T) {
		d := folder(t, "", files())
		os.Chmod(filepath.Join(d.Folder, "scripts", "a"), 0o775)
		_, err := d.Snapshot()
		if err == nil || !strings.Contains(err.Error(), "script a: it is writable") {
			t.Errorf("snapshot: %v", err)
		}
		// Check reports the same, as an error.
		if errs, _ := d.Check(); len(errs) != 1 {
			t.Errorf("check: %v", errs)
		}
	})
	t.Run("foreign owner", func(t *testing.T) {
		d := folder(t, "", files())
		t.Cleanup(func(old func() int) func() { return func() { getuid = old } }(getuid))
		getuid = func() int { return os.Getuid() + 1 }
		if _, err := d.Snapshot(); err == nil || !strings.Contains(err.Error(), "not grain's own user") {
			t.Errorf("snapshot: %v", err)
		}
	})
	t.Run("missing listed dir", func(t *testing.T) {
		d := folder(t, "scripts: {dir: nowhere}\n", nil)
		if _, err := d.Snapshot(); err == nil || !strings.Contains(err.Error(), "scripts.dir") {
			t.Errorf("snapshot: %v", err)
		}
	})
	t.Run("same tool twice", func(t *testing.T) {
		d := folder(t, "scripts: {list: [{path: tools/a}]}\n", map[string]string{
			"scripts/a": "#!/bin/sh\n", "tools/a": "#!/bin/sh\n",
		})
		if _, err := d.Snapshot(); err == nil || !strings.Contains(err.Error(), "both be served as local_a") {
			t.Errorf("snapshot: %v", err)
		}
	})
}

// call runs a tool through its handler as grain's MCP server would.
func call(t *testing.T, ex *Executor, tool string, args map[string]any) (Result, string, bool) {
	t.Helper()
	for _, tl := range ex.Tools() {
		if tl.Name != tool {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res := tl.Handler(ctx, args)
		var r Result
		if strings.HasPrefix(res.Text, "{") {
			text := res.Text
			if i := strings.LastIndex(text, "\n}"); i >= 0 {
				text = text[:i+2]
			}
			if jerr := json.Unmarshal([]byte(text), &r); jerr != nil {
				t.Fatalf("result %q: %v", res.Text, jerr)
			}
		}
		return r, res.Text, res.IsError
	}
	t.Fatalf("no tool %s", tool)
	return Result{}, "", false
}

func executor(t *testing.T, d *Declaration, cfg Config) *Executor {
	t.Helper()
	cfg.Scripts = snapshot(t, d)
	if cfg.Workdir == "" {
		cfg.Workdir = d.WorkdirPath()
	}
	ex := New(cfg)
	t.Cleanup(func() { ex.Close() })
	return ex
}

func TestChangedScriptIsRefused(t *testing.T) {
	d := folder(t, "", map[string]string{"scripts/a": "#!/bin/sh\necho one\n"})
	ex := executor(t, d, Config{})
	if r, text, isErr := call(t, ex, "local_a", map[string]any{"args": []any{}}); isErr || r.Stdout != "one\n" {
		t.Fatalf("first call: %s", text)
	}
	os.WriteFile(filepath.Join(d.Folder, "scripts", "a"), []byte("#!/bin/sh\necho two\n"), 0o755)
	_, text, isErr := call(t, ex, "local_a", map[string]any{"args": []any{}})
	if !isErr || !strings.Contains(text, "changed since this run started (content)") {
		t.Fatalf("changed script: %s", text)
	}
}

func TestArgumentsPassExactly(t *testing.T) {
	d := folder(t, "", map[string]string{
		"scripts/args": "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done\ncat\n",
	})
	ex := executor(t, d, Config{})
	args := []string{"a b", `"double"`, `'single'`, "$(touch pwned)", "`touch pwned2`", "*", "", "x;touch pwned3", "$HOME", "line\nbreak", "\\"}
	in := make([]any, len(args))
	want := ""
	for i, a := range args {
		in[i] = a
		want += "[" + a + "]\n"
	}
	r, text, isErr := call(t, ex, "local_args", map[string]any{"args": in, "stdin": "from stdin $(x)"})
	if isErr {
		t.Fatal(text)
	}
	if r.Stdout != want+"from stdin $(x)" || r.ExitCode != 0 {
		t.Fatalf("stdout:\n%s\nwant:\n%s", r.Stdout, want)
	}
	for _, p := range []string{"pwned", "pwned2", "pwned3"} {
		if _, err := os.Stat(filepath.Join(d.Folder, p)); err == nil {
			t.Errorf("an argument was interpreted: %s exists", p)
		}
	}

	if _, text, isErr := call(t, ex, "local_args", map[string]any{"args": []any{"ok", "nul\x00byte"}}); !isErr || !strings.Contains(text, "NUL") {
		t.Errorf("NUL: %s", text)
	}
	for _, bad := range []map[string]any{{}, {"args": "a b"}, {"args": []any{1}}, {"args": []any{}, "cwd": "/"}, {"args": []any{}, "stdin": 3}} {
		if _, text, isErr := call(t, ex, "local_args", bad); !isErr {
			t.Errorf("%v: %s, want a refusal", bad, text)
		}
	}
}

func TestNonZeroExitIsAResult(t *testing.T) {
	d := folder(t, "", map[string]string{"scripts/fail": "#!/bin/sh\necho oops >&2\nexit 3\n"})
	ex := executor(t, d, Config{})
	r, text, isErr := call(t, ex, "local_fail", map[string]any{"args": []any{}})
	if isErr || r.ExitCode != 3 || r.Stderr != "oops\n" || r.Truncated || r.Duration == "" {
		t.Fatalf("%s", text)
	}
	if !strings.HasPrefix(text, "{\n  \"exit_code\": 3,") {
		t.Errorf("exit_code is not first: %s", text)
	}
}

func TestWorkdirAndEnvironment(t *testing.T) {
	d := folder(t, `workdir: wd
scripts:
  list:
    - {path: scripts/a, env: {DEPLOY_TOKEN: tok, OTHER: other}}
`, map[string]string{
		"scripts/a": "#!/bin/sh\npwd\nenv\n",
		"scripts/b": "#!/bin/sh\nenv\n",
		"wd/.keep":  "",
	})
	environ := []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8",
		"GRAIN_SECRET=x", "ANTHROPIC_API_KEY=provider-key",
		"HTTPS_PROXY=http://user:pass@proxy",
	}
	scripts := snapshot(t, d)
	env := map[string]map[string]string{}
	for _, s := range scripts {
		for name, secret := range s.Env {
			if env[s.Name] == nil {
				env[s.Name] = map[string]string{}
			}
			env[s.Name][name] = secret + "-secret-value"
		}
	}
	ex := executor(t, d, Config{Environ: BaseEnviron(environ), Env: env})
	ra, text, isErr := call(t, ex, "local_a", map[string]any{"args": []any{}})
	if isErr {
		t.Fatal(text)
	}
	rb, text, isErr := call(t, ex, "local_b", map[string]any{"args": []any{}})
	if isErr {
		t.Fatal(text)
	}
	if wd, _ := filepath.EvalSymlinks(filepath.Join(d.Folder, "wd")); !strings.HasPrefix(ra.Stdout, wd+"\n") {
		t.Errorf("workdir: %s, want %s", ra.Stdout, wd)
	}
	for _, r := range []Result{ra, rb} {
		for _, gone := range []string{"GRAIN_SECRET", "ANTHROPIC_API_KEY", "HTTPS_PROXY"} {
			if strings.Contains(r.Stdout, gone) {
				t.Errorf("%s reached a script:\n%s", gone, r.Stdout)
			}
		}
		if !strings.Contains(r.Stdout, "LANG=C.UTF-8") {
			t.Errorf("the base environment is missing:\n%s", r.Stdout)
		}
	}
	if !strings.Contains(ra.Stdout, "DEPLOY_TOKEN=tok-secret-value") || !strings.Contains(ra.Stdout, "OTHER=other-secret-value") {
		t.Errorf("a's delivered credentials are missing:\n%s", ra.Stdout)
	}
	if strings.Contains(rb.Stdout, "secret") {
		t.Errorf("b got a credential delivered only to a:\n%s", rb.Stdout)
	}
}

func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	// A zombie is dead; it is only waiting for init to reap it.
	fields := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	d := folder(t, "timeout: 1s\n", map[string]string{
		// A grandchild that ignores SIGTERM, so only the SIGKILL after
		// the grace period ends it.
		"scripts/hang": "#!/bin/sh\nsh -c 'trap \"\" TERM; echo $$ > grandchild; while :; do sleep 1; done' &\necho started\nwhile :; do sleep 1; done\n",
	})
	ex := executor(t, d, Config{KillGrace: 500 * time.Millisecond})
	start := time.Now()
	r, text, isErr := call(t, ex, "local_hang", map[string]any{"args": []any{}})
	if !isErr || !strings.Contains(text, "timed out after 1s") || !r.TimedOut || r.Stdout != "started\n" {
		t.Fatalf("result %+v: %s", r, text)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s", d)
	}
	b, err := os.ReadFile(filepath.Join(d.Folder, "grandchild"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCloseEndsRunningScripts(t *testing.T) {
	d := folder(t, "", map[string]string{"scripts/hang": "#!/bin/sh\nwhile :; do sleep 1; done\n"})
	ex := executor(t, d, Config{KillGrace: 200 * time.Millisecond})
	textc := make(chan string, 1)
	go func() {
		_, text, _ := call(t, ex, "local_hang", map[string]any{"args": []any{}})
		textc <- text
	}()
	time.Sleep(300 * time.Millisecond)
	ex.Close()
	select {
	case text := <-textc:
		if !strings.Contains(text, "run ended") {
			t.Errorf("result %s", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close left the script running")
	}
}

func TestOutputCap(t *testing.T) {
	d := folder(t, "output_cap: 100\n", map[string]string{"scripts/loud": "#!/bin/sh\nhead -c 10000 /dev/zero | tr '\\0' x\necho small >&2\n"})
	ex := executor(t, d, Config{OutputCap: d.Cap()})
	r, text, isErr := call(t, ex, "local_loud", map[string]any{"args": []any{}})
	if isErr {
		t.Fatal(text)
	}
	if !r.Truncated || r.StdoutBytes != 10000 || r.StderrBytes != 6 || r.Stderr != "small\n" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Stdout, "9900 bytes of output dropped") || strings.Count(r.Stdout, "x") != 100 {
		t.Errorf("stdout %q", r.Stdout)
	}
}

func TestUnknownTool(t *testing.T) {
	ex := New(Config{})
	t.Cleanup(func() { ex.Close() })
	if res := ex.Call(context.Background(), "local_nope", map[string]any{"args": []any{}}); !res.IsError {
		t.Fatalf("%+v", res)
	}
}

func TestWarnings(t *testing.T) {
	cases := map[string]string{
		"eval":      "#!/bin/sh\neval \"$@\"\n",
		"shc":       "#!/bin/sh\nset -e\nbash -c \"$1\"\n",
		"shc2":      "#!/bin/sh\nexec /bin/sh -ec \"${2}\"\n",
		"unquoted":  "#!/bin/sh\nls $@\n",
		"unquoted2": "#!/bin/sh\necho ${*}\n",
		"xargs":     "#!/bin/sh\nprintf '%s\\n' \"$@\" | xargs rm\n",
		"wrapper":   "#!/bin/sh\nexec python3 \"$@\"\n",
	}
	clean := map[string]string{
		"quoted":   "#!/bin/sh\n# eval is mentioned only in a comment; so is xargs\nls -- \"$@\"\necho 'eval $@'\ngit log \"$1\"\n",
		"evaluate": "#!/bin/sh\nevaluate \"$1\"\nsh -c 'echo fixed'\n",
	}
	files := map[string]string{}
	for k, v := range cases {
		files["scripts/"+k] = v
	}
	for k, v := range clean {
		files["scripts/"+k] = v
	}
	d := folder(t, "", files)
	// A copy of a shell, which is itself an interpreter.
	sh, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Skip(err)
	}
	os.WriteFile(filepath.Join(d.Folder, "scripts", "dash"), sh, 0o755)
	cases["dash"] = ""

	for name := range cases {
		w := Warnings(filepath.Join(d.Folder, "scripts", name))
		if len(w) == 0 {
			t.Errorf("%s: no warning", name)
		}
	}
	for name := range clean {
		if w := Warnings(filepath.Join(d.Folder, "scripts", name)); len(w) != 0 {
			t.Errorf("%s: %v", name, w)
		}
	}
	errs, warnings := d.Check()
	if len(errs) != 0 || len(warnings) < len(cases) {
		t.Errorf("check: errors %v, warnings %v", errs, warnings)
	}
}

func TestToolName(t *testing.T) {
	for in, want := range map[string]string{"deploy": "local_deploy", "Logs.Tail": "local_logs_tail", "a-b c": "local_a_b_c"} {
		if got := ToolName(in); got != want {
			t.Errorf("ToolName(%q) = %q, want %q", in, got, want)
		}
	}
}
