// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// The controller-local configuration repository status and controls.
// grain exports its configuration database as text into git so settings
// can be changed through native grain/config pull requests. The repository
// has no external mirror; these handlers expose status, export, reset
// notices, and the separate encrypted-secrets backup.
//
// Config.StateRepo nil means this UI is not colocated with a daemon that
// owns one (`grain demo`, or any UI serving a store it does not manage),
// and every route below reports itself unavailable rather than erroring
// on each call -- the same nil-means-unavailable contract Secrets,
// Reboot and Upgrader already give their own panes.

// StateRepoStatus is what the bootstrap pane renders: where state lives,
// what is in the repository, and which key its secrets are encrypted to.
// No secret value and no private key is ever in here -- the public key is
// safe to show, and showing it is how an operator confirms the key they
// hold is the key this deployment encrypts to.
type StateRepoStatus struct {
	// Available is false when this UI has no state repository to manage.
	Available  bool   `json:"available"`
	Mode       string `json:"mode,omitempty"`
	Repository string `json:"repository,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Branch     string `json:"branch,omitempty"`
	// Dir is the working tree on this host, for an operator who wants to
	// go and look at it.
	Dir string `json:"dir,omitempty"`
	// Head is the commit the working tree is on, empty before the first
	// commit exists.
	Head string `json:"head,omitempty"`
	// SchemaVersion is the schema the repository's dump was written by,
	// and BuildSchemaVersion the one this build knows. They differ only
	// when something is wrong, which is why both are reported rather than
	// a single "compatible" boolean that hides which way round it is.
	SchemaVersion      int `json:"schemaVersion"`
	BuildSchemaVersion int `json:"buildSchemaVersion"`
	// SecretsPublicKey is the public half of the key this host holds, and
	// SecretsKeyFile where its private half is read from -- the file an
	// operator has to back up.
	SecretsPublicKey string `json:"secretsPublicKey,omitempty"`
	SecretsKeyFile   string `json:"secretsKeyFile,omitempty"`
	// SecretsFileRecipient is the public key this host's secrets file is
	// actually sealed to, which is not always the key above: a file
	// restored from another installation is encrypted to that
	// installation's key. Both are shown because the difference is the
	// diagnosis, and a public key reveals nothing -- it can seal a
	// secret, not open one.
	SecretsFileRecipient string `json:"secretsFileRecipient,omitempty"`
	// SecretsError says why this host cannot read its own secrets file,
	// and is empty when it can. Separate from Error below because the two
	// have different fixes -- one is a key to import, the other a local
	// repository problem -- and because an unreadable store is not an
	// export failure.
	SecretsError string `json:"secretsError,omitempty"`
	// SecretsRepo is the repository this host's encrypted secrets file is
	// backed up to (pkg/secretsrepo), and SecretsRepoFile what it is
	// called there -- a name derived from the state repository, since a
	// secrets repository is assumed to be shared and an operator looking
	// for this deployment's file has to know which one is theirs.
	//
	// Empty is a deployment that keeps its ciphertext on this host alone,
	// which the pane says out loud rather than leaving as a line that is
	// not there: it is the arrangement one disk failure away from taking
	// every credential with it, and the key beside it cannot open a file
	// that no longer exists.
	// SecretsRepoBranch is the branch it is kept on, reported so the pane
	// can offer the branch already in force rather than defaulting a
	// deployment that lives on "trunk" back to "main" every time somebody
	// re-names the repository.
	SecretsRepo       string `json:"secretsRepo,omitempty"`
	SecretsRepoFile   string `json:"secretsRepoFile,omitempty"`
	SecretsRepoBranch string `json:"secretsRepoBranch,omitempty"`
	// ResetNotices is every settings reset this deployment could not tell
	// anybody about anywhere else, newest last, and ResetNoticesDropped
	// how many older ones the host discarded to stay inside its own cap.
	//
	// A merged settings file that omits a column replaces the prior value
	// with the schema default. With no external pull request to comment on,
	// the pane and journal carry that reset notice.
	//
	// Dismissed for the whole deployment rather than one browser; the
	// journal keeps the record either way.
	ResetNotices        []ResetNotice `json:"resetNotices,omitempty"`
	ResetNoticesDropped int           `json:"resetNoticesDropped,omitempty"`
	// SyncedAt is when the export loop last ran a cycle against this
	// repository, zero on a deployment whose first cycle has not
	// finished.
	//
	// It is here for the file browser below. The working tree is written
	// on a timer, so a person reading a file out of it is reading what
	// the last cycle left, and "which commit, as of when" is the
	// difference between reading a file and drawing a conclusion from a
	// stale one. Reported whether or not the cycle succeeded -- Error
	// above says which -- because "last tried at" is the honest fact and
	// a timestamp that stopped moving is itself the symptom.
	SyncedAt time.Time `json:"syncedAt,omitempty"`
	// LastSnapshot is the path of the latest pre-import settings snapshot,
	// if any has been taken on this host.
	LastSnapshot string `json:"lastSnapshot,omitempty"`
	// LastSnapshotAt is when the latest settings snapshot was written.
	LastSnapshotAt *time.Time `json:"lastSnapshotAt,omitempty"`
	// Error is a last import or export failure. Empty when the last cycle succeeded.
	Error string `json:"error,omitempty"`
}

// StateRepoEntry is one file or directory of the exported configuration,
// at a slash-separated path relative to the repository root.
type StateRepoEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	// Size is the file's length in bytes, 0 for a directory.
	Size int64 `json:"size"`
}

// StateRepoTree is the listing the browser renders, with the commit it
// was read at so the pane can say what is on screen.
type StateRepoTree struct {
	Entries []StateRepoEntry `json:"entries"`
	Head    string           `json:"head,omitempty"`
	// Truncated is a repository holding more files than one listing
	// carries.
	Truncated bool `json:"truncated,omitempty"`
}

// StateRepoFile is one file of the exported configuration as a reader
// sees it: bounded, and saying so when what came back is the beginning
// of the file rather than the file.
type StateRepoFile struct {
	// Path is what was read, which is the resolved path rather than
	// necessarily the spelling asked for.
	Path string `json:"path"`
	// Size is the whole file's length, whether or not all of it is here.
	Size    int64  `json:"size"`
	Content string `json:"content"`
	Head    string `json:"head,omitempty"`
	// Truncated says Content stops at the bound. Binary says the file is
	// not text, in which case Content is empty: a browser has nothing to
	// do with the bytes, and replacement characters would read as
	// corruption of the file itself.
	Truncated bool `json:"truncated,omitempty"`
	Binary    bool `json:"binary,omitempty"`
}

// StateRepoPathRefused is a browse request for something outside the
// exported configuration -- .git, a secrets path, "..", a symlink
// leading out of the tree.
//
// Reason is shown to the reader as it stands and names what is
// browsable instead. That is deliberate and it gives nothing away: the
// browsable set is the same on every deployment, and a bare 404 would
// leave a reader unable to tell a path they may not have from one that
// is not there -- which is the other error below.
type StateRepoPathRefused struct{ Reason string }

func (e *StateRepoPathRefused) Error() string { return e.Reason }

// StateRepoPathMissing is a path inside the browsable set that this
// deployment has not written -- a table it has no rows for, most often.
type StateRepoPathMissing struct{ Reason string }

func (e *StateRepoPathMissing) Error() string { return e.Reason }

// ResetNotice is one merge whose settings reset something, on a
// deployment that had nowhere to say so but here.
//
// It mirrors staterepo.Undelivered field for field; cmd/grain converts,
// so that this package carries the shape of what the pane renders and
// nothing about where the marker holding it lives.
type ResetNotice struct {
	// At is when the merge was taken up, and Commit which merge it was --
	// the whole SHA, abbreviated by whoever renders it.
	At     time.Time `json:"at"`
	Commit string    `json:"commit"`
	// Reason is why this could not be said where the change was made, in
	// the same sentence the deployment's journal carries. It is shown as
	// it stands, and it is the whole answer to "why am I being told this
	// here": the rule -- only when the comment could not be posted -- is
	// not one a reader should have to know.
	Reason string `json:"reason"`
	// Failed distinguishes a delivery error from the expected absence of
	// an external place to deliver the notice.
	Failed bool `json:"failed,omitempty"`
	// Values is what the merge replaced -- one line each, already cut to
	// a readable width by staterepo -- and MoreValues counts the rest,
	// which are in the deployment's journal.
	Values     []ResetValue `json:"values,omitempty"`
	MoreValues int          `json:"moreValues,omitempty"`
}

// ResetValue is one value a merge replaced: which file left the column
// out, which row and column it was, and both sides of the change.
//
// From and To are rendered as SQL literals ('Europe/London', 0, NULL),
// the notation the schema spells a default in, so the pane shows the
// same characters the journal and the pull-request comment do.
type ResetValue struct {
	Path   string `json:"path"`
	Column string `json:"column"`
	Row    string `json:"row"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// StateRepoRemoteRequest is "mirror this deployment's controller-local
// grain/config repository to this external git repository", or "stop":
// where that mirror repository is, what branch to sync on, and the
// credential to push and fetch with.
type StateRepoRemoteRequest struct {
	// Remote is the git URL of the mirror repository, or "" to stop mirroring
	// and keep grain/config on this host alone.
	Remote string
	// Branch is the branch state lives on; the package default when empty.
	Branch string
	// Token is an optional personal access token for Remote. Empty leaves
	// the deployment's GitHub credential ladder to answer.
	Token string
}

// SecretsRepoRequest is "back this deployment's encrypted secrets file
// up here", or "stop": where that repository is, what branch to keep the
// file on, and the credential to push it with.
//
// An empty Remote is the "stop" -- and it is deliberately the only way
// to ask for one, so that the handler above can insist a caller says so
// outright rather than turning a body that lost a field into a
// deployment quietly backed up nowhere.
type SecretsRepoRequest struct {
	// Remote is the git URL of the repository, or "" to stop backing the
	// file up at all and keep it on this host alone.
	Remote string
	// Branch is the branch the ciphertext lives on; the package default
	// when empty.
	Branch string
	// Token is the credential to push with, for a secrets repository this
	// deployment's own GitHub credential ladder does not cover -- a
	// private repository under another account is the case it exists for.
	// Empty leaves that ladder to answer, as it does for the state
	// repository.
	Token string
}

// StateRepoManager is the daemon's side of the bootstrap. cmd/grain
// implements it over pkg/staterepo; this package holds only the shape,
// so pkg/ui carries no git and no database dump of its own.
type StateRepoManager interface {
	// Status reports where state lives right now.
	Status(ctx context.Context) (StateRepoStatus, error)
	// DismissResetNotices forgets every settings-reset notice this
	// deployment could not deliver anywhere else (Status.ResetNotices).
	// It deletes them rather than marking them read, and there is no way
	// back: a reset is something that happened, not a condition, so
	// nothing is left to change one's mind about, and the values remain
	// in the deployment's journal
	// whether or not anybody dismisses the notice. A later merge that
	// resets something and cannot say so puts a fresh one up.
	DismissResetNotices(ctx context.Context) (StateRepoStatus, error)
	// ImportSecretsKey installs a private key the operator holds, so a
	// secrets file sealed to another installation's key becomes readable
	// on this host. It refuses a key that cannot open the file rather
	// than installing it and failing later.
	ImportSecretsKey(ctx context.Context, key string) (StateRepoStatus, error)
	// SetRemote names the external Git repository this deployment's
	// controller-local grain/config repository mirrors to (pkg/staterepo),
	// or clears it.
	SetRemote(ctx context.Context, req StateRepoRemoteRequest) (StateRepoStatus, error)
	// SetSecretsRepo names the repository this deployment's encrypted
	// secrets file is backed up to (pkg/secretsrepo), or clears it.
	//
	// It writes settings and nothing else -- no clone and no push happens
	// here. The daemon re-reads which repository it is on every sync
	// tick, so the first push follows within a tick of this returning.
	SetSecretsRepo(ctx context.Context, req SecretsRepoRequest) (StateRepoStatus, error)
	// Sync applies a merged native configuration change and exports the
	// current database into the local repository immediately.
	Sync(ctx context.Context) (StateRepoStatus, error)
	// Tree lists the exported configuration -- and only that. The
	// implementation serves an allowlist of the paths grain writes
	// (pkg/staterepo's browse.go), never the working tree, which holds
	// .git and sits next to this deployment's secret material.
	Tree(ctx context.Context) (StateRepoTree, error)
	// File reads one of those files, cut to limit bytes and saying so
	// when it was cut. A path outside the allowlist comes back as
	// *StateRepoPathRefused and one that is merely absent as
	// *StateRepoPathMissing, which the handler below turns into
	// different answers because they are different facts.
	File(ctx context.Context, path string, limit int64) (StateRepoFile, error)
}

func repoSlugFromRemote(remote string) string {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
		if slash := strings.IndexByte(s, '/'); slash >= 0 {
			s = s[slash+1:]
		} else {
			return remote
		}
	} else if colon := strings.IndexByte(s, ':'); colon >= 0 && !strings.Contains(s[:colon], "/") {
		s = s[colon+1:]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "/" + parts[1]
	}
	return remote
}

