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
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Granting capability methods with arguments from the UI
// (docs/capabilities.md, "Granting from the UI"). A capability server's
// methods each declare their with: arguments as JSON Schema
// (inputSchema.properties.args, docs/capabilities-spec.md §4.1.3); the
// pickers render a form from that schema, and these routes are the
// server side of it: the methods and their schemas, a grant checked and
// normalized exactly as task creation does it, with each refusal pinned to
// the argument it is about, and SetCapability's attach/detach widened from
// a bare id to a {capability, with} grant.

// CapabilityMethod is one attached server's method as a picker offers it.
type CapabilityMethod struct {
	// Ref is <capability_id>#<method>, what a grant names.
	Ref         string `json:"ref"`
	Capability  string `json:"capability"`
	Method      string `json:"method"`
	Description string `json:"description,omitempty"`
	// Args is the method's args schema; nil for one taking no arguments.
	Args map[string]any `json:"args,omitempty"`
	// RequiresArgs is {} failing the schema once defaults are applied: a
	// grant of this method must name arguments (§2.2.1).
	RequiresArgs bool `json:"requiresArgs"`
	Releasable   bool `json:"releasable"`
	// Policy is the capability policy's verdict on this method for each
	// run kind it restricts (model.CapabilityRunTask,
	// model.CapabilityRunProcess, model.CapabilityRunCI). Omitted when
	// every kind is admitted unconditionally.
	Policy map[string]model.CapabilityPolicyVerdict `json:"policy,omitempty"`
	// PolicyRefusal, on a listing asked for one run (?repo=&kind=), is
	// why the capability policy (§4.2.2) denies this method to that run;
	// empty where it admits it, and on a listing that names no run.
	PolicyRefusal string `json:"policyRefusal,omitempty"`
}

// ListCapabilityMethods is every method of every attached capability
// server, sorted by server and then method. A server that is configured
// but not attached offers nothing: a grant of it could not be normalized.
func (c *Client) ListCapabilityMethods(ctx context.Context) []CapabilityMethod {
	out := []CapabilityMethod{}
	if c.Config.CapabilityServers == nil {
		return out
	}
	var policy model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	if c.Store != nil {
		if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
			policy = cfg.CapabilityPolicy
			configured = cfg.MCPServers
		}
	}
	configured, _ = intree.EnsureBuiltins(configured)
	for _, s := range c.Config.CapabilityServers.Statuses() {
		for _, m := range s.MethodDetails {
			ref := s.ID + "#" + m.Name
			out = append(out, CapabilityMethod{
				Ref:          ref,
				Capability:   s.ID,
				Method:       m.Name,
				Description:  m.Description,
				Args:         m.Args,
				RequiresArgs: !protocol.ArgsHaveNoRequirement(m.Args),
				Releasable:   m.Releasable,
				Policy:       policy.Verdicts(ref, configured),
			})
		}
	}
	return out
}

// GrantRequest is one grant as the UI writes it: a reference and its
// with: arguments, or (Grant) a grant string already in §2.2.3's
// <ref>=<json> form, as a task records it.
type GrantRequest struct {
	Capability string         `json:"capability,omitempty"`
	With       map[string]any `json:"with,omitempty"`
	Grant      string         `json:"grant,omitempty"`
}

// grantString is req as a grant string.
func (req GrantRequest) grantString() (string, error) {
	if s := strings.TrimSpace(req.Grant); s != "" {
		if req.Capability != "" || req.With != nil {
			return "", validationErrorf("name either grant, or capability and with, not both")
		}
		return s, nil
	}
	ref := strings.TrimSpace(req.Capability)
	if ref == "" {
		return "", validationErrorf("capability is required")
	}
	if req.With != nil && !model.IsCapabilityRef(ref) {
		return "", validationErrorf("capability %s takes no with: arguments; only a capability server's method does", ref)
	}
	s, err := model.EncodeCapabilityGrant(protocol.GrantSpec{Capability: ref, With: req.With})
	if err != nil {
		return "", validationErrorf("%v", err)
	}
	return s, nil
}

