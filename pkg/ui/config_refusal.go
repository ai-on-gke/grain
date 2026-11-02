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
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// ConfigManagedExternallyCode is the stable code of the refusal a
// configuration write gets when the deployment's configuration is
// managed in an external config repository: what an agent or the UI
// branches on, rather than on the wording of the sentence.
const ConfigManagedExternallyCode = "config_managed_externally"

// configRepoDocs is where the refusal points for the whole story.
const configRepoDocs = "https://github.com/ai-on-gke/grain/blob/main/docs/external-config-repo.md"

// externalConfigRepoError is that refusal (checkSettingsWritable).
//
// A bare "settings are read-only" names the repository and leaves the
// caller to reverse-engineer what to change in it. This carries the
// answer instead: the file or files holding the setting, and -- when the
// write could be rehearsed (configWriteRehearsal) -- the exact edit to
// commit there, as the export would have written it.
type externalConfigRepoError struct {
	Remote string
	Branch string
	// Internal is set when the repository is this deployment's own
	// controller-local grain/config rather than an external mirror:
	// non-bootstrap configuration is written only through it in either
	// mode (design/design/config-repo-settings.design.md, section 4.1).
	Internal bool
	// Paths are the repository files holding the setting: the files the
	// rehearsed write changed, or the files of the tables the route
	// writes when it was not rehearsed.
	Paths []string
	// Changes is the edit, one patch per file. Empty when the route
	// could not be rehearsed; Description says what to change instead.
	Changes     []staterepo.FileChange
	Description string
	// Access is whether the caller can open that pull request itself.
	// Nil when the caller is not an agent.
	Access *configRepoAccess
}

// configRepoAccess is what an agent's own task grants it on the config
// repository.
type configRepoAccess struct {
	CanWrite bool `json:"canWrite"`
	// Via names the grant that lets it; Need the one it would take.
	Via  string `json:"via,omitempty"`
	Need string `json:"need,omitempty"`
}

func (e *externalConfigRepoError) branch() string {
	if e.Branch == "" {
		return "main"
	}
	return e.Branch
}

func (e *externalConfigRepoError) repository() string {
	if e.Internal {
		return internalConfigRepo
	}
	return repoSlugFromRemote(e.Remote)
}

// internalConfigRepo is the controller-local configuration repository.
const internalConfigRepo = "grain/config"

// Error is the whole instruction, not just the refusal: it is what every
// relay of this error shows (an MCP tool result, a CLI's message), and
// the refusal on its own is the one sentence that left an agent guessing.
func (e *externalConfigRepoError) Error() string {
	var b strings.Builder
	if e.Internal {
		fmt.Fprintf(&b, "this setting is not written directly: configuration is managed in this deployment's config repository %s (%s).", internalConfigRepo, e.branch())
	} else {
		fmt.Fprintf(&b, "settings are read-only because configuration is managed by external config repository %s (%s).", e.Remote, e.branch())
	}
	where := e.repository() + "@" + e.branch()
	switch {
	case len(e.Changes) > 0:
		fmt.Fprintf(&b, " Configuration here is managed in %s. Commit this change to %s instead:\n\n", where, strings.Join(e.Paths, ", "))
		for _, c := range e.Changes {
			b.WriteString(c.Patch)
		}
		b.WriteString("\n" + e.openPullRequest())
	case len(e.Paths) > 0:
		fmt.Fprintf(&b, " Configuration here is managed in %s. Make this change in %s instead", where, strings.Join(e.Paths, ", "))
		if e.Description != "" {
			b.WriteString(": " + e.Description)
		}
		b.WriteString(". " + e.openPullRequest())
	}
	if a := e.Access; a != nil {
		if a.CanWrite {
			fmt.Fprintf(&b, " This task can write to %s (%s): open that pull request directly.", e.repository(), a.Via)
		} else {
			fmt.Fprintf(&b, " This task cannot write to %s; it would need %s.", e.repository(), a.Need)
		}
	}
	return b.String()
}

