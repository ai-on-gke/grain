package actionscache

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scopes issue #2392 is about: a task branch, the branch it builds
// on, and a release run of that branch.
var (
	taskBranch = Scope{Repo: "acme/widgets", Ref: "pr-666"}
	mainPush   = Scope{Repo: "acme/widgets", Ref: "main"}
	release    = Scope{Repo: "acme/widgets", Ref: "main", Trusted: true}
)

// Issue #2392's own proof, inverted: what a job on an untrusted branch
// uploads under a key is not restored by a job of any other branch, nor
// by a release, nor downloadable by them knowing its id.
func TestAnotherBranchCannotPoisonTheCache(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id := storeAs(t, c, taskBranch, "npm-cache-v1-linux", "abc123", []byte("POISON"))

	for _, reader := range []Scope{mainPush, release, {Repo: "acme/widgets", Ref: "pr-667"}} {
		if e, ok := c.Find(reader, []string{"npm-cache-v1-linux"}, "abc123"); ok {
			t.Errorf("%+v restored entry %d written by %s", reader, e.ID, taskBranch.Ref)
		}
		if _, ok := c.Find(reader, []string{"npm-cache-"}, "abc123"); ok {
			t.Errorf("%+v restored another branch's entry by restore-key prefix", reader)
		}
		if _, ok := c.Body(reader, id); ok {
			t.Errorf("%+v could download another branch's entry %d by id", reader, id)
		}
	}
	// The branch that wrote it still reads it back: a second push of a
	// task branch building warm is what the cache is for.
	if _, ok := c.Find(taskBranch, []string{"npm-cache-v1-linux"}, "abc123"); !ok {
		t.Error("a branch could not read back its own entry")
	}
}

// Not even a push to the branch being released reaches a release: only
// what release runs wrote is ever restored into one.
func TestAReleaseReadsOnlyWhatReleasesWrote(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	storeAs(t, c, mainPush, "layers", "v1", []byte("from a push"))
	if _, ok := c.Find(release, []string{"layers"}, "v1"); ok {
		t.Fatal("a release restored an entry an ordinary push wrote")
	}
	id := storeAs(t, c, release, "layers", "v1", []byte("from a release"))
	e, ok := c.Find(release, []string{"layers"}, "v1")
	if !ok || e.ID != id {
		t.Fatalf("a release found %+v, %v; want its own entry %d", e, ok, id)
	}
	if !e.Trusted || e.Ref != "main" {
		t.Errorf("a release's entry recorded as %+v", e)
	}
}

// A branch falls back on what releases wrote -- GitHub's "a branch may
// read its base branch's cache" -- but always prefers its own entries,
// even an older one.
func TestABranchFallsBackOnReleaseEntries(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	trusted := storeAs(t, c, release, "deps-abc", "v1", []byte("released"))
	e, ok := c.Find(taskBranch, []string{"deps-abc"}, "v1")
	if !ok || e.ID != trusted {
		t.Fatalf("a branch with nothing of its own found %+v, %v; want the release's entry %d", e, ok, trusted)
	}
	if _, ok := c.Body(taskBranch, trusted); !ok {
		t.Error("a branch could not download the release entry its lookup answered with")
	}

	own := storeAs(t, c, taskBranch, "deps-", "v1", []byte("mine"))
	storeAs(t, c, release, "deps-abd", "v1", []byte("released later"))
	// The release has an exact match for the first key; the branch has
	// only a prefix match on the restore key. Its own ref still wins,
	// because GitHub searches the whole key list on the current branch
	// before it looks anywhere else.
	e, ok = c.Find(taskBranch, []string{"deps-abc", "deps-"}, "v1")
	if !ok || e.ID != own {
		t.Errorf("found %+v, %v; want the branch's own entry %d ahead of any release's", e, ok, own)
	}
}

