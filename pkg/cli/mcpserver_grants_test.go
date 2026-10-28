package cli

// Which tools `grain mcpserver` serves for the grants its run holds
// (-grant). This is the whole of how a grant whose only effect is a tool
// roster reaches a running agent: a Framework forks this subcommand and
// can hand it nothing but arguments, so a grant the flag does not carry
// is a tool the prompt can name and no run can call.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// toolNames is what grantedTools would put on a run's roster.
func toolNames(tools []mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

// The bootstrap-playbooks grant needs no source checkout: the playbooks
// are embedded in this binary, so the grant's name is the only thing
// that has to reach this process.
func TestGrantedToolsServesThePlaybooksForTheBootstrapGrant(t *testing.T) {
	t.Parallel()
	names := toolNames(grantedTools(daemon.GrantNames{"bootstrap-playbooks"}, "", "", ""))
	for _, want := range []string{"list_bootstrap_playbooks", "read_bootstrap_playbook"} {
		if !slices.Contains(names, want) {
			t.Errorf("grantedTools = %v, want %s among them", names, want)
		}
	}
	if slices.Contains(names, "read_grain_log") {
		t.Errorf("grantedTools = %v, want nothing of grain-log's for a task without that grant", names)
	}
}

// A task holding both grants at once gets both sets.
func TestGrantedToolsServesEveryGrantsTools(t *testing.T) {
	t.Parallel()
	names := toolNames(grantedTools(daemon.GrantNames{"grain-log", "bootstrap-playbooks"}, "", secretsDirWithAKey(t), "1"))
	for _, want := range []string{
		"read_grain_log",
		"list_bootstrap_playbooks", "read_bootstrap_playbook",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("grantedTools = %v, want %s among them", names, want)
		}
	}
}

// An ordinary run's roster is exactly the sandbox tools and the escape
// hatches, as it was before any of this existed.
func TestGrantedToolsServesNothingWithoutAGrant(t *testing.T) {
	t.Parallel()
	if tools := grantedTools(nil, "/src", "", ""); len(tools) != 0 {
		t.Errorf("grantedTools = %v, want none for a run holding no grant", toolNames(tools))
	}
}

func TestDeploymentDiagnosticsRequireTheirOwnGrant(t *testing.T) {
	t.Parallel()
	without := toolNames(daemon.GrantedDaemonTools(daemon.GrantNames{"self-debug", "grain-log"}, "/src", secretsDirWithAKey(t), "/state", "1", nil))
	for _, name := range []string{"read_deployment_health", "read_deployment_state"} {
		if slices.Contains(without, name) {
			t.Errorf("tools without deployment-health = %v, unexpectedly contains %s", without, name)
		}
	}
	with := toolNames(daemon.GrantedDaemonTools(daemon.GrantNames{"deployment-health"}, "", secretsDirWithAKey(t), "/state", "1", nil))
	for _, name := range []string{"read_deployment_health", "read_deployment_state"} {
		if !slices.Contains(with, name) {
			t.Errorf("deployment-health tools = %v, want %s", with, name)
		}
	}
}

// A playbook really is readable through the tool the grant hands over --
// the end of the road that starts at a human attaching the label.
func TestTheBootstrapGrantsToolsReadAnEmbeddedPlaybook(t *testing.T) {
	t.Parallel()
	tools := grantedTools(daemon.GrantNames{"bootstrap-playbooks"}, "", "", "")
	var list, read mcp.Tool
	for _, tool := range tools {
		switch tool.Name {
		case "list_bootstrap_playbooks":
			list = tool
		case "read_bootstrap_playbook":
			read = tool
		}
	}
	if list.Handler == nil || read.Handler == nil {
		t.Fatalf("grantedTools = %v, want both playbook tools", toolNames(tools))
	}
	listed := list.Handler(context.Background(), nil)
	if listed.IsError || strings.TrimSpace(listed.Text) == "" {
		t.Fatalf("list_bootstrap_playbooks = %+v, want the embedded playbook names", listed)
	}
	name := strings.Split(listed.Text, "\n")[0]
	body := read.Handler(context.Background(), map[string]any{"name": name})
	if body.IsError || strings.TrimSpace(body.Text) == "" {
		t.Fatalf("read_bootstrap_playbook(%q) = %+v, want the playbook's own markdown", name, body)
	}
}