// openPullRequest is how the edit gets made.
func (e *externalConfigRepoError) openPullRequest() string {
	if e.Internal {
		return "Open a config pull request with it (POST /api/state-repo/pull-requests, or a task on " + internalConfigRepo + "); grain applies it once it merges."
	}
	return "Open a pull request there; grain applies it on its next sync."
}

// body is the JSON a refused write answers with (writeClientError).
func (e *externalConfigRepoError) body() map[string]any {
	paths := e.Paths
	if paths == nil {
		paths = []string{}
	}
	changes := e.Changes
	if changes == nil {
		changes = []staterepo.FileChange{}
	}
	out := map[string]any{
		"error":  e.Error(),
		"code":   ConfigManagedExternallyCode,
		"repo":   e.repository(),
		"remote": e.Remote,
		"branch": e.branch(),
		"path":   paths,
		"change": changes,
		"docs":   configRepoDocs,
	}
	if url := configRepoWebURL(e.Remote); url != "" {
		out["url"] = url
	}
	if e.Internal {
		out["internal"] = true
		out["url"] = "/repos/" + internalConfigRepo
	}
	if e.Description != "" {
		out["description"] = e.Description
	}
	if e.Access != nil {
		out["access"] = e.Access
	}
	return out
}

// configRepoWebURL is the browsable page of a GitHub remote, or empty
// for one this cannot tell is on GitHub.
func configRepoWebURL(remote string) string {
	s := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(s, "https://github.com/"):
		return s
	case strings.HasPrefix(s, "git@github.com:"):
		return "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
	case strings.HasPrefix(s, "ssh://git@github.com/"):
		return "https://github.com/" + strings.TrimPrefix(s, "ssh://git@github.com/")
	}
	return ""
}

// configRefusal builds the refusal for a write to tables, before any
// rehearsal has said exactly which lines it changes.
func (c *Client) configRefusal(ctx context.Context, remote, branch string, tables []string) *externalConfigRepoError {
	e := &externalConfigRepoError{Remote: remote, Branch: branch, Internal: remote == ""}
	for _, t := range tables {
		if p := staterepo.DumpPath(t); !slices.Contains(e.Paths, p) {
			e.Paths = append(e.Paths, p)
		}
	}
	if len(tables) > 0 {
		e.Description = "edit the " + strings.Join(tables, ", ") + " entries this request would change, in the format the file already uses"
	}
	e.Access = c.configRepoAccess(ctx, e.repository(), e.branch())
	return e
}

// configRepoAccess says whether the agent making this request may land
// work in the config repository: its own task's target, or a writes:
// grant naming it. Nil for any caller that is not an agent, who has no
// task to be granted anything by.
func (c *Client) configRepoAccess(ctx context.Context, repo, branch string) *configRepoAccess {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Kind != model.PrincipalAgent || c.Store == nil {
		return nil
	}
	need := fmt.Sprintf("a writes: grant on %s (base %s)", repo, branch)
	if call, ok := agentCallFromContext(ctx); ok && call.external != nil {
		// An external agent lands work by filing it, so it may when its
		// credential's tasks:write reaches the config repository.
		if call.grants.Allows("tasks:write", repo) {
			return &configRepoAccess{CanWrite: true, Via: "external agent credential"}
		}
		return &configRepoAccess{Need: "tasks:write on " + repo + " in the external agent's credential"}
	}
	task, err := c.Store.GetTask(ctx, p.ID)
	if err != nil || task == nil {
		return &configRepoAccess{Need: need}
	}
	if task.Target != nil && strings.EqualFold(task.Target.String(), repo) {
		return &configRepoAccess{CanWrite: true, Via: "it is this task's own repository"}
	}
	for _, w := range task.Writes {
		if strings.EqualFold(w.Repo.String(), repo) {
			return &configRepoAccess{CanWrite: true, Via: "this task's writes: grant"}
		}
	}
	return &configRepoAccess{Need: need}
}

