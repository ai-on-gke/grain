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
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// DeclarationAcceptanceView is one declaration's record on Settings ->
// Declarations (model.DeclarationAcceptance): what it last asked for that
// only a person may grant, what a person accepted, and whether it is
// awaiting acceptance.
type DeclarationAcceptanceView struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Repo            string     `json:"repo,omitempty"`
	Requested       []string   `json:"requested"`
	RequestedCommit string     `json:"requestedCommit,omitempty"`
	RequestedAt     time.Time  `json:"requestedAt"`
	Accepted        []string   `json:"accepted,omitempty"`
	AcceptedBy      string     `json:"acceptedBy,omitempty"`
	AcceptedScopes  []string   `json:"acceptedScopes,omitempty"`
	AcceptedCommit  string     `json:"acceptedCommit,omitempty"`
	AcceptedAt      *time.Time `json:"acceptedAt,omitempty"`
	// Awaiting is true when Requested reaches past Accepted: the
	// declaration's work is held until a person accepts, and Unaccepted
	// is what they would be accepting beyond what they already have.
	Awaiting   bool     `json:"awaiting"`
	Unaccepted []string `json:"unaccepted,omitempty"`
	// NeedsScopes is the scopes a person must hold to accept Requested
	// (model.DeclaredItemScopes).
	NeedsScopes []string `json:"needsScopes"`
}

// AcceptDeclarationRequest is POST /api/declarations/accept's body: the
// declaration, and the set the person was shown (model.ErrDeclarationMoved).
type AcceptDeclarationRequest struct {
	ID   string   `json:"id"`
	Seen []string `json:"seen"`
}

func declarationAcceptanceView(a model.DeclarationAcceptance) DeclarationAcceptanceView {
	v := DeclarationAcceptanceView{
		ID: a.ID, Kind: string(a.Kind), Requested: a.Requested, RequestedCommit: a.RequestedCommit,
		RequestedAt: a.RequestedAt, Accepted: a.Accepted, AcceptedBy: a.AcceptedBy, AcceptedScopes: a.AcceptedScopes,
		AcceptedCommit: a.AcceptedCommit, AcceptedAt: a.AcceptedAt, Awaiting: a.Awaiting(), Unaccepted: a.Unaccepted(),
		NeedsScopes: declarationNeedsScopes(a.Requested),
	}
	if a.Repo.Name != "" {
		v.Repo = a.Repo.String()
	}
	if v.Requested == nil {
		v.Requested = []string{}
	}
	return v
}

// declarationNeedsScopes is every scope accepting items takes, sorted,
// each once.
func declarationNeedsScopes(items []string) []string {
	var out []string
	for _, it := range items {
		for _, sc := range model.DeclaredItemScopes(it) {
			if !slices.Contains(out, string(sc)) {
				out = append(out, string(sc))
			}
		}
	}
	slices.Sort(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// DeclarationAcceptances lists every declaration that has asked for
// something only a person may grant, those awaiting acceptance first.
func (c *Client) DeclarationAcceptances(ctx context.Context) ([]DeclarationAcceptanceView, error) {
	rows, err := c.Store.ListDeclarationAcceptances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DeclarationAcceptanceView, 0, len(rows))
	for _, a := range rows {
		out = append(out, declarationAcceptanceView(a))
	}
	return out, nil
}

// declarationAcceptanceRefusal refuses an acceptance by anybody but a
// person acting directly, or by a person whose role does not hold every
// scope the set needs: accepting is granting, so it takes what granting
// each item to a task would. An unlimited session, the deployment's
// owner holding the access code, holds every scope.
func (c *Client) declarationAcceptanceRefusal(ctx context.Context, needs []string) error {
	by := c.requestActor(ctx).Actor
	if by.Kind != model.PrincipalHuman || isDelegatedRequest(ctx) {
		return forbiddenErrorf("only a person acting directly may accept what a repository's declaration asks for: " +
			"a declaration is not a person's grant, and accepting it is one")
	}
	return declarationScopeRefusal(ctx, needs)
}

// declarationScopeRefusal is declarationAcceptanceRefusal's half about
// scopes: the calling role must hold every one of needs.
func declarationScopeRefusal(ctx context.Context, needs []string) error {
	role := accessRoleFromContext(ctx)
	if role == nil || role.Full {
		return nil
	}
	var missing []string
	for _, sc := range needs {
		if !role.allows(APIScope(sc), "") {
			missing = append(missing, sc)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return forbiddenErrorf("%s cannot accept this declaration: it asks for what %s governs, and the role does not hold it. "+
		"Ask a person holding %s to accept it (docs/access-code.md)",
		role.label(), strings.Join(missing, " and "), strings.Join(missing, " and "))
}

// AcceptDeclaration records the calling person's acceptance of what
// declaration id last asked for, seen being the set they were shown.
// Once it is recorded the declaration's work lands approved for as long as
// it asks for no more (model.DeclarationAcceptance).
func (c *Client) AcceptDeclaration(ctx context.Context, id string, seen []string) (DeclarationAcceptanceView, error) {
	id = strings.TrimSpace(id)
	acc, err := c.Store.GetDeclarationAcceptance(ctx, id)
	if err != nil {
		return DeclarationAcceptanceView{}, err
	}
	if acc == nil {
		return DeclarationAcceptanceView{}, &NotFoundError{ID: id}
	}
	if seen == nil {
		seen = []string{}
	}
	// The store accepts whatever the declaration asks for inside its own
	// transaction, refusing it unless that is seen; the scopes are
	// checked against seen too, so a declaration widened between this
	// read and that one cannot be accepted by a person whose role only
	// held what the narrower set needed.
	needs := declarationNeedsScopes(append(slices.Clone(acc.Requested), seen...))
	if err := c.declarationAcceptanceRefusal(ctx, needs); err != nil {
		return DeclarationAcceptanceView{}, err
	}
	by := c.requestActor(ctx).Actor.ID
	got, err := c.Store.AcceptDeclaration(ctx, id, by, needs, seen, c.now())
	if errors.Is(err, model.ErrDeclarationMoved) {
		return DeclarationAcceptanceView{}, validationErrorf("%s now asks for %s: reload and accept what it asks for now",
			id, strings.Join(acc.Requested, ", "))
	}
	if err != nil {
		return DeclarationAcceptanceView{}, err
	}
	return declarationAcceptanceView(got), nil
}

func (s *Server) handleListDeclarationAcceptances(w http.ResponseWriter, r *http.Request) {
	rows, err := s.tasks.DeclarationAcceptances(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAcceptDeclaration(w http.ResponseWriter, r *http.Request) {
	var req AcceptDeclarationRequest
	if !readJSON(w, r, &req) {
		return
	}
	got, err := s.tasks.AcceptDeclaration(r.Context(), req.ID, req.Seen)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