func normalizeStateRepoStatus(status StateRepoStatus) StateRepoStatus {
	status.Available = true
	if strings.TrimSpace(status.Remote) != "" {
		if status.Mode == "" {
			status.Mode = "external"
		}
		if status.Repository == "" {
			status.Repository = repoSlugFromRemote(status.Remote)
		}
	} else {
		if status.Mode == "" {
			status.Mode = "internal"
		}
		if status.Repository == "" {
			status.Repository = "grain/config"
		}
	}
	return status
}

func (c *Client) externalConfigRepo(ctx context.Context) (remote, branch string) {
	if c == nil {
		return "", ""
	}
	if c.Config.StateRepo != nil {
		if ext, ok := c.Config.StateRepo.(interface{ ExternalConfigRepo() (string, string) }); ok {
			remote, branch = ext.ExternalConfigRepo()
		} else if status, err := c.Config.StateRepo.Status(ctx); err == nil {
			remote = strings.TrimSpace(status.Remote)
			branch = strings.TrimSpace(status.Branch)
			if branch == "" && remote != "" {
				branch = "main"
			}
		}
	}
	if remote == "" && c.Config.DataDir != "" {
		if raw, err := os.ReadFile(filepath.Join(c.Config.DataDir, "state-repo.json")); err == nil {
			var s struct {
				Remote string `json:"remote"`
				Branch string `json:"branch"`
			}
			if json.Unmarshal(raw, &s) == nil {
				remote = strings.TrimSpace(s.Remote)
				branch = strings.TrimSpace(s.Branch)
				if branch == "" && remote != "" {
					branch = "main"
				}
			}
		}
	}
	return remote, branch
}

