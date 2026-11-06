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

// Deleting a hostd from the registry (DELETE /api/hostds/{name},
// Store.DeleteHostd). Only a revoked hostd is deleted: revoking is what
// cuts a hostd off, and deleting one is only tidying the registry
// afterwards, so it can never drop a connected hostd or its running work
// by surprise.
//
// What still names the hostd is handled in two ways, by what it is:
//
//   - Configuration an operator wrote that names the hostd -- a model
//     connection's hostd or hostdFallback, or a capability server
//     entry's hostd, by name (a label selector names no one hostd) --
//     refuses the delete, listing each one. Clearing it silently would
//     move that connection's runs, or that server, somewhere the
//     operator never chose; the refusal says what to change first.
//   - Runtime state built from the name -- the usage-limit pauses keyed
//     owner@hostd/<name> (daemon's hostdPauseOwner) -- is cleared and
//     reported in the answer. Nobody configures it, and it would
//     otherwise wrongly pause a new hostd registered under the name.
//
// Per-task hostd pins (`task_hostd`, a repository's default_hostd, a
// template's hostd) were dropped by schema 111, so a deployment at this
// build's schema holds none to check.
//
// The run history keeps the name: task_run_hostd and the spool cursors
// are not touched, and an attempt whose recorded hostd is no longer the
// registry's (deleted, or deleted and registered again since) reads it
// as deleted (TaskRunHostd.Deleted).

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/model"
)

// HostdReference is one piece of configuration that names a hostd:
// Kind "connection" (a model connection, Field "hostd" or
// "hostdFallback") or "capability-server" (Field "hostd"), and the ID of
// that connection or entry.
type HostdReference struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Field string `json:"field"`
}

func (r HostdReference) String() string {
	switch r.Kind {
	case "connection":
		return fmt.Sprintf("model connection %q (%s)", r.ID, r.Field)
	case "capability-server":
		return fmt.Sprintf("capability server %q (%s)", r.ID, r.Field)
	}
	return fmt.Sprintf("%s %q (%s)", r.Kind, r.ID, r.Field)
}

// DeletedHostd is the answer to deleting a hostd: its name, and the
// usage-limit pauses keyed to it that were cleared with it.
type DeletedHostd struct {
	Name string `json:"name"`
	// ClearedPauses are the usage-limit pauses scoped to the hostd
	// (owner@hostd/<name>) that were lifted and forgotten, by key.
	ClearedPauses []string `json:"clearedPauses,omitempty"`
}

// SkippedHostd is a revoked hostd DeleteRevokedHostds left in place, and
// why.
type SkippedHostd struct {
	Name       string           `json:"name"`
	Reason     string           `json:"reason"`
	References []HostdReference `json:"references,omitempty"`
}

// DeleteRevokedHostdsResult is POST /api/hostds/delete-revoked's answer.
type DeleteRevokedHostdsResult struct {
	Deleted []DeletedHostd `json:"deleted"`
	Skipped []SkippedHostd `json:"skipped"`
}

// hostdReferenceError is a delete refused because configuration still
// names the hostd.
type hostdReferenceError struct {
	name string
	refs []HostdReference
}

func (e *hostdReferenceError) Error() string {
	parts := make([]string, 0, len(e.refs))
	for _, r := range e.refs {
		parts = append(parts, r.String())
	}
	return fmt.Sprintf("host %s is still named by %s: point them at another host (Settings -> Models, Settings -> Capabilities), then delete it",
		e.name, strings.Join(parts, ", "))
}