// GrantCheck is one grant checked against its server's current schema.
type GrantCheck struct {
	// Grant is the grant string as asked about.
	Grant string `json:"grant"`
	// Recorded is the grant normalized (§2.3.1): what a task would
	// record. Empty when it does not validate.
	Recorded string `json:"recorded,omitempty"`
	// Args are Recorded's normalized arguments.
	Args map[string]any `json:"args,omitempty"`
	// Error is why it does not validate, and Fields the same refusal
	// keyed by the argument path it names ("account", "scopes[1]";
	// "" for the arguments as a whole).
	Error  string            `json:"error,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	// Changed is a grant already in recorded form that normalizes to
	// something else under the server's current schema -- a default
	// added or changed since it was recorded. It is reported, not
	// rewritten: the task keeps what it was granted until someone
	// edits it (§2.3.1, §2.3.4).
	Changed bool `json:"changed,omitempty"`
	// Unchecked is a reference whose server is not attached here, so
	// only its syntax could be checked.
	Unchecked bool `json:"unchecked,omitempty"`
	// CatalogueRefusal is why settings/capabilities.yml refuses the
	// grant to the run the check named (checkGrantsRequest.Subject):
	// filing a task holding it would be refused, or the grant dropped
	// when it is only a default. Empty when no subject was named.
	CatalogueRefusal string `json:"catalogueRefusal,omitempty"`
}

// CheckCapabilityGrants normalizes each grant against its server, the
// same normalization task creation and SetCapability apply, without
// writing anything.
func (c *Client) CheckCapabilityGrants(ctx context.Context, grants []GrantRequest) ([]GrantCheck, error) {
	lookup := c.capabilityLookup()
	out := make([]GrantCheck, 0, len(grants))
	for _, req := range grants {
		s, err := req.grantString()
		if err != nil {
			return nil, err
		}
		check := GrantCheck{Grant: s}
		if !model.IsCapabilityRef(s) {
			if _, ok := c.capabilityByID(s); !ok {
				check.Error = capabilityRefusal(s)
			} else {
				check.Recorded = s
			}
			out = append(out, check)
			continue
		}
		if lookup != nil {
			if _, err := lookup(protocolCapabilityID(s)); err != nil {
				check.Unchecked = true
			}
		} else {
			check.Unchecked = true
		}
		if check.Unchecked {
			if err := model.CheckCapabilityGrantSyntax(s); err != nil {
				check.Error, check.Fields = err.Error(), grantFieldErrors(err)
			} else {
				check.Recorded = s
			}
			out = append(out, check)
			continue
		}
		names, err := model.NormalizeCapabilityNames([]string{s}, lookup)
		if err != nil {
			check.Error, check.Fields = err.Error(), grantFieldErrors(err)
			out = append(out, check)
			continue
		}
		check.Recorded = names[0]
		if spec, err := model.CapabilityGrantSpec(check.Recorded); err == nil {
			check.Args = spec.With
		}
		// Only a grant handed back as a task recorded it can have changed;
		// {capability, with} is a new grant being written.
		if ref, err := protocol.ParseRef(model.CapabilityGrantRef(s)); err == nil && req.Grant != "" && ref.Method != "" && strings.Contains(s, "=") {
			check.Changed = check.Recorded != s
		}
		out = append(out, check)
	}
	return out, nil
}

// protocolCapabilityID is the capability_id a grant string names, without
// its #method and arguments.
func protocolCapabilityID(grant string) string {
	ref := model.CapabilityGrantRef(grant)
	if r, err := protocol.ParseRef(ref); err == nil {
		return r.CapabilityID
	}
	id, _, _ := strings.Cut(ref, "#")
	return id
}

// argsErrorPath finds the argument path in a schema refusal:
// ValidateArgs names it as args, args.<name> or args.<name>[<i>].
var argsErrorPath = regexp.MustCompile(`(?:^|[\s:(])args((?:\.[A-Za-z0-9_$\-]+|\[\d+\])*): (.*)$`)

// grantFieldErrors keys a normalization refusal by the argument it names,
// or returns nil when it names none.
func grantFieldErrors(err error) map[string]string {
	m := argsErrorPath.FindStringSubmatch(err.Error())
	if m == nil {
		return nil
	}
	return map[string]string{strings.TrimPrefix(m[1], "."): m[2]}
}

// grantValidationError is a normalization refusal as a ValidationError
// that also carries its field errors.
func grantValidationError(err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		if ve.fields == nil {
			ve.fields = grantFieldErrors(ve.err)
		}
		return ve
	}
	return &ValidationError{err: err, fields: grantFieldErrors(err)}
}

// SetCapabilityGrant attaches or detaches one grant (SetCapability for a
// GrantRequest). On attach, replace names a grant the task already holds
// that the new one takes the place of -- a chip edited in place -- so the
// edit is one write and the grant keeps its position. Equal grants are
// one grant (§2.3.2): attaching one the task already holds leaves a
// single copy.
func (c *Client) SetCapabilityGrant(ctx context.Context, id string, req GrantRequest, attach bool, replace string) error {
	grant, err := req.grantString()
	if err != nil {
		return err
	}
	replace = strings.TrimSpace(replace)
	if !attach || replace == "" {
		return c.SetCapability(ctx, id, grant, attach)
	}
	normalized, err := c.normalizeCapabilityID(grant)
	if err != nil {
		return grantValidationError(err)
	}
	if _, ok := c.capabilityByID(normalized); !ok {
		return validationErrorf("%s", capabilityRefusal(normalized))
	}
	if err := c.catalogueEditRefusalFor(ctx, id, normalized); err != nil {
		return err
	}
	return c.mutate(ctx, id, func(task *model.Task) error {
		kept := make([]model.Grant, 0, len(task.Grants)+1)
		placed := false
		for _, g := range task.Grants {
			switch {
			case g.Capability == replace && !placed:
				g.Capability, g.Via = normalized, model.GrantByLabel
				kept = append(kept, g)
				placed = true
			case g.Capability == replace, g.Capability == normalized:
			default:
				kept = append(kept, g)
			}
		}
		if !placed {
			kept = append(kept, model.Grant{Capability: normalized, Via: model.GrantByLabel})
		}
		task.Grants = kept
		return nil
	})
}

type checkGrantsRequest struct {
	Grants []GrantRequest `json:"grants"`
	// Subject, when set, is the task the grants are for, and each is
	// also judged against the capability catalogue for it
	// (GrantCheck.CatalogueRefusal): what the new-task form marks
	// against its pre-ticked defaults before anything is filed.
	Subject *grantCheckSubject `json:"subject,omitempty"`
}

// grantCheckSubject is the task a grant check is for. Repo is its
// target, owner/name; "" is a task with no repository.
type grantCheckSubject struct {
	Repo string `json:"repo"`
}

// handleListCapabilityMethods is GET /api/capability-methods. With
// ?kind= (task, process or ci) and optionally ?repo=owner/name, each
// method also says whether the capability policy denies it to that run
// (CapabilityMethod.PolicyRefusal), which is what a picker for a task on
// that repository offers it by.
func (s *Server) handleListCapabilityMethods(w http.ResponseWriter, r *http.Request) {
	methods := s.tasks.ListCapabilityMethods(r.Context())
	q := r.URL.Query()
	if kind := strings.TrimSpace(q.Get("kind")); kind != "" {
		switch kind {
		case model.CapabilityRunTask, model.CapabilityRunProcess, model.CapabilityRunCI:
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("kind %q is not task, process or ci", kind))
			return
		}
		subject := model.CapabilitySubject{Kind: kind}
		if repo := strings.TrimSpace(q.Get("repo")); repo != "" {
			ref, err := model.ParseRepo(repo)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("repo %q: %w", repo, err))
				return
			}
			subject.Repo = &ref
		}
		marked, err := s.tasks.markPolicyRefusals(r.Context(), methods, subject)
		if err != nil {
			writeClientError(w, err)
			return
		}
		methods = marked
	}
	writeJSON(w, http.StatusOK, methods)
}

func (s *Server) handleCheckCapabilityGrants(w http.ResponseWriter, r *http.Request) {
	var req checkGrantsRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.CheckCapabilityGrants(r.Context(), req.Grants)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if req.Subject != nil {
		var target *model.RepoRef
		if repo := strings.TrimSpace(req.Subject.Repo); repo != "" {
			ref, err := model.ParseRepo(repo)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("subject repo %q: %w", repo, err))
				return
			}
			target = &ref
		}
		if out, err = s.tasks.markCatalogueRefusals(r.Context(), out, target); err != nil {
			writeClientError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}
