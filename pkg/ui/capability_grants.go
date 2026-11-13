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
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Granting capability methods with arguments from the UI
// (docs/capabilities.md, "Granting from the UI"). A capability server's
// methods each declare their with: arguments as JSON Schema
// (inputSchema.properties.args, design/spec/capabilities/capabilities.spec.md §4.1.3); the
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
	// AliasOf, when non-empty, names the canonical method Ref(s) this
	// method aliases (for example api#grain/task-admin -> [api#task-admin],
	// which pickers offer once under the unprefixed preset).
	AliasOf []string `json:"aliasOf,omitempty"`
	// DescriptionTruncated and ArgsTruncated say a paged call cut this
	// row to fit an agent's tool-result budget: Description to its first
	// pageRowDescriptionBytes bytes, and each array in Args (an enum of
	// gchat's spaces, say) to its first pageRowArrayItems items, with
	// ArgItemCounts giving each cut array's full length by its path in
	// the schema. Ask with fields=full (and q=<ref>) for the whole row.
	DescriptionTruncated bool           `json:"descriptionTruncated,omitempty"`
	ArgsTruncated        bool           `json:"argsTruncated,omitempty"`
	ArgItemCounts        map[string]int `json:"argItemCounts,omitempty"`
}

// pagedCapabilityMethod is m as a paged row carries it: the first
// pageRowDescriptionBytes of its description and the first
// pageRowArrayItems items of each array in its argument schema. A
// method whose description and schema enumerate a configured list --
// gchat#post names every space -- was 26 KB on its own.
func pagedCapabilityMethod(m CapabilityMethod) CapabilityMethod {
	if desc, cut := truncateText(m.Description, pageRowDescriptionBytes); cut {
		m.Description, m.DescriptionTruncated = desc, true
	}
	if args, cut := capJSONObject(m.Args, pageRowArrayItems); len(cut) > 0 {
		m.Args, m.ArgsTruncated, m.ArgItemCounts = args, true, cut
	}
	return m
}