// HostdReferences is every model connection and capability server entry
// that names hostd name: by name, in a connection's hostd or
// hostdFallback, or in a capability server entry's hostd. A label
// selector names no one hostd and is not counted.
func (c *Client) HostdReferences(ctx context.Context, name string) ([]HostdReference, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	names := func(selector string) bool {
		n, _, err := model.ParseHostdSelector(selector)
		return err == nil && n == name
	}
	var refs []HostdReference
	for _, b := range cfg.AgentBackends {
		if names(b.Hostd) {
			refs = append(refs, HostdReference{Kind: "connection", ID: b.ID, Field: "hostd"})
		}
		for _, f := range b.HostdFallback {
			if names(f) {
				refs = append(refs, HostdReference{Kind: "connection", ID: b.ID, Field: "hostdFallback"})
				break
			}
		}
	}
	servers, _ := intree.EnsureBuiltins(cfg.MCPServers)
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if names(string(servers[id].Hostd)) {
			refs = append(refs, HostdReference{Kind: "capability-server", ID: id, Field: "hostd"})
		}
	}
	return refs, nil
}

// hostdPauseSuffix is how a usage-limit pause scoped to hostd name ends
// its credential key (the daemon's hostdPauseOwner).
func hostdPauseSuffix(name string) string { return "@hostd/" + name }

// clearHostdPauses lifts and forgets every usage-limit pause scoped to
// hostd name -- in force in the reconcile loop's gate, or only stored --
// and reports their keys.
func (c *Client) clearHostdPauses(ctx context.Context, name string) []string {
	suffix := hostdPauseSuffix(name)
	seen := map[string]bool{}
	var cleared []string
	add := func(key string) {
		if !seen[key] {
			seen[key] = true
			cleared = append(cleared, key)
		}
	}
	if c.Config.AgentPause != nil {
		for _, p := range c.Config.AgentPause.LiftWhere(func(p model.UsageLimitPause) bool {
			return strings.HasSuffix(p.Connection, suffix)
		}) {
			add(pauseLabel(p))
		}
	}
	rows, err := c.Store.ListUsageLimitPauses(ctx)
	if err != nil {
		log.Printf("grain hostd: listing usage-limit pauses to clear hostd %s's: %v", name, err)
	}
	for _, row := range rows {
		if !strings.HasSuffix(row.Pause.Connection, suffix) {
			continue
		}
		if err := c.Store.DeleteUsageLimitPause(ctx, row.Key); err != nil {
			log.Printf("grain hostd: forgetting the usage-limit pause %s: %v", row.Key, err)
			continue
		}
		add(pauseLabel(row.Pause))
	}
	sort.Strings(cleared)
	return cleared
}

// pauseLabel names a pause in DeletedHostd.ClearedPauses: its credential
// key, and the model it covers when it covers one.
func pauseLabel(p model.UsageLimitPause) string {
	if p.Model != "" {
		return p.Connection + " (" + p.Model + ")"
	}
	return p.Connection
}

// DeleteHostd removes a revoked hostd from the registry with every token
// it had. A live hostd is refused ("revoke it first"), as is one that
// configuration still names (HostdReferences) and the co-located local;
// an unknown name is not found. Usage-limit pauses scoped to it are
// cleared and reported.
func (c *Client) DeleteHostd(ctx context.Context, name string) (DeletedHostd, error) {
	if name == model.LocalHostd {
		return DeletedHostd{}, validationErrorf("%q is the co-located host and cannot be deleted", name)
	}
	h, err := c.Store.GetHostd(ctx, name)
	if err != nil {
		return DeletedHostd{}, err
	}
	if h == nil {
		return DeletedHostd{}, &NotFoundError{message: fmt.Sprintf("no host %q in the registry", name)}
	}
	if !h.Revoked() {
		return DeletedHostd{}, validationErrorf("host %s is not revoked: revoke it first, which disconnects it, then delete it", name)
	}
	refs, err := c.HostdReferences(ctx, name)
	if err != nil {
		return DeletedHostd{}, err
	}
	if len(refs) > 0 {
		return DeletedHostd{}, validationErrorf("%v", &hostdReferenceError{name: name, refs: refs})
	}
	if err := c.Store.DeleteHostd(ctx, name); err != nil {
		return DeletedHostd{}, hostdError(err)
	}
	out := DeletedHostd{Name: name, ClearedPauses: c.clearHostdPauses(ctx, name)}
	if _, err := c.Store.ClearNotification(ctx, "hostd-disconnected:"+name, "automation:hostd"); err != nil {
		log.Printf("grain ui: clearing hostd-disconnected:%s after delete: %v", name, err)
	}
	summary := "deleted revoked host " + name + " from the registry"
	if len(out.ClearedPauses) > 0 {
		summary += "; cleared its usage-limit pauses " + strings.Join(out.ClearedPauses, ", ")
	}
	c.auditHostd(ctx, name, summary)
	return out, nil
}