// configWriteRehearsal is the model.WriteRehearsal every API request
// carries (Server.serveHTTP), unarmed. checkSettingsWritable arms it
// instead of refusing outright, so the guarded route runs on to its
// first Store write; that write is rehearsed -- run, exported on both
// sides, rolled back (staterepo.Rehearse) -- and refused with the
// difference it would have made.
//
// The refusal stops the route at its first write that changes the
// configuration, as the plain refusal used to stop it at the guard --
// with one exception. A create takes its row's id from a *_sequence
// table in a write of its own before it writes the row; that write is
// recorded, answered as if it had happened (the route goes on with the
// id it was issued) and folded into the refusal of the write after it,
// so the edit is the whole create and not just its id.
type configWriteRehearsal struct {
	mu      sync.Mutex
	refusal *externalConfigRepoError
	issued  []staterepo.FileChange
	// refused is set once a rehearsed write has answered with the
	// refusal, which is the only answer an armed request may send
	// (configRefusalWriter).
	refused bool
	// wroteSettings is set when guardConfigWrite permits a settings write
	// so configRefusalWriter can export the updated tables immediately to
	// grain/config once the handler succeeds.
	wroteSettings bool
}

// pending is the refusal an armed request still owes its caller: armed,
// and no rehearsed write has refused yet. Nil otherwise.
func (r *configWriteRehearsal) pending() *externalConfigRepoError {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusal == nil || r.refused {
		return nil
	}
	e := *r.refusal
	return &e
}

// onlySequences reports whether every change is to an id sequence.
func onlySequences(changes []staterepo.FileChange) bool {
	for _, c := range changes {
		if !strings.HasSuffix(c.Path, "_sequence.json") {
			return false
		}
	}
	return len(changes) > 0
}

func (r *configWriteRehearsal) arm(e *externalConfigRepoError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusal == nil {
		r.refusal = e
	}
}

func (r *configWriteRehearsal) Rehearsing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refusal != nil
}

func (r *configWriteRehearsal) Rehearse(ctx context.Context, tx *sql.Tx, write func(*sql.Tx) error) error {
	r.mu.Lock()
	refusal := *r.refusal
	r.mu.Unlock()
	changes, writeErr, err := staterepo.Rehearse(ctx, tx, write)
	if writeErr != nil {
		return writeErr
	}
	if err != nil {
		log.Printf("grain api: rehearsing a refused configuration write: %v", err)
		r.markRefused()
		return &refusal
	}
	if onlySequences(changes) {
		r.mu.Lock()
		r.issued = append(r.issued, changes...)
		r.mu.Unlock()
		return nil
	}
	r.mu.Lock()
	changes = append(slices.Clone(r.issued), changes...)
	r.mu.Unlock()
	if len(changes) > 0 {
		refusal.Changes = changes
		refusal.Paths = nil
		for _, c := range changes {
			refusal.Paths = append(refusal.Paths, c.Path)
		}
		refusal.Description = ""
	}
	r.markRefused()
	return &refusal
}

func (r *configWriteRehearsal) markRefused() {
	r.mu.Lock()
	r.refused = true
	r.mu.Unlock()
}

func (r *configWriteRehearsal) markWroteSettings() {
	r.mu.Lock()
	r.wroteSettings = true
	r.mu.Unlock()
}

func (r *configWriteRehearsal) didWriteSettings() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wroteSettings && r.refusal == nil
}

// StateRepoWriteSyncer is what a StateRepoManager implements when it can
// export a direct UI/API settings write immediately to grain/config
// (pkg/daemon's StateManager does).
type StateRepoWriteSyncer interface {
	SyncAfterWrite(ctx context.Context) error
}

func (c *Client) syncStateRepoAfterWrite(ctx context.Context) {
	if c == nil || c.Config.StateRepo == nil {
		return
	}
	if syncer, ok := c.Config.StateRepo.(StateRepoWriteSyncer); ok {
		if err := syncer.SyncAfterWrite(ctx); err != nil {
			log.Printf("grain api: syncing config repository after settings write: %v", err)
		}
	}
}

