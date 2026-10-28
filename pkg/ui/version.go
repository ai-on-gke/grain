package ui

import (
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// versionResponse is GET /api/config's "version" object: which build of
// grain is answering. It rides on that response rather than a route of
// its own because it is chrome -- Sidebar.jsx prints it in the rail's
// footer on every view -- and /api/config is the one call App.jsx
// already makes before rendering anything.
//
// Read from this binary's own build stamp (pkg/version) rather than from
// Config or the store: it describes the process serving the request, not
// anything an operator configured, so there is nothing here for a
// deployment to have to set and nothing for it to get wrong.
type versionResponse struct {
	// Release is the release version or tag (e.g. "v1.2.0", "v1.3.0-rc").
	// Omitted when unstamped.
	Release string `json:"release,omitempty"`
	// Edition is the build edition or variant (e.g. "google" when
	// google/ is linked in), or "" for the core/open-source build.
	Edition string `json:"edition,omitempty"`
	// Commit is the full hash, not the seven characters the sidebar
	// shows: shortening is a display choice, and the full value is what
	// gets pasted into `git show` when a UI is reporting something odd.
	Commit string `json:"commit,omitempty"`
	// CommittedAt is that commit's own timestamp, in UTC -- vcs.time,
	// which is when the code was committed rather than when it was
	// built (Go records no build time). Omitted when the stamp carried
	// no readable time, which Commit alone survives.
	CommittedAt *time.Time `json:"committedAt,omitempty"`
	// Modified says the build ran against uncommitted changes, so Commit
	// does not fully describe what is running. Never true of a CI build;
	// worth showing when it happens, since it means a deployment nobody
	// can reproduce from the hash beside it.
	Modified bool `json:"modified,omitempty"`
	// Unversioned says nothing stamped a version into this build, so
	// Release, when there is one, is only Go's module pseudo-version
	// (pkg/version.Info.Unversioned).
	Unversioned bool `json:"unversioned,omitempty"`
}

// versionResponseFrom converts a build stamp for the wire, reporting nil
// for a binary that carries none -- a -buildvcs=false build, or any test
// binary. nil means the field is absent from the response entirely and
// the sidebar prints nothing, the same nil-means-nothing-to-show shape
// the optional Config fields use, rather than a version object full of
// empty strings the frontend would have to special-case.
func versionResponseFrom(info version.Info) *versionResponse {
	if info.Revision == "" && info.Release == "" && info.Edition == "" {
		return nil
	}
	resp := &versionResponse{
		Release:  info.Release,
		Edition:  info.Edition,
		Commit:   info.Revision,
		Modified: info.Modified,

		Unversioned: info.Unversioned,
	}
	if !info.Time.IsZero() {
		t := info.Time
		resp.CommittedAt = &t
	}
	return resp
}