// Ids are sequential, so a reservation is guarded by the scope that
// made it, not by its id being hard to guess: nobody else may add to an
// upload in progress or commit it.
func TestOnlyTheReservingScopeMayUploadIntoAReservation(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id, err := c.Reserve(release, "layers", "v1", 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []Scope{mainPush, taskBranch, {Repo: "acme/widgets", Ref: "pr-666", Trusted: true}} {
		if other == release {
			continue
		}
		if err := c.Write(other, id, 0, strings.NewReader("POISON")); err == nil {
			t.Errorf("%+v wrote into a release's reservation", other)
		}
		if _, err := c.Commit(other, id); err == nil {
			t.Errorf("%+v committed a release's reservation", other)
		}
	}
	if err := c.Write(release, id, 0, strings.NewReader("honest")); err != nil {
		t.Fatalf("the reserving release could not upload: %v", err)
	}
	if _, err := c.Commit(release, id); err != nil {
		t.Fatalf("the reserving release could not commit: %v", err)
	}
	path, ok := c.Body(release, id)
	if !ok {
		t.Fatal("committed release entry not servable")
	}
	if got, _ := os.ReadFile(path); string(got) != "honest" {
		t.Errorf("release entry holds %q", got)
	}
}

// A scope with no ref names nothing: it can neither reserve nor find,
// and a token is never granted one.
func TestAScopeWithNoRefNamesNothing(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	storeAs(t, c, mainPush, "k", "v1", []byte("x"))
	noRef := Scope{Repo: "acme/widgets", Ref: "  "}
	if _, err := c.Reserve(noRef, "k", "v1", 1); err == nil {
		t.Error("reserved with no ref")
	}
	if _, ok := c.Find(noRef, []string{"k"}, "v1"); ok {
		t.Error("found with no ref")
	}
	s := NewScopes()
	s.Grant("tok", noRef)
	if s.Granted() != 0 {
		t.Error("granted a token with no ref")
	}
	// refs/heads/main and main are one ref.
	if _, ok := c.Find(Scope{Repo: "acme/widgets", Ref: "refs/heads/main"}, []string{"k"}, "v1"); !ok {
		t.Error("refs/heads/main did not read main's entry")
	}
}

// Entries written before entries recorded a ref came from no branch
// anybody can name, so nobody may read them and a restart clears them.
func TestEntriesWithNoRefAreDroppedOnReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := NewCache(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	storeAs(t, c, mainPush, "kept", "v1", []byte("x"))
	legacy := filepath.Join(dir, "50")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "meta.json"),
		[]byte(`{"id":50,"repo":"acme/widgets","key":"legacy","version":"v1","size":1,"complete":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "body"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCache(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Find(mainPush, []string{"kept"}, "v1"); !ok {
		t.Error("an entry with a ref did not survive a restart")
	}
	if n := reopened.Stats().Entries; n != 1 {
		t.Errorf("reopened cache holds %d entries, want 1", n)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("an entry with no ref was left on disk: %v", err)
	}
}

// The same boundary over HTTP, where it is the token that carries the
// scope: one branch's job cannot reach what another's uploaded.
func TestServerIsolatesBranches(t *testing.T) {
	t.Parallel()
	c, cache, scopes := newServer(t, nil)
	scopes.Grant("pr", taskBranch)
	scopes.Grant("main", mainPush)
	scopes.Grant("release", release)

	id := storeAs(t, cache, taskBranch, "image-layers", "v1", []byte("POISON"))
	for _, token := range []string{"main", "release"} {
		resp, body := c.do("GET", c.url(token, "_apis/artifactcache/cache?keys=image-layers&version=v1"), "", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("%s lookup of a task branch's entry = %d %s, want a miss", token, resp.StatusCode, body)
		}
		resp, _ = c.do("GET", c.url(token, fmt.Sprintf("_apis/artifactcache/artifacts/%d", id)), "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s download of a task branch's entry = %d, want 404", token, resp.StatusCode)
		}
	}

	reserved, err := cache.Reserve(release, "image-layers", "v1", 6)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := c.do("PATCH", c.url("pr", fmt.Sprintf("_apis/artifactcache/caches/%d", reserved)), "POISON",
		map[string]string{"Content-Range": "bytes 0-5/*"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a task branch's upload into a release's reservation = %d %s, want 400", resp.StatusCode, body)
	}
}
