package ui

import (
	"context"
	"errors"
	"net/http"

	"github.com/bwsalmon/grain/pkg/releasenotes"
	"github.com/bwsalmon/grain/pkg/upgrade"
	"github.com/bwsalmon/grain/pkg/version"
)

// releaseVersions is GET /api/release-notes/versions' answer: every
// grain release this binary carries notes for, newest first, and the
// version this deployment runs, so the pane can mark it.
//
// Per-version notes are not the weekly ones beside them on the same
// path (releasenotes.go): those are rows this deployment writes about
// its own work, these are the notes each grain release shipped with,
// read out of the binary (pkg/releasenotes) so that showing them needs
// no access to the repository.
type releaseVersions struct {
	Running  string                 `json:"running,omitempty"`
	Versions []releasenotes.Version `json:"versions"`
}

// handleListReleaseVersions answers GET /api/release-notes/versions.
// Anybody signed in may read them -- they are what the binary ships
// with -- so, like the weekly notes, the handler checks nothing the
// server's own sign-in check has not.
func (s *Server) handleListReleaseVersions(w http.ResponseWriter, r *http.Request) {
	versions := releasenotes.List()
	if versions == nil {
		versions = []releasenotes.Version{}
	}
	writeJSON(w, http.StatusOK, releaseVersions{Running: version.Get().Release, Versions: versions})
}

// handleGetReleaseVersion answers GET /api/release-notes/versions/{version}
// with that release's whole notes.
func (s *Server) handleGetReleaseVersion(w http.ResponseWriter, r *http.Request) {
	note, ok := releasenotes.Get(r.PathValue("version"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("this build carries no release notes for "+r.PathValue("version")))
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// upgradeReleaseNoter is the Upgrader that can preview what a target
// brings (upgrade.Upgrader.ReleaseNotes); a test double need not.
type upgradeReleaseNoter interface {
	ReleaseNotes(ctx context.Context, branch string) (upgrade.ReleaseNotesPreview, error)
}

// handleUpgradeReleaseNotes answers GET /api/upgrade/release-notes?branch=,
// what the upgrade pane shows when a target is chosen: the release
// notes that target's build carries for every version newer than this
// one. A system admin's, like every other upgrade route -- answering it
// pulls and runs the target image.
func (s *Server) handleUpgradeReleaseNotes(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Upgrader == nil {
		writeError(w, http.StatusNotFound, errUpgradeUnavailable)
		return
	}
	branch := r.URL.Query().Get("branch")
	noter, ok := s.tasks.Config.Upgrader.(upgradeReleaseNoter)
	if !ok {
		writeJSON(w, http.StatusOK, upgrade.ReleaseNotesPreview{
			Branch: branch, Notes: []releasenotes.Note{}, NotInThisBuild: true, Message: upgrade.NotInThisBuild,
		})
		return
	}
	if _, err := s.tasks.Config.Upgrader.ValidateBranch(branch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	preview, err := noter.ReleaseNotes(r.Context(), branch)
	if err != nil {
		// Not a 502: the pane reads 502-504 as the daemon itself being
		// unreachable (api.js isUnreachable), and this is the daemon
		// answering that the target could not be asked.
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