// settingsWritable reports whether this deployment takes configuration
// writes, for a caller that only wants to know: unlike
// checkSettingsWritable it arms nothing. It answers for a direct write
// made on the caller's own behalf (a bootstrap convenience such as
// github_tokens.go's default credential), which only an external config
// repository refuses.
func (c *Client) settingsWritable(ctx context.Context) bool {
	remote, _ := c.externalConfigRepo(ctx)
	return remote == ""
}

// checkSettingsWritable guards a write of non-bootstrap configuration
// to tables. The config repository is the only write path for such
// configuration (design/design/config-repo-settings.design.md, section 2.2): an
// external one, and -- for a request that came through the HTTP API --
// this deployment's own grain/config too. It refuses the write at once,
// with the files those tables are exported to, when the request carries
// no rehearsal; otherwise it arms the request's rehearsal, so the
// refusal comes from the route's own write with the exact edit attached.
//
// A deployment with no config repository at all (`grain demo`, a UI
// serving a store it does not manage) and a write made by grain's own
// code rather than an API caller still write directly: there is no
// pull request for either to open.
func (c *Client) checkSettingsWritable(ctx context.Context, tables ...string) error {
	return c.guardConfigWrite(ctx, true, false, tables)
}

// checkSettingsWritableUnrehearsed is checkSettingsWritable for a route
// that must not run past the guard when refused (one with side effects
// outside the store, such as setting a secret, before its write).
func (c *Client) checkSettingsWritableUnrehearsed(ctx context.Context, tables ...string) error {
	return c.guardConfigWrite(ctx, false, false, tables)
}

// checkBootstrapWritable guards a write of bootstrap configuration --
// Models, Sandboxes, credentials, Access -- which stays writable from the
// UI in internal mode, and is exported to grain/config as before. Only
// an external config repository refuses it.
func (c *Client) checkBootstrapWritable(ctx context.Context, tables ...string) error {
	return c.guardConfigWrite(ctx, true, true, tables)
}

// checkBootstrapWritableUnrehearsed is checkBootstrapWritable for a
// route that must not run past the guard when refused.
func (c *Client) checkBootstrapWritableUnrehearsed(ctx context.Context, tables ...string) error {
	return c.guardConfigWrite(ctx, false, true, tables)
}

// checkGrainConfigWritable guards a write of the deployment's own row
// that may change only bootstrap fields (PUT /api/settings, PUT
// /api/deployment): before is the row as it stands and after the row the
// request would write, or nil when the request did not get as far as
// saying (it failed validation), in which case only an external config
// repository refuses it, leaving the validation error to answer.
func (c *Client) checkGrainConfigWritable(ctx context.Context, before, after *model.Config) error {
	if before != nil && after != nil && before.SettingsLocked && !after.SettingsLocked {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
			return c.checkSettingsWritable(ctx, "grain_config")
		}
	}
	if after == nil || before == nil || before.ConnectionsOnly() || bootstrapOnlyChange(*before, *after) {
		return c.checkBootstrapWritable(ctx, "grain_config")
	}
	return c.checkSettingsWritable(ctx, "grain_config")
}

// settingsLocked reports whether the deployment has opted into locking
// non-bootstrap settings in the UI and REST API (grain_config.settings_locked).
func (c *Client) settingsLocked(ctx context.Context) bool {
	if c == nil || c.Store == nil {
		return false
	}
	cfg, err := c.Store.GetConfig(ctx)
	return err == nil && cfg != nil && cfg.SettingsLocked
}

// configRepoFor is the config repository a write must go through, or
// ok false when it may be written directly.
func (c *Client) configRepoFor(ctx context.Context, bootstrap bool) (remote, branch string, ok bool) {
	if remote, branch := c.externalConfigRepo(ctx); remote != "" {
		return remote, branch, true
	}
	if bootstrap || c == nil || c.Config.StateRepo == nil || c.directConfigWrites() || !c.settingsLocked(ctx) {
		return "", "", false
	}
	// Only an API caller is sent to the config repository: grain's own
	// code (a merged config change being applied, a daemon loop) writes
	// with a context that carries no rehearsal (Server.serveHTTP).
	if _, api := model.WriteRehearsalFrom(ctx); !api {
		return "", "", false
	}
	return "", "main", true
}