// handleGetStateRepo answers GET /api/state-repo: where this host keeps
// its working tree and its secrets key, and what went wrong last --
// a system administrator's to read.
func (s *Server) handleGetStateRepo(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeJSON(w, http.StatusOK, StateRepoStatus{Available: false})
		return
	}
	status, err := s.tasks.Config.StateRepo.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}

// maxBrowseLimit is the most of one file this API will return, and
// defaultBrowseLimit what it returns when the caller asks for no
// particular amount. A reader wants the beginning of tables/task.json
// far more than a browser wants all of it, and the bound is the same
// lesson the unbounded agent tools taught: cut, but say so.
const (
	defaultBrowseLimit = 256 << 10
	maxBrowseLimit     = 1 << 20
)

// handleListStateRepoFiles lists the exported configuration.
//
// Read-only, and the only way to read any of it without a shell on the
// controller: this repository is controller-local, so there is no forge
// page for it and no remote to point a browser at. Changing any of it is
// still a pull request against grain/config, which is the deployment's
// own model and what the pane says.
func (s *Server) handleListStateRepoFiles(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	tree, err := s.tasks.Config.StateRepo.Tree(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// handleGetStateRepoFile reads one of them.
//
// A path outside the exported configuration is 403 with a sentence
// naming what is browsable, rather than 404: the caller asked for
// something this deployment will not serve, and a reader is owed the
// difference between that and a file that is simply not there.
func (s *Server) handleGetStateRepoFile(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, errors.New("a path is required"))
		return
	}
	limit := int64(defaultBrowseLimit)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive number of bytes"))
			return
		}
		limit = min(n, maxBrowseLimit)
	}
	file, err := s.tasks.Config.StateRepo.File(r.Context(), path, limit)
	var refused *StateRepoPathRefused
	var missing *StateRepoPathMissing
	switch {
	case errors.As(err, &refused):
		writeError(w, http.StatusForbidden, refused)
	case errors.As(err, &missing):
		writeError(w, http.StatusNotFound, missing)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, file)
	}
}