// ListCapabilityMethods is every method of every attached capability
// server, sorted by server and then method. A server that is configured
// but not attached offers nothing: a grant of it could not be normalized.
func (c *Client) ListCapabilityMethods(ctx context.Context) []CapabilityMethod {
	out := []CapabilityMethod{}
	var policy model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	if c.Store != nil {
		if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
			policy = cfg.CapabilityPolicy
			configured = cfg.MCPServers
		}
	}
	configured, _ = intree.EnsureBuiltins(configured)
	statuses := c.inProcessStatuses()
	if c.Config.CapabilityServers != nil {
		for _, s := range c.Config.CapabilityServers.Statuses() {
			if grainserver.IsInProcessID(s.ID) {
				continue
			}
			statuses = append(statuses, s)
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	for _, s := range statuses {
		for _, m := range s.MethodDetails {
			ref := s.ID + "#" + m.Name
			var aliasOf []string
			if s.ID == grainserver.APIID {
				if unprefixed, ok := strings.CutPrefix(m.Name, model.APIScopeSetBuiltinPrefix); ok {
					if _, isPreset := model.APIPresetNamed(unprefixed); isPreset {
						aliasOf = []string{grainserver.APIID + "#" + unprefixed}
					}
				}
			}
			out = append(out, CapabilityMethod{
				Ref:          ref,
				Capability:   s.ID,
				Method:       m.Name,
				Description:  m.Description,
				Args:         m.Args,
				RequiresArgs: !protocol.ArgsHaveNoRequirement(m.Args),
				Releasable:   m.Releasable,
				Policy:       policy.Verdicts(ref, configured),
				AliasOf:      aliasOf,
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
	// AliasOf is the canonical grant(s) a retired bare capability name
	// became (model.CanonicalLegacyGrants), normalized as a task records
	// them. Empty when Grant is not a retired alias.
	AliasOf []string `json:"aliasOf,omitempty"`
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
		repl := model.CanonicalLegacyGrants(s)
		if len(repl) > 1 {
			check := GrantCheck{Grant: s, AliasOf: make([]string, 0, len(repl))}
			for _, r := range repl {
				rc := c.checkSingleCapabilityGrant(r, "", lookup)
				if rc.Recorded != "" {
					check.AliasOf = append(check.AliasOf, rc.Recorded)
				} else {
					check.AliasOf = append(check.AliasOf, r)
				}
				if rc.Error != "" && check.Error == "" {
					check.Error, check.Fields = rc.Error, rc.Fields
				}
				if rc.Unchecked {
					check.Unchecked = true
				}
			}
			out = append(out, check)
			continue
		}
		isAlias := len(repl) == 1 && repl[0] != s
		target := s
		reqGrant := req.Grant
		if isAlias {
			target = repl[0]
			reqGrant = ""
		}
		check := c.checkSingleCapabilityGrant(target, reqGrant, lookup)
		check.Grant = s
		if isAlias {
			if check.Recorded != "" {
				check.AliasOf = []string{check.Recorded}
			} else {
				check.AliasOf = []string{target}
			}
		}
		out = append(out, check)
	}
	return out, nil
}

func (c *Client) checkSingleCapabilityGrant(s, reqGrant string, lookup protocol.Lookup) GrantCheck {
	check := GrantCheck{Grant: s}
	// grain#ci is a well-formed reference to a registered method, so the
	// checks below would pass it -- and SetCapability would then refuse
	// it (capabilityByID). A check that says yes to what the edit says no
	// to is how request_access relayed a grain#ci nobody could grant
	// (task 4361): refuse it here, in the same words.
	if grainci.IsGrant(s) {
		check.Error = capabilityRefusal(s)
		return check
	}
	if !model.IsCapabilityRef(s) {
		if _, ok := c.capabilityByID(s); !ok {
			check.Error = capabilityRefusal(s)
		} else {
			check.Recorded = s
		}
		return check
	}
	grantLookup := lookup
	if grantLookup == nil && grainserver.IsInProcessGrant(s) {
		grantLookup = grainserver.LookupWithRepoCredentials(nil, c.configuredGitCredentials())
	}
	if grantLookup != nil {
		if _, err := grantLookup(protocolCapabilityID(s)); err != nil {
			check.Unchecked = true
		}
	} else {
		check.Unchecked = true
	}
	if check.Unchecked {
		if err := model.CheckCapabilityGrantArgsOffline(s); err != nil {
			check.Error, check.Fields = err.Error(), grantFieldErrors(err)
		} else if errMsg, fields := postNormalizeGrantRefusal(s); errMsg != "" {
			check.Error, check.Fields = errMsg, fields
		} else {
			check.Recorded = s
		}
		return check
	}
	names, err := model.NormalizeCapabilityNames([]string{s}, grantLookup)
	if err != nil {
		check.Error, check.Fields = err.Error(), grantFieldErrors(err)
		return check
	}
	recorded := names[0]
	if errMsg, fields := postNormalizeGrantRefusal(recorded); errMsg != "" {
		check.Error, check.Fields = errMsg, fields
		return check
	}
	check.Recorded = recorded
	if spec, err := model.CapabilityGrantSpec(check.Recorded); err == nil {
		check.Args = spec.With
	}
	// Only a grant handed back as a task recorded it can have changed;
	// {capability, with} is a new grant being written.
	if ref, err := protocol.ParseRef(model.CapabilityGrantRef(s)); err == nil && reqGrant != "" && ref.Method != "" && strings.Contains(s, "=") {
		check.Changed = check.Recorded != s
	}
	return check
}

// postNormalizeGrantRefusal refuses normalized capability references that
// validate against their server's JSON schema yet cannot be attached as a
// capability grant anywhere: a task's own repository target or write
// target (repo#target, repo#write {repo, base}) and an api#* scope set
// limited to a repository outside its reach. CI-only repo#write {repo}
// is not refused here: the repos page's CI capabilities list grants it
// and checks it through this same call, so it is refused for a task only
// when the check names one (markCatalogueRefusals' CatalogueRefusal) and
// by the task writes themselves.
func postNormalizeGrantRefusal(grant string) (string, map[string]string) {
	if msg, ok := taskRepoGrantRefusal(grant); ok {
		return msg, nil
	}
	if g, ok, err := model.ParseAPIGrant(grant); ok {
		if err != nil {
			return err.Error(), nil
		}
		if err := g.OutsideRepoLimit(); err != nil {
			var fields map[string]string
			if g.Repo != "" {
				fields = map[string]string{"repo": err.Error()}
			}
			return err.Error(), fields
		}
	}
	return "", nil
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
	var before, after []string
	if err := c.mutate(ctx, id, func(task *model.Task) error {
		before = grantStrings(task.Grants)
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
		if _, _, err := model.GitCredentialOverride(kept); err != nil {
			return validationErrorf("%w", err)
		}
		task.Grants = kept
		after = grantStrings(kept)
		return nil
	}); err != nil {
		return err
	}
	c.noteGrantChanges(ctx, id, before, after)
	return nil
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
	list, err := parseListQuery(r, listPageSmall)
	if err != nil {
		writeClientError(w, err)
		return
	}
	methods := s.tasks.ListCapabilityMethods(r.Context())
	q := r.URL.Query()
	if kind := strings.TrimSpace(q.Get("kind")); kind != "" {
		switch kind {
		case model.CapabilityRunTask, model.CapabilityRunProcess, model.CapabilityRunCI:
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("kind %q is not task, process or ci", kind))
			return
		}
		// A listing names a run kind and a repository, never a CI job's
		// trigger event: the CI capabilities picker grants to every job
		// the repository runs, so an event-gated rule (release#registry-
		// push, for release jobs) is not a refusal of the whole list.
		// Judged as an event-less run it was, and the picker disabled
		// every release method with "this is a unnamed run" (2026-11-09
		// repository walkthrough).
		subject := model.CapabilitySubject{Kind: kind, AnyEvent: kind == model.CapabilityRunCI}
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
	methods = filterList(methods, func(m CapabilityMethod) bool {
		return list.matches(m.Ref, m.Capability, m.Method, m.Description)
	})
	writePageTrimmed(w, r, methods, list, nil, pagedCapabilityMethod)
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