// configRepoOnly is whether the API refuses a non-bootstrap settings
// write in favour of a config pull request -- configRepoFor's answer for
// a request that came through the API, asked without one.
func (c *Client) configRepoOnly(ctx context.Context) bool {
	if c == nil {
		return false
	}
	if remote, _ := c.externalConfigRepo(ctx); remote != "" {
		return true
	}
	return c.Config.StateRepo != nil && !c.directConfigWrites() && c.settingsLocked(ctx)
}

// rowConfigRepoOnly is configRepoOnly for cfg rather than the row in the
// store: what a settings response answers with the row it just wrote.
func (c *Client) rowConfigRepoOnly(ctx context.Context, cfg model.Config) bool {
	if c == nil {
		return false
	}
	if remote, _ := c.externalConfigRepo(ctx); remote != "" {
		return true
	}
	return c.Config.StateRepo != nil && cfg.SettingsLocked && !c.directConfigWrites()
}

// directConfigWrites is the host-local opt-out from the config
// repository write path in internal mode (staterepo.Settings), read on
// every guarded write so an operator's edit to state-repo.json takes
// effect without a restart.
func (c *Client) directConfigWrites() bool {
	if c.Config.DataDir == "" {
		return false
	}
	s, err := staterepo.LoadSettings(c.Config.DataDir)
	return err == nil && s.DirectConfigWrites
}

func (c *Client) guardConfigWrite(ctx context.Context, rehearse, bootstrap bool, tables []string) error {
	remote, branch, managed := c.configRepoFor(ctx, bootstrap)
	if !managed {
		if r, ok := model.WriteRehearsalFrom(ctx); ok {
			if cr, ok := r.(*configWriteRehearsal); ok {
				cr.markWroteSettings()
			}
		}
		return nil
	}
	refusal := c.configRefusal(ctx, remote, branch, tables)
	if rehearse && c.Store != nil {
		if r, ok := model.WriteRehearsalFrom(ctx); ok {
			if cr, ok := r.(*configWriteRehearsal); ok {
				cr.arm(refusal)
				return nil
			}
		}
	}
	return refusal
}

// configRefusalWriter holds a request whose configuration write was
// armed (checkSettingsWritable) to the one answer external mode allows:
// the refusal. A guarded route that stops before its rehearsed write --
// an unknown id, a body that fails validation, a no-op -- would
// otherwise answer with its own 404, 400 or 200, and a deployment
// managed by an external config repository has always refused every
// configuration write with 409 whatever was wrong with it. Such a
// response is swallowed and the refusal (with the paths and
// description the guard computed, there being no rehearsed edit) sent
// in its place.
type configRefusalWriter struct {
	http.ResponseWriter
	rehearsal *configWriteRehearsal
	onSuccess func()
	decided   bool
	swallow   bool
}

func (w *configRefusalWriter) WriteHeader(status int) {
	if w.decided {
		if !w.swallow {
			w.ResponseWriter.WriteHeader(status)
		}
		return
	}
	w.decided = true
	if w.rehearsal.pending() != nil {
		w.swallow = true
		return
	}
	if status < http.StatusMultipleChoices && w.rehearsal.didWriteSettings() && w.onSuccess != nil {
		onSuccess := w.onSuccess
		w.onSuccess = nil
		onSuccess()
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *configRefusalWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.swallow {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *configRefusalWriter) Flush() {
	if w.swallow {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.decided = true
		f.Flush()
	}
}

func (w *configRefusalWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.decided = true
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *configRefusalWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish sends the refusal the request still owes, if the route never
// sent it.
func (w *configRefusalWriter) finish() {
	if !w.swallow && w.decided {
		return
	}
	if e := w.rehearsal.pending(); e != nil {
		writeClientError(w.ResponseWriter, e)
	}
}
