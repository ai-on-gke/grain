package ui_test

// grain/task-382: registry-push and package-publish are CI capabilities.
// An operator enables one for a repository, and a job receives it only
// for a trusted post-merge release or an operator's own
// workflow_dispatch. They were also rows in the per-task picker, and
// every route by which a task could come to hold one -- ticking the box,
// the deployment-wide default set, a repo's own default set -- took the
// task path, which applies no event gate at all.
//
// These tests are the four doors, checked from the outside: the API a
// browser actually calls.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/imagesigning"
	"github.com/bwsalmon/grain/pkg/capability/packagepublish"
	"github.com/bwsalmon/grain/pkg/capability/registrypush"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// ciOnlyIDs is the capabilities this whole file is about, named from the
// providers themselves rather than spelled out again here.
var ciOnlyIDs = []string{registrypush.CapabilityName, packagepublish.CapabilityName, imagesigning.CapabilityName}

func TestCIOnlyCapabilitiesAreNotInTheTaskPicker(t *testing.T) {
	t.Parallel()
	offered := ui.OfferedCapabilities()
	for _, id := range ciOnlyIDs {
		if slices.ContainsFunc(offered, func(c ui.Capability) bool { return c.ID == id }) {
			t.Errorf("the task capability picker offers %q", id)
		}
	}
}

// Every way a human can put a capability id on a task, refused -- and
// refused with a sentence naming where the grant that does work lives,
// since "unknown capability package-publish" is a true thing to say
// about a capability this build ships and publishes releases with, and
// no help at all to the person reading it.
func TestCIOnlyCapabilitiesAreRefusedEverywhereATaskCouldHoldOne(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	for _, id := range append(slices.Clone(ciOnlyIDs), model.ReleaseGateReadCapability) {
		// The deployment-wide default set: one accepted id here would
		// be one every task on the deployment is filed holding.
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
			DefaultCapabilities: &[]string{id},
		}); !refusesAsCIOnly(err, id) {
			t.Errorf("UpdateSettings accepted %q as a task default: %v", id, err)
		}
		// The per-repo default set: the same, for every task filed
		// against that repo.
		if _, err := c.SetRepoDefaultCapabilities(ctx, repo.String(), []string{id}); !refusesAsCIOnly(err, id) {
			t.Errorf("SetRepoDefaultCapabilities accepted %q: %v", id, err)
		}
		// Filing a task that names it, and ticking the box on one that
		// already exists.
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "publish something", Capabilities: &[]string{id},
		}); !refusesAsCIOnly(err, id) {
			t.Errorf("CreateTask accepted %q: %v", id, err)
		}
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "publish something"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetCapability(ctx, task.ID, id, true); !refusesAsCIOnly(err, id) {
			t.Errorf("SetCapability attached %q: %v", id, err)
		}
	}

	// And what an operator is left with instead: the repo's CI grant,
	// which is where this capability has always belonged and is
	// untouched by any of the above.
	if _, err := c.SetRepoCICapabilities(ctx, repo.String(), ciOnlyIDs); err != nil {
		t.Fatalf("the repo's own CI capabilities refuse %v: %v", ciOnlyIDs, err)
	}
	defaults, err := c.RepoDefaults(ctx, repo.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ciOnlyIDs {
		if !slices.Contains(defaults.CICapabilities, id) ||
			!slices.Contains(defaults.AvailableCICapabilities, id) {
			t.Errorf("the repo's CI picker lost %q: %+v", id, defaults)
		}
	}

	// Settings still reports on them -- they are configured, checked
	// and granted somewhere -- and says which kind of ungrantable they
	// are, so the pane does not read them as the code drift "not
	// grantable" otherwise means.
	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: %d %s", rec.Code, rec.Body)
	}
	var settings struct {
		Capabilities []ui.CapabilityStatus `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	for _, id := range ciOnlyIDs {
		i := slices.IndexFunc(settings.Capabilities, func(s ui.CapabilityStatus) bool { return s.ID == id })
		if i < 0 {
			t.Fatalf("Settings has no row for %q at all", id)
		}
		if status := settings.Capabilities[i]; !status.CIOnly || status.Grantable {
			t.Errorf("%q reports ciOnly=%v grantable=%v, want true/false", id, status.CIOnly, status.Grantable)
		}
	}
}

// refusesAsCIOnly is the bar every refusal above is held to: it happened,
// and it says what to do instead rather than only that something is
// wrong.
func refusesAsCIOnly(err error, id string) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, id) &&
		strings.Contains(text, "CI capability") &&
		strings.Contains(text, "CI capabilities")
}
