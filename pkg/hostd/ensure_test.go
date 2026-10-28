//go:build unix

package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testBuiltins = []BuiltinEntry{
	{Name: "host-git", Args: []string{"capability", "host-git"}},
	{Name: "grain-workspace", Args: []string{"capability", "workspace"}, ExtraArgs: true},
	{Name: "grain-gcp", Args: []string{"capability", "gcp"}},
}

const cmd = "/usr/local/bin/grain"

func plan(t *testing.T, in string) (string, EnsureResult) {
	t.Helper()
	out, res, err := PlanEnsure([]byte(in), cmd, testBuiltins)
	if err != nil {
		t.Fatalf("PlanEnsure: %v", err)
	}
	if _, err := ParseAllowlist(out); err != nil {
		t.Fatalf("the result does not parse: %v\n%s", err, out)
	}
	return string(out), res
}

func TestPlanEnsureAddsOnlyWhatIsMissing(t *testing.T) {
	in := `# the operator's header
clis: {}
# A stdio capability server a capability config starts with run: host
# names one of these in its command:
capabilities:
#  corp-search:
#    command: /usr/local/bin/corp-search-mcp
  host-git:
    command: /usr/local/bin/grain
    args: [capability, host-git]
    pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]
  grain-gcp:
    command: /opt/my/grain   # the operator's own
    args: [capability, gcp]
`
	out, res := plan(t, in)
	if strings.Join(res.Added, ",") != "grain-workspace" {
		t.Fatalf("added %v, want just grain-workspace", res.Added)
	}
	if !strings.HasPrefix(out, in) {
		t.Errorf("the file's own text moved:\n%s", out)
	}
	if want := testBuiltins[1].YAML(cmd, "  ", "  "); !strings.HasSuffix(out, want) {
		t.Errorf("grain-workspace not appended as the installer writes it:\n%s", out)
	}
	if len(res.Differs) != 1 || !strings.Contains(res.Differs[0], "grain-gcp") || !strings.Contains(res.Differs[0], "/opt/my/grain") {
		t.Errorf("differs = %q, want grain-gcp's command reported", res.Differs)
	}
	a, _ := ParseAllowlist([]byte(out))
	if a.Capabilities["grain-gcp"].Command != "/opt/my/grain" {
		t.Error("the operator's grain-gcp entry was changed")
	}
	// Run again: nothing more to add, and the file is left byte for byte.
	again, res2 := plan(t, out)
	if again != out || len(res2.Added) != 0 {
		t.Errorf("a second run changed the file or added %v", res2.Added)
	}
}

func TestPlanEnsureMatchesOddIndentationAndKeepsLaterKeys(t *testing.T) {
	in := `capabilities:
    mine:
        command: /usr/bin/mine
        args: [--stdio]

# how long a drain waits
drain_cap: 2h
clis:
    agy:
        path: /usr/bin/agy
`
	out, res := plan(t, in)
	if len(res.Added) != 3 {
		t.Fatalf("added %v, want all three", res.Added)
	}
	if !strings.Contains(out, "\n    grain-workspace:\n        command: /usr/local/bin/grain\n") {
		t.Errorf("not written at the mapping's own indentation:\n%s", out)
	}
	if !strings.Contains(out, "        pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]\n\n# how long a drain waits\ndrain_cap: 2h\n") {
		t.Errorf("not inserted before the next key and its comment:\n%s", out)
	}
	a, _ := ParseAllowlist([]byte(out))
	if a.DrainCap.Hours() != 2 || a.CLIs["agy"] == nil || a.Capabilities["mine"] == nil {
		t.Errorf("the rest of the file was lost: %+v", a)
	}
}

func TestPlanEnsureEmptyForms(t *testing.T) {
	for name, in := range map[string]string{
		"no file":        "",
		"no section":     "clis: {}\n",
		"no newline":     "clis: {}",
		"null":           "capabilities:\nclis: {}\n",
		"flow empty":     "capabilities: {}\nclis: {}\n",
		"tilde":          "capabilities: ~ # none yet\n",
		"comments only":  "# nothing here\n",
		"null then more": "clis: {}\ncapabilities:\n#  example:\n#    command: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, res := plan(t, in)
			if len(res.Added) != 3 || res.Manual != "" {
				t.Errorf("added %v manual %q:\n%s", res.Added, res.Manual, out)
			}
		})
	}
}

func TestPlanEnsureLeavesAFlowMappingToTheOperator(t *testing.T) {
	in := "capabilities: {mine: {command: /usr/bin/mine}}\n"
	out, res := plan(t, in)
	if out != in || len(res.Added) != 0 {
		t.Fatalf("a flow mapping was edited:\n%s", out)
	}
	if !strings.Contains(res.Manual, "  grain-workspace:\n    command: /usr/local/bin/grain\n") {
		t.Errorf("manual = %q, want the entries to copy", res.Manual)
	}
}

func TestPlanEnsureRefusesAnAllowlistHostdRefuses(t *testing.T) {
	if _, _, err := PlanEnsure([]byte("capabilities:\n  x:\n    nonsense: 1\n"), cmd, testBuiltins); err == nil {
		t.Error("an invalid allowlist was edited")
	}
}

func TestEnsureBuiltinsWritesAtomicallyKeepingTheMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(path, []byte("clis: {}\ncapabilities:\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	res, err := EnsureBuiltins(path, cmd, testBuiltins)
	if err != nil || len(res.Added) != 3 {
		t.Fatalf("EnsureBuiltins = %+v, %v", res, err)
	}
	after, _ := os.Lstat(path)
	if after.Mode() != before.Mode() {
		t.Errorf("mode %v, want %v kept", after.Mode(), before.Mode())
	}
	if os.SameFile(before, after) {
		t.Error("written in place, not replaced by a rename")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left %d files behind in the directory", len(entries))
	}
	if _, err := LoadAllowlist(path, []uint32{uint32(os.Getuid())}); err != nil {
		t.Errorf("hostd refuses the result: %v", err)
	}
}

func TestEnsureBuiltinsNeverFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(real, []byte("capabilities:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "hostd.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBuiltins(link, cmd, testBuiltins); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
	if data, _ := os.ReadFile(real); string(data) != "capabilities:\n" {
		t.Errorf("the symlink's target was written: %q", data)
	}
}
