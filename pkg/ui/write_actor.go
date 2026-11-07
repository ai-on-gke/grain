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
	"log"
	"net/http"
	"reflect"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// accessCodeWriter is who a configuration write is recorded as made by
// when its request carried no principal grain can name. ServeHTTP lets
// such a request in only as the access code's holder or an in-process
// hop, and the access code is the one of those a person is behind.
const accessCodeWriter = "the access code"

// writerName is who ctx's configuration write is recorded as made by,
// in the daemon log and the state repository's commit: the verified
// human or service account it carries (as requestUser names them), an
// agent principal by its ID, and accessCodeWriter otherwise. Before
// this, a settings save left no trace of who made it at all -- comments
// and closes are attributed, a deployment setting was not (the
// 2026-11-02 verified-identity walkthrough, O1).
func writerName(ctx context.Context) string {
	p, ok := PrincipalFromContext(ctx)
	id := strings.TrimSpace(p.ID)
	switch {
	case !ok || id == "":
		return accessCodeWriter
	case p.Kind == model.PrincipalHuman || p.Kind == model.PrincipalService:
		return id
	case p.Kind == model.PrincipalAgent:
		return "agent " + id
	}
	return accessCodeWriter
}

// writeCommitNote is the state repository commit message for the export
// a successful API write triggers (syncStateRepoAfterWrite): it names
// who made the write, which the diff alone cannot say. The export is
// of the whole database, so the body says what else it may carry.
func writeCommitNote(ctx context.Context) string {
	by := writerName(ctx)
	return "Update grain state after a write by " + by + "\n\n" +
		"Exported straight after " + by + " changed this deployment's\n" +
		"configuration through grain's API. Anything else written since the\n" +
		"previous export is in this commit too."
}

// recordConfigWrite appends the config_audit row for r, a configuration
// write that has just succeeded (operator-attribution spec §4.4): who
// made it, as RequestActor says, and what it changed, as the route it
// came by and the path it named. It runs where the state repository's
// export is triggered (ServeHTTP's onSuccess), so it covers every write
// guardConfigWrite let through -- settings, a repo's settings, a
// schedule, a template, a session -- without each handler having to
// remember. The summary is the method and path, plus the caller's own
// X-Grain-Acting-Reason in parentheses when it gave one (on one line),
// never the body: a body can carry a secret, and the state repository's
// diff has the values.
//
// ctx is the context the write was made under, r the request it came in.
func (c *Client) recordConfigWrite(ctx context.Context, r *http.Request) {
	if c == nil || c.Store == nil || r == nil {
		return
	}
	who := RequestActor(ctx)
	surface := configAuditSurface(r)
	summary := r.Method + " " + r.URL.Path
	if reason := strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(r.Header.Get(ActingReasonHeader))); reason != "" {
		summary += " (" + reason + ")"
	}
	log.Printf("grain api: %s (%s) changed %s: %s",
		PrincipalLabel(who.Actor, who.OnBehalfOf), sourceName(who.Actor.Source), surface, summary)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: c.now(), By: who.Actor, OnBehalfOf: who.OnBehalfOf, Surface: surface, Summary: summary,
	}); err != nil {
		log.Printf("grain api: recording %s's change to %s in the config audit log: %v", who.Actor.ID, surface, err)
	}
}

// configAuditSurface is what a configuration write changed, for its
// config_audit row: `repo:<owner/name>` for a route under a repository,
// `<kind>:<id>` for one naming a single schedule, template, session and
// so on, and otherwise the route's first path segment (`settings`,
// `capability-policy`, ...).
func configAuditSurface(r *http.Request) string {
	if owner, name := r.PathValue("owner"), r.PathValue("name"); owner != "" && name != "" {
		return "repo:" + owner + "/" + name
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/")
	kind, _, _ := strings.Cut(rest, "/")
	if kind == "" {
		kind = "settings"
	}
	for _, key := range []string{"id", "backendId", "name", "cred", "framework", "secret"} {
		if v := r.PathValue(key); v != "" {
			return strings.TrimSuffix(kind, "s") + ":" + v
		}
	}
	return kind
}

// sourceName is s for a log line, saying so when there is none.
func sourceName(s model.AuthSource) string {
	if s == "" {
		return "no source"
	}
	return string(s)
}

// changedConfigFields names the fields of model.Config that differ
// between before and after, in declaration order. Names only: a value
// can be a connection's credential reference or a URL with userinfo,
// and the log line this feeds is read by more people than the values
// are meant for. The state repository's diff has the values.
func changedConfigFields(before *model.Config, after model.Config) []string {
	var was model.Config
	if before != nil {
		was = *before
	}
	b, a := reflect.ValueOf(was), reflect.ValueOf(after)
	var changed []string
	for i := 0; i < a.NumField(); i++ {
		f := a.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			changed = append(changed, f.Name)
		}
	}
	return changed
}
