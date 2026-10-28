package gitproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func writeCredentialSet(t *testing.T, patterns map[string]string, tokens map[string]string) string {
	t.Helper()
	// Nearly every test here starts with a credential set -- newTestProxy
	// and newAppProxy both do -- so this is where they claim the log,
	// first of all so that the release it registers runs last of all.
	// See log_test.go.
	logToTest(t)
	dir := t.TempDir()
	if patterns != nil {
		data, err := json.Marshal(patterns)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, token := range tokens {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSelectAsksTheConnectionsAndNeverCredentialsJSON: which credential
// covers a repository is the git connections' answer; a credentials.json
// left in the directory is not read.
func TestSelectAsksTheConnectionsAndNeverCredentialsJSON(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ladder"},
		map[string]string{"ladder": "ladder-token", "conn": "conn-token"})
	cs, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cred, ok := cs.Select("acme", "widgets"); ok {
		t.Fatalf("Select with no connections = %+v, want nothing: credentials.json is not a ladder any more", cred)
	}
	var asked string
	cs.SetConnections(func(owner, repo string) (string, bool) {
		asked = owner + "/" + repo
		return "conn", true
	})
	cred, ok := cs.Select("ACME", "Widgets.git")
	if !ok || cred.Name != "conn" || cred.Token == nil || *cred.Token != "conn-token" {
		t.Fatalf("Select = %+v, %v; want the connection's credential", cred, ok)
	}
	if asked != "acme/widgets" {
		t.Errorf("connections asked about %q, want the canonical acme/widgets", asked)
	}
	cs.SetConnections(func(string, string) (string, bool) { return "missing", true })
	if cred, ok := cs.Select("acme", "widgets"); ok {
		t.Errorf("Select of a connection naming no stored credential = %+v, want nothing", cred)
	}
}

func TestGetReturnsANamedCredentialDirectly(t *testing.T) {
	dir := writeCredentialSet(t, nil, map[string]string{"workflow": "wf-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := set.Get("workflow")
	if !ok || cred.Name != "workflow" || cred.Token == nil || *cred.Token != "wf-token" {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

func TestGetFailsClosedForAnUnconfiguredName(t *testing.T) {
	set, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Get("nonexistent"); ok {
		t.Error("expected Get to fail closed for a missing token file")
	}
}

func TestNamesListsEveryConfiguredCredentialOnce(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "bot"},
		map[string]string{"bot": "bot-token", "release-bot": "release-token"})
	// A credential backed by both file shapes is still one credential --
	// load prefers the App one, so counting it twice would offer a
	// duplicate capability for a single token.
	if err := os.WriteFile(filepath.Join(dir, "release-bot.app.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := set.Names()
	want := []string{"bot", "release-bot"}
	if !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v -- credentials.json itself is not a credential", got, want)
	}
}

func TestExtraNamesIsEveryStoredCredential(t *testing.T) {
	dir := writeCredentialSet(t, nil,
		map[string]string{"bot": "bot-token", "acme-bot": "acme-token", "release-bot": "release-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Which connection names a credential is a different question from
	// which tokens a task may ask for by name.
	got, want := set.ExtraNames(), []string{"acme-bot", "bot", "release-bot"}
	if !slices.Equal(got, want) {
		t.Errorf("ExtraNames() = %v, want %v", got, want)
	}
}

func TestExtraNamesIsEmptyWithNothingConfigured(t *testing.T) {
	set, err := LoadCredentialSet(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.ExtraNames(); len(got) != 0 {
		t.Errorf("ExtraNames() = %v, want none -- an unreadable secrets directory configures no credentials", got)
	}
}

func TestGetAnonymousNeedsNoTokenFile(t *testing.T) {
	set, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := set.Get("anonymous")
	if !ok || cred.Token != nil {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

// load's cache is the one piece of mutable state a CredentialSet carries,
// and Select/Get are called from GitProxy.Handle -- one goroutine per
// inbound HTTP request in a real deployment, so two sandboxes doing git
// operations at the same moment reach it concurrently. Run with -race:
// before mu guarded the cache, this reliably reported a concurrent map
// read/write.
func TestSelectAndGetAreSafeForConcurrentCallers(t *testing.T) {
	dir := writeCredentialSet(t,
		map[string]string{"acme/widgets": "narrow", "acme/*": "wide", "*": "global"},
		map[string]string{"narrow": "narrow-token", "wide": "wide-token", "global": "global-token", "named": "named-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			switch i % 4 {
			case 0:
				if _, ok := useTestPatterns(set).Select("acme", "widgets"); !ok {
					t.Error("expected acme/widgets to resolve")
				}
			case 1:
				if _, ok := useTestPatterns(set).Select("acme", "other"); !ok {
					t.Error("expected acme/other to resolve")
				}
			case 2:
				if _, ok := useTestPatterns(set).Select("someone-else", "repo"); !ok {
					t.Error("expected someone-else/repo to resolve")
				}
			case 3:
				if _, ok := set.Get("named"); !ok {
					t.Error("expected named to resolve")
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

// An override name comes from a task grant or team config, not from the
// secrets directory, so one that climbs out of it must not be read.
func TestGetRefusesANameOutsideTheSecretsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "github")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.token"), []byte("stolen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "global.token"), []byte("global-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logToTest(t)
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "sub/../../outside", `..\outside`, ".hidden", ""} {
		if cred, ok := set.Get(name); ok {
			t.Errorf("Get(%q) = %+v, want no such credential", name, cred)
		}
		if cred, ok := set.GetForRepo(name, "acme", "widgets"); ok {
			t.Errorf("GetForRepo(%q) = %+v, want no such credential", name, cred)
		}
		if set.IsApp(name) {
			t.Errorf("IsApp(%q) = true", name)
		}
		if cred := set.load(name, ""); cred.Token != nil {
			t.Errorf("load(%q) read token %q", name, *cred.Token)
		}
	}
	if cred, ok := set.Get("global"); !ok || cred.Token == nil || *cred.Token != "global-token" {
		t.Fatalf("Get(global) = %+v, %v", cred, ok)
	}
}