// secretsKeyRequest is POST /api/state-repo/secrets-key's body: the
// private key itself, which goes to the daemon and is never read back
// out through here -- no route in this package returns one, and Status
// carries only public halves.
type secretsKeyRequest struct {
	Key string `json:"key"`
}

// handleImportSecretsKey installs the operator's own secrets key.
//
// A restored <data-dir>/secrets may contain a file sealed to the prior
// host's key. This endpoint lets the operator supply that key without ever
// returning a private key through the API.
func (s *Server) handleImportSecretsKey(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	var req secretsKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, errors.New("a key is required"))
		return
	}
	status, err := s.tasks.Config.StateRepo.ImportSecretsKey(r.Context(), strings.TrimSpace(req.Key))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}

// secretsRepoRequest is POST /api/state-repo/secrets-repo's body. A
// token, when one is given, is write-only in the same sense every secret
// this package handles is.
//
// None is what asks for the backup to stop, rather than an empty Remote
// meaning it: the two are one field apart in a JSON body, and of the two
// possible mistakes -- refusing a request that meant to clear, and
// clearing a deployment's only off-host copy of every credential it has
// because a field went missing -- only one is recoverable by asking
// again.
type secretsRepoRequest struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Token  string `json:"token"`
	None   bool   `json:"none"`
}

