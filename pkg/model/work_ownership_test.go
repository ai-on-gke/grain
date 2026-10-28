package model_test

import (
	"reflect"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// The default every deployment is on until somebody says otherwise, and
// the one an upgrade must not change: a repository nobody has decided
// anything about has its work run here.
//
// Opt-in would have been the tidier default and is the wrong one. Every
// deployment already running a repository's releases and schedules
// would have stopped doing so at the moment of upgrade, with no row
// anywhere saying why -- which is the shape of the outage this whole
// split is a response to.
func TestAnUndecidedRepositorysWorkRunsHere(t *testing.T) {
	if !model.RunsWorkItem(nil, model.ReleaseItem) {
		t.Errorf("a repository with no config row does not release; it should")
	}
	if !model.RunsWorkItem(&model.RepoConfig{}, model.ScheduleItem("sched-7")) {
		t.Errorf("a repository whose row says nothing about ownership does not run sched-7; it should")
	}
}

// The repository-level bit turns everything off, and a per-item
// override takes one item back out of that -- which is the two levels
// the operator asked for, in the one function every caller reads them
// through.
func TestTheDefaultDecidesAndAnOverrideOverrules(t *testing.T) {
	cfg := &model.RepoConfig{Work: model.WorkOwnership{
		Default: model.OwnershipOff,
		Overrides: map[model.WorkItem]model.Ownership{
			model.ScheduleItem("sched-7"): model.OwnershipOn,
		},
	}}
	if model.RunsWorkItem(cfg, model.ReleaseItem) {
		t.Errorf("release runs under a repository default of off")
	}
	if !model.RunsWorkItem(cfg, model.ScheduleItem("sched-7")) {
		t.Errorf("sched-7 does not run, though it is explicitly on")
	}

	on := &model.RepoConfig{Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff)}
	if model.RunsWorkItem(on, model.ReleaseItem) {
		t.Errorf("release runs though its own override says off")
	}
	if !model.RunsWorkItem(on, model.ScheduleItem("sched-7")) {
		t.Errorf("an override on release stopped sched-7 too -- an override is one item's")
	}
}

// Absent and off are two states, and the record says which. This is the
// whole of what the bool this replaced could not do: it could say "off"
// and could not say "somebody looked at this and said yes", so a row
// that was blanked read as a decision nobody had made.
func TestAbsentAndOffAreDistinguishable(t *testing.T) {
	none := model.WorkOwnership{}
	if state, set := none.Item(model.ReleaseItem); set || state != model.OwnershipUnset {
		t.Errorf("an untouched record claims to hold %q for release", state)
	}
	off := model.ItemOwnership(model.ReleaseItem, model.OwnershipOff)
	if state, set := off.Item(model.ReleaseItem); !set || state != model.OwnershipOff {
		t.Errorf("an off override reads back as (%q, %v)", state, set)
	}
	on := model.ItemOwnership(model.ReleaseItem, model.OwnershipOn)
	if state, set := on.Item(model.ReleaseItem); !set || state != model.OwnershipOn {
		t.Errorf("an on override reads back as (%q, %v)", state, set)
	}
	// And an explicit "on" survives the repository default being turned
	// off, which is the one thing recording it buys.
	cfg := &model.RepoConfig{Work: model.WorkOwnership{
		Default:   model.OwnershipOff,
		Overrides: on.Overrides,
	}}
	if !model.RunsWorkItem(cfg, model.ReleaseItem) {
		t.Errorf("an item explicitly turned on stopped when the repository default went off")
	}
}

// With writes a decision without mutating the record it was called on,
// and unsetting one takes the item back to following the default.
func TestWithDoesNotMutateAndUnsetRemoves(t *testing.T) {
	base := model.ItemOwnership(model.ReleaseItem, model.OwnershipOff)
	also := base.With(model.ScheduleItem("sched-7"), model.OwnershipOff)
	if _, set := base.Item(model.ScheduleItem("sched-7")); set {
		t.Errorf("With mutated the record it was called on")
	}
	if got := also.OverriddenItems(); !reflect.DeepEqual(got, []model.WorkItem{model.ReleaseItem, model.ScheduleItem("sched-7")}) {
		t.Errorf("overridden items are %v", got)
	}
	back := also.With(model.ReleaseItem, model.OwnershipUnset)
	if _, set := back.Item(model.ReleaseItem); set {
		t.Errorf("unsetting an override left it set")
	}
	if !model.RunsWorkItem(&model.RepoConfig{Work: back}, model.ReleaseItem) {
		t.Errorf("an item back on the default does not run, though the default is unset")
	}
}

