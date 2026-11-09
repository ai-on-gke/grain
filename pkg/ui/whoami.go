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
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// WirePrincipal is a model.Principal in the shape the API speaks
// (operator-attribution spec §1.2): the stored triple plus the label
// PrincipalLabel gives it, so every reader says the same thing.
type WirePrincipal struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Source string `json:"source"`
	Label  string `json:"label"`
}

func wirePrincipal(p model.Principal) WirePrincipal {
	return WirePrincipal{Kind: string(p.Kind), ID: p.ID, Source: string(p.Source), Label: PrincipalLabel(p, nil)}
}

// WhoamiResponse is GET /api/whoami's body (spec §5.2).
type WhoamiResponse struct {
	Actor      WirePrincipal  `json:"actor"`
	OnBehalfOf *WirePrincipal `json:"onBehalfOf"`
	Via        string         `json:"via,omitempty"`
	// Source duplicates Actor.Source for the UI's convenience.
	Source string `json:"source"`
	// Verified is model.Principal.Verified for Actor (spec §1.1.4).
	Verified bool `json:"verified"`
	// Allowed is whether the caller passed the allowlist. It is always
	// true here, since a caller that is not gets the gate's 403 first; it
	// is in the body so a future read-only role can say false without
	// changing the shape.
	Allowed bool `json:"allowed"`
}

// whoami is who the request in r acts as, as GET /api/whoami reports it.
func whoami(r *http.Request) WhoamiResponse {
	who := RequestActor(r.Context())
	actor := wirePrincipal(who.Actor)
	if who.OnBehalfOf != nil {
		actor.Label = PrincipalLabel(who.Actor, who.OnBehalfOf)
	}
	resp := WhoamiResponse{
		Actor:    actor,
		Source:   string(who.Actor.Source),
		Verified: who.Actor.Verified(),
		Allowed:  true,
	}
	if b := who.OnBehalfOf; b != nil {
		w := wirePrincipal(*b)
		resp.OnBehalfOf = &w
	}
	if del := delegatedIdentityFromContext(r.Context()); del != nil && who.Actor.Kind == model.PrincipalService && who.Actor.Source == model.SourceHub {
		resp.Via = del.ViaLabel
	}
	return resp
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	resp := whoami(r)
	if s.tasks != nil && s.tasks.Store != nil && resp.Actor.Kind == string(model.PrincipalAgent) && resp.Actor.Source == string(model.SourceSession) {
		who := RequestActor(r.Context())
		resp.Actor.Label = sessionLabel(who.Actor, who.OnBehalfOf, s.tasks.sessionTitles(r.Context()))
	}
	writeJSON(w, http.StatusOK, resp)
}

// Who is how c's author reads in a transcript: its AuthorLabel, and when
// grain relayed somebody else's words that the label does not already
// name -- an agent's question, say -- whose they were. A comment from
// before labels existed falls back to the raw author and on-behalf-of.
func (c Comment) Who() string {
	who, behalf := c.AuthorLabel, c.OnBehalfOfLabel
	if who == "" {
		who, behalf = c.Author, c.OnBehalfOf
	}
	if behalf != "" && !strings.HasSuffix(who, " for "+behalf) && !strings.HasPrefix(who, behalf+" (via ") {
		who += " on behalf of " + behalf
	}
	return who
}

// ConfigAuditView is one config_audit row on the wire (operator-attribution
// spec §4.4): what changed, when, and who changed it, labelled.
type ConfigAuditView struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Actor       string    `json:"actor"`
	ActorKind   string    `json:"actorKind"`
	ActorSource string    `json:"actorSource,omitempty"`
	ActorLabel  string    `json:"actorLabel"`
	Surface     string    `json:"surface"`
	Summary     string    `json:"summary"`
}

// maxConfigAudit bounds one read of GET /api/deployment/config-audit.
const maxConfigAudit = 500

func (s *Server) handleGetConfigAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	fetchLimit := maxConfigAudit
	if !isAgentListCaller(r) && !q.Has("cursor") && !q.Has("q") {
		list.Paged = false
		fetchLimit = 100
		if q.Has("limit") {
			if list.Limit == 0 {
				fetchLimit = maxConfigAudit
			} else {
				fetchLimit = min(list.Limit, maxConfigAudit)
			}
		}
	}
	entries, err := s.tasks.Store.ConfigAudit(r.Context(), fetchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	title := s.tasks.sessionTitles(r.Context())
	out := make([]ConfigAuditView, 0, len(entries))
	for _, e := range entries {
		view := ConfigAuditView{
			ID:          e.ID,
			At:          e.At,
			Actor:       e.By.ID,
			ActorKind:   string(e.By.Kind),
			ActorSource: string(e.By.Source),
			ActorLabel:  sessionLabel(e.By, e.OnBehalfOf, title),
			Surface:     e.Surface,
			Summary:     e.Summary,
		}
		if list.matches(view.Actor, view.ActorLabel, view.Surface, view.Summary) {
			out = append(out, view)
		}
	}
	writeList(w, out, list)
}
