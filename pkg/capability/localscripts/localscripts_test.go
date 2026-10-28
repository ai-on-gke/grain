package localscripts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/localexec"
	"github.com/ai-on-gke/grain/pkg/model"
)

// stateRepo writes a declaration named name into a new state repository
// checkout and returns the checkout.
func stateRepo(t *testing.T, name, decl string, scripts map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, StateRepoDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DeclarationFile), []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	for file, content := range scripts {
		if err := os.WriteFile(filepath.Join(dir, "scripts", file), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type secrets map[string]string

func (s secrets) Resolve(_ context.Context, name string) (string, error) {
	if v, ok := s[name]; ok {
		return v, nil
	}
	return "", errors.New("no such secret")
}

func resolve(t *testing.T, reg *model.CapabilityRegistry, cc model.CapabilityContext) model.Resolution {
	t.Helper()
	out, err := model.ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("resolutions %+v: want one", out)
	}
	return out[0].Resolution
}

func TestCapabilityGrantedToATaskAPersonAskedFor(t *testing.T) {
	root := stateRepo(t, "ops", "scripts: {}\n", map[string]string{"hello": "#!/bin/sh\necho hi\n"})
	reg := model.NewCapabilityRegistry()
	reg.RegisterDynamic(DynamicProvider(Config{StateDir: root}))
	task := model.Task{
		Origin: model.Origin{Reason: model.ReasonDirect},
		Grants: []model.Grant{{Capability: "local-scripts:ops", Via: model.GrantByDefault}},
	}
	if res := resolve(t, reg, model.CapabilityContext{Task: task}); res.Refused {
		t.Fatalf("resolution %+v: want a directly filed task granted local-scripts:ops", res)
	}
	if got := Granted(task); len(got) != 1 || got[0] != "ops" {
		t.Fatalf("Granted = %v, want [ops]", got)
	}
}

func TestCapabilityRefusedToUntrustedRuns(t *testing.T) {
	root := stateRepo(t, "ops", "scripts: {}\n", map[string]string{"hello": "#!/bin/sh\necho hi\n"})
	reg := model.NewCapabilityRegistry()
	reg.RegisterDynamic(DynamicProvider(Config{StateDir: root}))
	grants := []model.Grant{{Capability: "local-scripts:ops", Via: model.GrantByDefault}}
	for _, tc := range []struct {
		name string
		cc   model.CapabilityContext
		want string
	}{
		{"scheduled run", model.CapabilityContext{Task: model.Task{
			Origin: model.Origin{Reason: model.ReasonSchedule}, Grants: grants}}, "scheduled run"},
		{"CI job", model.CapabilityContext{CI: true, Task: model.Task{Grants: grants}}, "CI job"},
		{"missing declaration", model.CapabilityContext{Task: model.Task{
			Grants: []model.Grant{{Capability: "local-scripts:nope", Via: model.GrantByDefault}}}}, "no declaration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resolve(t, reg, tc.cc)
			if !res.Refused || !strings.Contains(res.Reason, tc.want) {
				t.Fatalf("resolution %+v: want refused, naming %q", res, tc.want)
			}
		})
	}
	p, _ := DynamicProvider(Config{StateDir: root})("local-scripts:ops")
	for _, event := range []string{"push", "pull_request", "schedule", "release", "workflow_dispatch"} {
		if p.(*Provider).SupportsCIEvent(event) {
			t.Errorf("SupportsCIEvent(%q) = true: no CI event may hold local scripts", event)
		}
	}
}

func TestNameRefusesPaths(t *testing.T) {
	for _, id := range []string{"local-scripts:", "local-scripts:../x", "local-scripts:a/b", "local-scripts:/etc", "local-scripts:Ops", "repo-write:a/b"} {
		if _, ok := Name(id); ok {
			t.Errorf("Name(%q) accepted", id)
		}
	}
	if _, err := (Config{StateDir: t.TempDir()}).Path("../x"); err == nil {
		t.Error("Path(../x) accepted")
	}
}

func TestPathPrefersHostManaged(t *testing.T) {
	root := stateRepo(t, "ops", "scripts: {}\n", nil)
	host := filepath.Join(t.TempDir(), DeclarationFile)
	if err := os.WriteFile(host, []byte("scripts: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	cfg.StateDir = root
	if err := cfg.ParseHost("ops=" + host); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ParseHost("ops=relative/path"); err == nil {
		t.Error("ParseHost accepted a relative path")
	}
	if got, err := cfg.Path("ops"); err != nil || got != host {
		t.Fatalf("Path = %q, %v; want the host-managed %q", got, err, host)
	}
}

func TestCredentialsFileRoundTripAndServing(t *testing.T) {
	root := stateRepo(t, "ops", `scripts:
  list:
    - path: scripts/show
      env: {TOKEN: ops-token}
`, map[string]string{"show": "#!/bin/sh\nprintf '%s:%s' \"$TOKEN\" \"$1\"\n"})
	decl, err := (Config{StateDir: root}).Path("ops")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := ResolveCredentials(context.Background(), secrets{"ops-token": "s3cret"}, []string{decl})
	if err != nil {
		t.Fatal(err)
	}
	path, err := WriteCredentialsFile(t.TempDir(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode %v, want 0600", info.Mode().Perm())
	}
	back, err := ReadCredentialsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o644)
	if _, err := ReadCredentialsFile(path); err == nil {
		t.Error("ReadCredentialsFile accepted a world-readable file")
	}
	execs, err := Open([]string{decl}, back, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, e := range execs {
			e.Close()
		}
	}()
	tools := execs[0].Tools()
	if len(tools) != 1 || tools[0].Name != "local_show" {
		t.Fatalf("tools %+v: want local_show", tools)
	}
	res := tools[0].Handler(context.Background(), map[string]any{"args": []any{"x"}})
	var out localexec.Result
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil || out.Stdout != "s3cret:x" {
		t.Fatalf("result %q (%v): want the secret delivered to the script", res.Text, err)
	}
	if _, err := Open([]string{decl}, Credentials{}, nil); err == nil {
		t.Error("Open served a script whose secret was not delivered")
	}
}