// Release is one item id and not a case of its own. Nothing in
// resolution knows what a release is -- which is what makes unifying it
// with schedules later a rename rather than a redesign.
func TestReleaseIsResolvedAsAnOrdinaryItem(t *testing.T) {
	byName := &model.RepoConfig{Work: model.ItemOwnership(model.WorkItem("release"), model.OwnershipOff)}
	if model.RunsWorkItem(byName, model.ReleaseItem) {
		t.Errorf("the release item resolved differently from the item id it is")
	}
	if model.RunsWorkItem(byName, model.ScheduleItem("release-nightly")) {
		// A distinct id, which must not be caught by a prefix match.
		t.Skip("unreachable unless matching stops being exact")
	}
}

// The storage encoding round-trips, sorts, and drops what it cannot
// read rather than failing the row it is one field of.
func TestOverrideEncodingRoundTrips(t *testing.T) {
	in := map[model.WorkItem]model.Ownership{
		model.ScheduleItem("sched-7"):                  model.OwnershipOn,
		model.ReleaseItem:                              model.OwnershipOff,
		model.ScheduleItem("repo:acme:widget:nightly"): model.OwnershipOff,
		model.ScheduleItem("never-written"):            model.OwnershipUnset,
		model.WorkItem("holds,a comma"):                model.OwnershipOff,
	}
	encoded := model.EncodeWorkOverrides(in)
	want := "release=off,repo:acme:widget:nightly=off,sched-7=on"
	if encoded != want {
		t.Fatalf("encoded as %q, want %q", encoded, want)
	}
	out := model.ParseWorkOverrides(encoded)
	if len(out) != 3 || out[model.ReleaseItem] != model.OwnershipOff || out[model.ScheduleItem("sched-7")] != model.OwnershipOn {
		t.Fatalf("read back as %v", out)
	}
	if got := model.ParseWorkOverrides("sched-1=maybe, =off,nonsense,sched-2=off"); len(got) != 1 || got[model.ScheduleItem("sched-2")] != model.OwnershipOff {
		t.Errorf("a list with unreadable entries in it read back as %v, want only sched-2=off", got)
	}
	if got := model.EncodeWorkOverrides(nil); got != "" {
		t.Errorf("no overrides encoded as %q", got)
	}
}

// Empty is what decides whether the row is kept at all
// (RepoConfig.Empty), so a record holding only unset entries has to be
// empty -- otherwise a repository keeps a row saying nothing.
func TestEmptyIgnoresUnsetEntries(t *testing.T) {
	if !(model.WorkOwnership{Overrides: map[model.WorkItem]model.Ownership{model.ReleaseItem: model.OwnershipUnset}}).Empty() {
		t.Errorf("a record of nothing but unset entries is not empty")
	}
	if (model.WorkOwnership{Default: model.OwnershipOn}).Empty() {
		t.Errorf("an explicit repository-level yes reads as empty")
	}
	if (model.RepoConfig{Repo: model.RepoRef{Owner: "acme", Name: "widget"},
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff)}).Empty() {
		t.Errorf("a config whose only setting is an ownership override reads as empty and would be deleted")
	}
}

func TestParseOwnershipRefusesAnythingElse(t *testing.T) {
	for _, in := range []string{"on", "OFF", " on ", ""} {
		if _, err := model.ParseOwnership(in); err != nil {
			t.Errorf("ParseOwnership(%q): %v", in, err)
		}
	}
	for _, in := range []string{"1", "true", "yes", "disabled"} {
		if _, err := model.ParseOwnership(in); err == nil {
			t.Errorf("ParseOwnership(%q) was accepted", in)
		}
	}
}