// The grain-log grant is served separately from self-debug, and a task
// holding only self-debug gets nothing of it. This is the whole of what
// "its own capability" means at the far end of the wire: a run that was
// granted "read grain's source" does not thereby get "read everything
// anything on this host has ever logged".
func TestGrantedToolsKeepsTheJournalOutOfASelfDebugRun(t *testing.T) {
	t.Parallel()
	dir := secretsDirWithAKey(t)
	names := toolNames(grantedTools(daemon.GrantNames{"self-debug"}, "/src", dir, "1"))
	if slices.Contains(names, "read_grain_log") {
		t.Errorf("grantedTools = %v, want no journal tool for a task holding only self-debug", names)
	}
	names = toolNames(grantedTools(daemon.GrantNames{"grain-log"}, "/src", dir, "1"))
	if !slices.Contains(names, "read_grain_log") {
		t.Errorf("grantedTools = %v, want read_grain_log for a task holding grain-log", names)
	}
}

// A deployment that gave this process no secrets directory still puts
// the tool on the roster -- a run's roster is a property of its grants,
// not of one deployment's configuration -- and that tool refuses every
// call, because it cannot redact what it cannot enumerate. Failing
// closed is the direction that fails safe: an unread journal costs a run
// one answer, an unredacted one cannot be recalled from a model's
// context or a sandbox's network.
func TestTheJournalToolRefusesWithNothingToRedactWith(t *testing.T) {
	t.Parallel()
	tools := grantedTools(daemon.GrantNames{"grain-log"}, "", "", "1")
	if len(tools) != 1 || tools[0].Name != "read_grain_log" {
		t.Fatalf("grantedTools = %v, want the journal tool present", toolNames(tools))
	}
	got := tools[0].Handler(context.Background(), map[string]any{"source": "daemon"})
	if !got.IsError {
		t.Fatalf("read_grain_log served a journal it could not redact:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "redact") {
		t.Errorf("the refusal does not say why it refused:\n%s", got.Text)
	}
}

// A run whose secrets it *can* read gets a working tool -- and the
// redaction list is built out of what this host actually holds, which is
// what a value in the journal is matched against.
func TestTheJournalToolRedactsThisHostsOwnSecrets(t *testing.T) {
	t.Parallel()
	dir := secretsDirWithAKey(t)
	store := secrets.New(dir)
	if err := store.Set("acme-webhook", "value", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("storing a secret: %v", err)
	}

	known, err := daemon.KnownSecretValues(dir)
	if err != nil {
		t.Fatalf("knownSecretValues: %v", err)
	}
	if len(known) != 1 || known[0].Value != "correct-horse-battery-staple" {
		t.Fatalf("knownSecretValues = %+v, want this host's one secret", known)
	}
	if !strings.Contains(known[0].Label, "acme-webhook") {
		t.Errorf("the value is labelled %q, which does not name the secret it came from", known[0].Label)
	}
}

// A secrets file this host cannot decrypt is a refusal, never an empty
// list. "This host has lost track of its own credentials" and "this host
// has none" look identical downstream, and reading the first as the
// second is how a journal full of a credential nobody could name gets
// served as a redacted one.
func TestKnownSecretValuesRefusesAnUnreadableStore(t *testing.T) {
	t.Parallel()
	dir := secretsDirWithAKey(t)
	if err := os.WriteFile(filepath.Join(dir, secrets.DefaultFileName),
		[]byte("this is not ciphertext this host can read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.KnownSecretValues(dir); err == nil {
		t.Fatal("knownSecretValues read an unreadable store as an empty one")
	}
}

// And a directory with no key at all is a refusal rather than a minting:
// this process is a short-lived fork, and creating the deployment's own
// secret key is the daemon's job (see knownSecretValues).
func TestKnownSecretValuesMintsNoKeyOfItsOwn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := daemon.KnownSecretValues(dir); err == nil {
		t.Fatal("knownSecretValues accepted a directory with no key")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("it wrote %d files into a secrets directory it was only meant to read", len(entries))
	}
}

// secretsDirWithAKey is a secrets directory as the daemon would have
// left it: a key, and no secrets stored yet.
func secretsDirWithAKey(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if store := secrets.New(dir); !store.KeyCreated() {
		t.Fatalf("no key was minted in %s", dir)
	}
	return dir
}

// The flag itself: repeated, one name per pair, in the order the
// Framework wrote them (agent.GrantArgs).
func TestTheGrantFlagCollectsEveryName(t *testing.T) {
	t.Parallel()
	var grants daemon.GrantNames
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Var(&grants, "grant", "")
	if err := fs.Parse([]string{"-grant", "self-debug", "-grant", "bootstrap-playbooks"}); err != nil {
		t.Fatal(err)
	}
	if want := (daemon.GrantNames{"self-debug", "bootstrap-playbooks"}); !slices.Equal(grants, want) {
		t.Fatalf("-grant collected %v, want %v", grants, want)
	}
	if !grants.Has("bootstrap-playbooks") || grants.Has("self-repair") {
		t.Errorf("grants.has misread %v", grants)
	}
}
