package ui_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSetRepoUnfixableChecksStoresTheList(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Nothing is the default, and a repository whose checks all work is
	// meant to stay in it.
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.UnfixableChecks) != 0 {
		t.Fatalf("unfixableChecks = %v for a repo nobody has configured, want none", read.UnfixableChecks)
	}
	if read.GrainCICheckName != forge.GrainCICheckName {
		t.Fatalf("grainCICheckName = %q, want %q", read.GrainCICheckName, forge.GrainCICheckName)
	}

	got, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google"})
	if err != nil {
		t.Fatalf("declaring cla/google unfixable: %v", err)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("unfixableChecks = %v after the save, want [cla/google]", got.UnfixableChecks)
	}
	if read, err = c.RepoDefaults(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("unfixableChecks = %v on a re-read, want the stored list", read.UnfixableChecks)
	}

	// And the whole list is replaced rather than added to, empty
	// included -- which is how a repository whose CLA bot has been
	// satisfied goes back to saying nothing.
	if got, err = c.SetRepoUnfixableChecks(ctx, "acme/widgets", nil); err != nil {
		t.Fatalf("clearing the list: %v", err)
	}
	if len(got.UnfixableChecks) != 0 {
		t.Fatalf("unfixableChecks = %v after clearing it, want none", got.UnfixableChecks)
	}
}

// A blank box on the form is somebody who has not typed a check name yet,
// and two spellings of one check are one declaration -- neither is worth
// refusing a save over.
func TestSetRepoUnfixableChecksTrimsAndDeduplicates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	got, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{
		"  cla/google  ", "", "   ", "CLA/Google", "license/cla",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cla/google", "license/cla"}
	if !reflect.DeepEqual(got.UnfixableChecks, want) {
		t.Fatalf("unfixableChecks = %v, want %v -- trimmed, blanks dropped, and the first spelling kept", got.UnfixableChecks, want)
	}
}

// grain CI's own verdict is never covered by a declaration
// (forge.Unfixable.Covers), so storing it would leave something on the
// pane that looks like a setting and is not one.
func TestSetRepoUnfixableChecksRefusesGrainCI(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	for _, name := range []string{forge.GrainCICheckName, strings.ToUpper(forge.GrainCICheckName)} {
		if _, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google", name}); err == nil {
			t.Fatalf("declaring %q unfixable was accepted, want it refused", name)
		}
	}
	// And the refusal writes nothing, so the repository is left as it was
	// rather than half-saved.
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.UnfixableChecks) != 0 {
		t.Fatalf("unfixableChecks = %v after a refused save, want none stored", read.UnfixableChecks)
	}
}

// The column is a comma-joined list, so a name with a comma in it would
// read back as two names no check is called.
func TestSetRepoUnfixableChecksRefusesACommaInAName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google,license/cla"}); err == nil {
		t.Fatal("a check name with a comma in it was accepted, want it refused")
	}
}

func TestUnfixableChecksSurviveTheOtherRepoSaves(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("unfixableChecks = %v after saving a setup command, want it untouched", got.UnfixableChecks)
	}
	if got, err = c.SetRepoIgnoreGitHubCI(ctx, "acme/widgets", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("unfixableChecks = %v after declaring grain CI the gate, want it untouched", got.UnfixableChecks)
	}

	if got, err = c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google"}); err != nil {
		t.Fatal(err)
	}
	if got.SetupCommand != "make deps" || !got.IgnoreGitHubCI {
		t.Fatalf("defaults = %+v after saving the unfixable checks, want the rest untouched", got)
	}
}

// The same list over a real net/http round trip, which is what `grain
// repo unfixable-checks` reaches the daemon through: the write and the
// read come back the same document, and the refusal survives the trip as
// an error naming grain CI rather than as a bare status a shell would
// print as "400".
func TestHTTPClientUnfixableChecksRoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)

	got, err := c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{"cla/google"})
	if err != nil {
		t.Fatalf("SetRepoUnfixableChecks: %v", err)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("unfixableChecks = %v after the save, want [cla/google]", got.UnfixableChecks)
	}
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("RepoDefaults: %v", err)
	}
	if !reflect.DeepEqual(read, got) {
		t.Fatalf("RepoDefaults = %+v, want what the write returned %+v", read, got)
	}
	if read.GrainCICheckName != forge.GrainCICheckName {
		t.Fatalf("grainCICheckName = %q over the wire, want %q", read.GrainCICheckName, forge.GrainCICheckName)
	}

	err = nil
	if _, err = c.SetRepoUnfixableChecks(ctx, "acme/widgets", []string{forge.GrainCICheckName}); err == nil {
		t.Fatal("declaring grain CI unfixable over the wire was accepted, want it refused")
	}
	if !strings.Contains(err.Error(), forge.GrainCICheckName) {
		t.Fatalf("refusal = %v, want it to name %q", err, forge.GrainCICheckName)
	}

	// Nil is how the last declaration is withdrawn, and it has to survive
	// the trip as `[]` rather than as a field the server never sees.
	if got, err = c.SetRepoUnfixableChecks(ctx, "acme/widgets", nil); err != nil {
		t.Fatalf("clearing the list over the wire: %v", err)
	}
	if len(got.UnfixableChecks) != 0 {
		t.Fatalf("unfixableChecks = %v after clearing them, want none", got.UnfixableChecks)
	}
}

func TestRepoUnfixableChecksGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/unfixable-checks",
		`{"unfixableChecks":["cla/google"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !reflect.DeepEqual(saved.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("put response = %+v, want acme/widgets with cla/google declared", saved)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/unfixable-checks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google"}) {
		t.Fatalf("the capabilities route reports unfixableChecks = %v, want the stored list", got.UnfixableChecks)
	}

	// The refusal reaches whoever is looking at the form, with grain CI's
	// own name in it rather than a bare 400.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/unfixable-checks",
		`{"unfixableChecks":["grain CI"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("naming grain CI: status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), forge.GrainCICheckName) {
		t.Fatalf("refusal = %s, want it to name %q", rec.Body, forge.GrainCICheckName)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/unfixable-checks",
		`{"unfixableChecks":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); len(got.UnfixableChecks) != 0 {
		t.Fatalf("unfixableChecks = %v after clearing them, want none", got.UnfixableChecks)
	}
}
