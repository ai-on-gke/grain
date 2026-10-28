package gitproxy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// grain/task-137: the ladder is written from the UI now, not only by an
// operator dropping a file on the host -- which means this package owns
// both ends of the file convention, and these are the write end.

func TestSetTokenAddsACredentialTheLadderThenSees(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "bot"}, map[string]string{"bot": "bot-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("release-bot", "  ghp-release\n"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	// Everything that answers "which tokens exist" sees it, without this
	// CredentialSet being reloaded: Names/ExtraNames read the directory
	// per call, which is exactly why a token added here is a token the
	// next process start offers as a capability.
	if got := set.Names(); !slices.Equal(got, []string{"bot", "release-bot"}) {
		t.Errorf("Names() = %v, want bot and release-bot", got)
	}
	if got := set.ExtraNames(); !slices.Equal(got, []string{"bot", "release-bot"}) {
		t.Errorf("ExtraNames() = %v, want bot and release-bot", got)
	}
	cred, ok := set.Get("release-bot")
	if !ok || cred.Token == nil || *cred.Token != "ghp-release" {
		t.Fatalf("Get(release-bot) = %+v, %v, want the trimmed token back", cred, ok)
	}

	// 0600, like every other credential file under /data/secrets: the
	// mode setup.sh gives the ones it seeds itself.
	info, err := os.Stat(filepath.Join(dir, "release-bot.token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("release-bot.token mode = %v, want 0600", info.Mode().Perm())
	}
}

// A replaced token is the case the cache would otherwise get wrong: this
// CredentialSet has already served the old value, and the file under it
// just changed.
func TestSetTokenReplacesAValueThisSetHasAlreadyRead(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "bot"}, map[string]string{"bot": "old-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cred, _ := useTestPatterns(set).Select("acme", "widgets"); cred.Token == nil || *cred.Token != "old-token" {
		t.Fatalf("first read = %+v, want the old token", cred)
	}
	if err := set.SetToken("bot", "new-token"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	cred, ok := useTestPatterns(set).Select("acme", "widgets")
	if !ok || cred.Token == nil || *cred.Token != "new-token" {
		t.Fatalf("after SetToken = %+v, %v, want the new token -- forget should have dropped the cached one", cred, ok)
	}
}

// Nothing in the temporary file SetToken writes through may look like a
// credential to Names, even if this process dies between create and
// rename: a half-written token that reads as a configured credential is
// a capability the picker would offer and every push through it would
// fail on.
func TestSetTokenLeavesNoCredentialShapedTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("bot", "ghp-token"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "bot.token" {
			t.Errorf("left %q behind, want only bot.token", e.Name())
		}
	}
}

func TestSetTokenCreatesTheSecretsDirectoryItNeeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets", "github")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("bot", "ghp-token"); err != nil {
		t.Fatalf("SetToken into a directory that does not exist yet: %v", err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"bot"}) {
		t.Fatalf("Names() = %v, want bot", got)
	}
}

func TestSetTokenRefusesAnAppBackedCredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ci-app.app.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	// load prefers the App credential, so a ci-app.token written here
	// would be read by nothing at all -- refused rather than silently
	// stored.
	if err := set.SetToken("ci-app", "ghp-token"); err == nil {
		t.Fatal("expected writing a token over a GitHub App credential to be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "ci-app.token")); !os.IsNotExist(err) {
		t.Errorf("ci-app.token exists after a refused write (stat err %v)", err)
	}
	if !set.IsApp("ci-app") {
		t.Error("IsApp(ci-app) = false, want true")
	}
	if set.IsApp("bot") {
		t.Error("IsApp(bot) = true with no bot.app.json")
	}
}

func TestSetTokenRejectsUnusableNamesAndEmptyValues(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../escape", "with/slash", "anonymous", "with space", "dotted.name"} {
		if err := set.SetToken(name, "ghp-token"); err == nil {
			t.Errorf("SetToken(%q) was accepted, want it refused", name)
		}
	}
	if err := set.SetToken("bot", "   \n"); err == nil {
		t.Error("SetToken with a whitespace-only value was accepted, want it refused")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused write left %d files behind", len(entries))
	}
}

func TestRemoveDeletesBothFileShapesOfOneCredential(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "bot"},
		map[string]string{"bot": "bot-token", "release-bot": "release-token"})
	if err := os.WriteFile(filepath.Join(dir, "release-bot.app.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Get("release-bot"); !ok {
		t.Fatal("Get(release-bot) before removing it = not configured")
	}
	if err := set.Remove("release-bot"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := set.Get("release-bot"); ok {
		t.Error("Get(release-bot) after Remove still reports it configured")
	}
	if got := set.Names(); !slices.Equal(got, []string{"bot"}) {
		t.Errorf("Names() = %v, want only bot -- both file shapes should be gone", got)
	}
	if err := set.Remove("release-bot"); err == nil {
		t.Error("removing a credential that is not configured reported success")
	}
}

func TestValidCredentialNameSaysWhyItRefused(t *testing.T) {
	if err := ValidCredentialName("release-bot_2"); err != nil {
		t.Errorf("ValidCredentialName(release-bot_2) = %v, want nil", err)
	}
	err := ValidCredentialName("anonymous")
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("ValidCredentialName(anonymous) = %v, want an error saying it is reserved", err)
	}
}

func TestDirIsWhereTheLadderWasLoadedFrom(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", set.Dir(), dir)
	}
}

// A credential named before this package wrote any of these files
// itself -- with a dot in it, which SetToken would refuse today -- is
// still listed, and so must still be removable from the same listing.
func TestRemoveWorksForANameSetTokenWouldNotAccept(t *testing.T) {
	dir := writeCredentialSet(t, nil, map[string]string{"legacy.bot": "legacy-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(set.Names(), "legacy.bot") {
		t.Fatalf("Names() = %v, want it to list legacy.bot", set.Names())
	}
	if err := set.Remove("legacy.bot"); err != nil {
		t.Fatalf("Remove(legacy.bot): %v", err)
	}
	if len(set.Names()) != 0 {
		t.Errorf("Names() = %v after removing the only credential", set.Names())
	}
}

// What Remove will not do is address anything but a file in its own
// directory.
func TestRemoveRefusesANameThatIsNotOneFileHere(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.token")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadCredentialSet(filepath.Join(dir, "github"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../victim", `..\victim`} {
		if err := set.Remove(name); err == nil {
			t.Errorf("Remove(%q) reported success", name)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a file outside the secrets directory was removed: %v", err)
	}
}