// DeleteRevokedHostds deletes every revoked hostd that nothing still
// names, and lists the ones it skipped.
func (c *Client) DeleteRevokedHostds(ctx context.Context) (DeleteRevokedHostdsResult, error) {
	rows, err := c.Store.ListHostds(ctx)
	if err != nil {
		return DeleteRevokedHostdsResult{}, err
	}
	out := DeleteRevokedHostdsResult{Deleted: []DeletedHostd{}, Skipped: []SkippedHostd{}}
	for _, h := range rows {
		if !h.Revoked() || h.Name == model.LocalHostd {
			continue
		}
		refs, err := c.HostdReferences(ctx, h.Name)
		if err != nil {
			return out, err
		}
		if len(refs) > 0 {
			out.Skipped = append(out.Skipped, SkippedHostd{
				Name: h.Name, Reason: (&hostdReferenceError{name: h.Name, refs: refs}).Error(), References: refs,
			})
			continue
		}
		d, err := c.DeleteHostd(ctx, h.Name)
		if err != nil {
			out.Skipped = append(out.Skipped, SkippedHostd{Name: h.Name, Reason: err.Error()})
			continue
		}
		out.Deleted = append(out.Deleted, d)
	}
	return out, nil
}

// markDeletedRunHostds sets Deleted on each recorded run hostd the
// registry no longer holds as the hostd that run used: its row is gone,
// or the name was registered again after the run. The co-located hostd,
// the deprecated shared token's hostd (never in the registry) and a run
// that recorded no name are left alone. A failure to read the registry
// marks nothing.
func (c *Client) markDeletedRunHostds(ctx context.Context, runs map[string]model.TaskRunHostd) {
	if len(runs) == 0 {
		return
	}
	shared := ""
	if s, ok := c.Config.HostdRemote.(hostdSharedNamer); ok {
		shared = s.SharedTokenName()
	}
	rows, err := c.Store.ListHostds(ctx)
	if err != nil {
		log.Printf("ui: reading the hostd registry to mark deleted hostds: %v", err)
		return
	}
	created := make(map[string]time.Time, len(rows))
	for _, h := range rows {
		created[h.Name] = h.CreatedAt
	}
	for id, r := range runs {
		if r.Hostd == "" || r.Hostd == model.LocalHostd || r.Hostd == shared {
			continue
		}
		at, ok := created[r.Hostd]
		if !ok || (!r.At.IsZero() && at.After(r.At)) {
			r.Deleted = true
			runs[id] = r
		}
	}
}

func (s *Server) handleDeleteHostd(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.DeleteHostd(r.Context(), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: deleted revoked hostd %s from the registry", out.Name)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteRevokedHostds(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.DeleteRevokedHostds(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteHostd is DELETE /api/hostds/{name}.
func (c *HTTPClient) DeleteHostd(ctx context.Context, name string) (DeletedHostd, error) {
	var out DeletedHostd
	err := c.do(ctx, http.MethodDelete, "/api/hostds/"+url.PathEscape(name), nil, &out)
	return out, err
}

// DeleteRevokedHostds is POST /api/hostds/delete-revoked.
func (c *HTTPClient) DeleteRevokedHostds(ctx context.Context) (DeleteRevokedHostdsResult, error) {
	var out DeleteRevokedHostdsResult
	err := c.do(ctx, http.MethodPost, "/api/hostds/delete-revoked", nil, &out)
	return out, err
}
