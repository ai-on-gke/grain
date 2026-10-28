package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// Revisions are how an editor that saves a whole form -- ScheduleOverlay,
// SuiteOverlay and TemplateOverlay all do -- finds out that what it is
// about to write over is no longer what it loaded. Without one, two tabs
// open on the same schedule each PATCH every field they show, and the
// second save silently puts back every field the first one changed
// (found in the 2026-10-13 recurring/composed walkthrough).
//
// A revision is a digest of what the row says somebody could edit, in
// its wire form, rather than a counter on the row: nothing has to be
// stored, a row written before this existed has one, and -- the part a
// counter would get wrong -- it does not move when grain itself rewrites
// the row for reasons no editor shows as an edit. For a schedule those
// are a firing (NextRunAt/LastRunAt), Pause/Resume (Enabled, which the
// editor never sends: it is the pane's own button, saved on its own), and
// the display cache a template- or suite-backed schedule carries of what
// it fires (model.Store.ResyncTemplateSchedules), all left out below.
// An editor holding a stale copy of any of those has nothing it would
// write back over them.
//
// Sending one is optional. A PATCH without it is applied as before, so a
// script or a CLI that sets a single field is not asked to read first;
// one that sends it is refused with staleEditError when it no longer
// matches.

// staleEditError is a PATCH carrying a revision the row has since moved
// past: somebody else saved it after this caller loaded it. A 409 with
// apierror code "stale", so an editor can tell it from every other
// refusal and offer to reload rather than just print it.
type staleEditError struct {
	Kind string // "schedule", "suite", "template"
	ID   string
}

func (e *staleEditError) Error() string {
	return "this " + e.Kind + " was changed by somebody else after you opened it (" + e.Kind + " " + e.ID +
		"); reload it to see what they saved, then make your change again"
}

// staleEditCode is the apierror.Response code writeClientError gives a
// staleEditError.
const staleEditCode = "stale"

// revisionOf digests v's JSON. Two values that render the same JSON have
// the same revision, which is the point: the wire form is what an editor
// loaded and what it would send back.
func revisionOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Every value passed here is a plain wire struct; a failure is a
		// programming error, and an empty revision only means "no check".
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// scheduleRevision is w's revision: everything an editor can change,
// and nothing grain changes on its own (the comment above). It reads
// the wire form so that scheduleFrom can stamp it on what it renders;
// scheduleRevisionOf is the same for a row.
func scheduleRevision(w Schedule) string {
	w.Revision = ""
	// Names resolved by whoever rendered it, and facts derived at read
	// time: none of them is the row's.
	w.TemplateName, w.SuiteName, w.DeclaredIn, w.Suppressed, w.LastWorkflowRun = "", "", nil, nil, nil
	// Derived from the fields that are the row's, for a filter to read.
	w.Source, w.Type = "", ""
	// A record of skipped firings, which grain writes as the schedule comes due.
	w.Skips = nil
	w.Enabled, w.NextRunAt, w.LastRunAt, w.CreatedAt = false, time.Time{}, nil, time.Time{}
	if w.TemplateID != "" || w.SuiteID != "" {
		// What the template or suite decides, cached on the row for
		// display and rewritten whenever that template or suite is saved.
		// Repo and Base stay in: they are the schedule's own unless the
		// template is bound, and a bound template's resync rewrites them
		// only when its binding itself moves, which does change what this
		// schedule fires at.
		w.Title, w.Description, w.AutoMerge = "", "", false
		w.Reads, w.Writes, w.Capabilities, w.CustomFields = nil, nil, nil, nil
		w.NetworkAccess, w.NothingNeededAction = "", ""
	}
	return revisionOf(w)
}

func scheduleRevisionOf(s model.Schedule) string { return scheduleFrom(s, "", "").Revision }

// suiteRevision is w's revision. A suite has no runtime state on its row
// -- a run snapshots it (model.SuiteRun) -- so it is all of it but the
// creation time and what a reader resolved or derived.
func suiteRevision(w Suite) string {
	w.Revision, w.CreatedAt, w.DeclaredIn = "", time.Time{}, nil
	items := make([]SuiteItem, len(w.Items))
	for i, it := range w.Items {
		items[i] = SuiteItem{TemplateID: it.TemplateID}
	}
	w.Items = items
	return revisionOf(w)
}

func suiteRevisionOf(s model.Suite) string { return suiteFrom(s, nil).Revision }

// templateRevision is w's revision, on suiteRevision's reasoning.
func templateRevision(w Template) string {
	w.Revision, w.CreatedAt, w.DeclaredIn = "", time.Time{}, nil
	return revisionOf(w)
}

func templateRevisionOf(t model.Template) string { return templateFrom(t).Revision }

// checkRevision refuses sent when it names a revision other than
// current. A nil or blank one is no check at all (the comment above on
// why sending one is optional).
func checkRevision(kind, id string, sent *string, current string) error {
	if sent == nil || strings.TrimSpace(*sent) == "" {
		return nil
	}
	if strings.TrimSpace(*sent) != current {
		return &staleEditError{Kind: kind, ID: id}
	}
	return nil
}
