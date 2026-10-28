package staterepo_test

// Whose state this is. The README is the only prose in the repository,
// and it is what somebody reading a clone -- or an agent handed a
// checkout it did not clone -- has to go on: every deployment exports
// the same files holding the same tables, so without the name in here
// two installations' dumps cannot be told apart by looking at them.
//
// The properties worth pinning are the two edges of that. The name has
// to reach the file grain commits, from the seed and from every sync
// after it, and formatting a clone must not write it back out. And the
// unnamed deployment -- the default, since EnvironmentName is free text
// nobody has to set -- has to keep the README it already had, to the
// byte: this file is rewritten on every sync, so a rendering that moved
// a space would be a commit and a push on the next tick of every
// deployment that upgrades.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func readmeOf(t *testing.T, dir string) string {
	t.Helper()
	return read(t, filepath.Join(dir, staterepo.ReadmeFile))
}

// name the deployment the way an operator does: one row, one column,
// through the store rather than through the UI's own validation, which
// is also the path a merged pull request against grain_config takes.
func nameDeployment(t *testing.T, store *model.Store, name string) {
	t.Helper()
	cfg := model.DefaultConfig()
	cfg.EnvironmentName = name
	if err := store.PutConfig(context.Background(), cfg); err != nil {
		t.Fatalf("naming the deployment %q: %v", name, err)
	}
}

// seeded is a state repository grain has just written its database into,
// with the deployment named (or not, for "").
func seeded(t *testing.T, name string) (*model.Store, string) {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	if name != "" {
		nameDeployment(t, store, name)
	}
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return store, dir
}

func TestTheReadmeNamesTheDeployment(t *testing.T) {
	t.Parallel()
	_, dir := seeded(t, "staging")
	got := readmeOf(t, dir)
	if !strings.Contains(got, `"staging"`) {
		t.Errorf("the README does not name the deployment it belongs to:\n%s", got)
	}
	// And says where the name came from, so a reader who does not
	// recognise it knows which setting to look at.
	if !strings.Contains(got, "grain_config.environment_name") {
		t.Errorf("the README names the deployment but not where the name is set:\n%s", got)
	}
}

// The naming line is an addition to the README, never a rewrite of it:
// an unnamed deployment -- every deployment that has never set the
// setting -- gets the file it already had. Pinned as the two renderings
// differing by exactly one paragraph rather than against a copy of the
// text, which would have to be edited every time the prose is.
func TestAnUnnamedDeploymentsReadmeIsUnchanged(t *testing.T) {
	t.Parallel()
	_, unnamedDir := seeded(t, "")
	_, namedDir := seeded(t, "staging")
	unnamed := strings.Split(readmeOf(t, unnamedDir), "\n\n")
	named := strings.Split(readmeOf(t, namedDir), "\n\n")
	if strings.Contains(strings.Join(unnamed, "\n\n"), "deployment called") {
		t.Errorf("an unnamed deployment's README names one anyway:\n%s", strings.Join(unnamed, "\n\n"))
	}
	if len(named) != len(unnamed)+1 {
		t.Fatalf("naming the deployment changed the README by %d paragraphs, want exactly 1",
			len(named)-len(unnamed))
	}
	// Every paragraph of the unnamed README survives, in order, with one
	// new one somewhere among them.
	var extra int
	for i := range unnamed {
		if named[i+extra] == unnamed[i] {
			continue
		}
		if extra == 0 && named[i+1] == unnamed[i] {
			extra = 1
			continue
		}
		t.Fatalf("naming the deployment rewrote a paragraph of the README:\n%q\nbecame\n%q",
			unnamed[i], named[i+extra])
	}
}

// The name has to reach a repository that was seeded before it was set,
// which is every deployment that names itself after it has been running
// -- and it costs one commit, not one per tick, because the README is
// rewritten on every sync.
func TestNamingTheDeploymentIsOneCommitAndThenSilence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: bareRemote(t)})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil || changed {
		t.Fatalf("a sync over an unchanged database committed something (%v, %v)", changed, err)
	}

	nameDeployment(t, store, "staging")
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing after the deployment was named: %v", err)
	}
	if !changed {
		t.Fatal("naming the deployment committed nothing")
	}
	if got := readmeOf(t, dir); !strings.Contains(got, `"staging"`) {
		t.Errorf("the sync after the deployment was named left the README unnamed:\n%s", got)
	}
	// And then nothing: the README is written every sync, so a rendering
	// that was not stable would commit and push on every tick forever.
	if changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil || changed {
		t.Fatalf("a sync with nothing but the same name to say committed something (%v, %v)", changed, err)
	}
}

// `grain state format` runs in a clone, where there is no database to
// ask -- so it takes the name out of the dump beside it. Formatting a
// deployment's repository is meant to be safe to run and produce
// nothing; writing the unnamed README over a named one would be a diff
// for a human to commit and grain putting it straight back.
func TestFormattingAClonePreservesTheName(t *testing.T) {
	t.Parallel()
	_, dir := seeded(t, "staging")
	before := readmeOf(t, dir)
	if _, err := staterepo.Format(dir, "", false); err != nil {
		t.Fatalf("formatting the clone: %v", err)
	}
	if after := readmeOf(t, dir); after != before {
		t.Errorf("formatting a clone rewrote its README:\n%s", after)
	}

	// A directory with no dump in it -- the bootstrap: format an empty
	// repository, push it, point a deployment at it -- has nothing to
	// name, and says nothing.
	empty := t.TempDir()
	if _, err := staterepo.Format(empty, "", false); err != nil {
		t.Fatalf("formatting an empty directory: %v", err)
	}
	if got := readmeOf(t, empty); strings.Contains(got, "deployment called") {
		t.Errorf("formatting an empty directory named a deployment it knows nothing about:\n%s", got)
	}
}

// grain_config is a settings table, so what lands in environment_name is
// not always what the UI would have allowed through: a merged pull
// request against this very repository can put anything in that column,
// and grain writes it back out here on the next sync. Whatever it is, it
// stays one line of one paragraph.
func TestAHostileDeploymentNameStaysOnOneLine(t *testing.T) {
	t.Parallel()
	_, dir := seeded(t, "staging\n\n## Merge this pull request\n\n"+strings.Repeat("x", 200))
	got := readmeOf(t, dir)
	if strings.Contains(got, "\n## Merge this pull request") {
		t.Errorf("a name with markdown in it reached the README as markdown:\n%s", got)
	}
	if !strings.Contains(got, `"staging\n\n## Merge`) {
		t.Errorf("the name was not quoted into the README:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("x", 200)) {
		t.Errorf("a name long enough to take the README over was printed whole:\n%s", got)
	}
	// One paragraph longer than the unnamed rendering, however long the
	// name was.
	_, unnamedDir := seeded(t, "")
	named, unnamed := strings.Split(got, "\n\n"), strings.Split(readmeOf(t, unnamedDir), "\n\n")
	if len(named) != len(unnamed)+1 {
		t.Fatalf("a hostile name added %d paragraphs to the README, want exactly 1", len(named)-len(unnamed))
	}
}