// handleSetSecretsRepo names the repository this deployment's encrypted
// secrets file is backed up to, from the pane that already reports where
// it goes.
//
// The pane has said "on this host alone: nothing pushes it anywhere"
// since the repository existed, and until now the only ways to act on
// that were `grain secrets repo` at a terminal, an environment variable
// at deploy time, or editing JSON in the data directory. An operator who
// runs this deployment through a browser could read the warning and do
// nothing about it, which is the same gap the state repository's own
// bootstrap pane was built to close.
//
// Nothing here clones, pushes or fetches: it writes the settings file
// and the daemon's next tick does the rest (stateManager's
// currentSecretsRepo re-reads it every cycle). So a repository that is
// unreachable or a credential that does not cover it fails in the
// journal and in this pane's error line a tick later, not in this
// request -- exactly as `grain secrets repo` behaves.
func (s *Server) handleSetSecretsRepo(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	var req secretsRepoRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	remote := strings.TrimSpace(req.Remote)
	switch {
	case req.None:
		remote = ""
	case remote == "":
		writeError(w, http.StatusBadRequest,
			errors.New(`a repository URL is required; send "none": true to stop backing the secrets file up`))
		return
	}
	status, err := s.tasks.Config.StateRepo.SetSecretsRepo(r.Context(), SecretsRepoRequest{
		Remote: remote,
		Branch: strings.TrimSpace(req.Branch),
		Token:  strings.TrimSpace(req.Token),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}

// stateRepoRemoteRequest is POST /api/state-repo/remote's body.
// None asks to switch back to the internal config repository, rather than an empty Remote meaning it.
type stateRepoRemoteRequest struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Token  string `json:"token"`
	None   bool   `json:"none"`
}

// handleSetStateRepoRemote names the external Git repository this
// deployment reads configuration from in external IaC mode, or switches
// back to the internal grain/config repository when "none": true is sent.
func (s *Server) handleSetStateRepoRemote(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	var req stateRepoRemoteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	remote := strings.TrimSpace(req.Remote)
	switch {
	case req.None:
		remote = ""
	case remote == "":
		writeError(w, http.StatusBadRequest,
			errors.New(`a repository URL is required; send "none": true to switch to the internal config repository`))
		return
	}
	status, err := s.tasks.Config.StateRepo.SetRemote(r.Context(), StateRepoRemoteRequest{
		Remote: remote,
		Branch: strings.TrimSpace(req.Branch),
		Token:  strings.TrimSpace(req.Token),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}

// handleDismissResetNotice is a person saying they have read what a
// merge took away.
//
// Dismissing removes the notice and its rail marker. The journal line
// written at the same moment remains, so dismissal does not erase the
// record.
//
// A bare POST rather than one naming a notice: what is dismissed is
// everything on the pane at the moment somebody read it, and a body
// naming one of several would need the browser to have an id for a fact
// that has none.
func (s *Server) handleDismissResetNotice(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	status, err := s.tasks.Config.StateRepo.DismissResetNotices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}

func (s *Server) handleSyncStateRepo(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.StateRepo == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment's UI does not manage a state repository"))
		return
	}
	status, err := s.tasks.Config.StateRepo.Sync(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeStateRepoStatus(status))
}
