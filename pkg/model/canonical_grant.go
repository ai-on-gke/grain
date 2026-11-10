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

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// This file is slices 2-3 of design/design/unified-grants.design.md (§1-§3):
// every grant a task can hold read and written as one shape, a capability
// reference plus its normalized with: arguments, while readers still accept
// legacy api: / repo-read: / repo-write: strings from unmigrated store rows
// and .grain/ declarations.
//
// CanonicalGrant turns any stored string into the unified shape; writers emit
// CanonicalGrant(...).Key() (EncodeCapabilityGrant's form); StoredGrantSpelling
// and LegacyGrantString keep internal comparisons identical across both
// spellings until slice 4 retires the legacy strings.

// APICapabilityServerID is the canonical capability_id of grain's own
// agent API as an in-process capability server: one method per scope,
// preset and scope set of docs/agent-api.md, each taking an optional
// {repo} (formerly api:<scope>[@owner/name]).
const APICapabilityServerID = "https://grain.dev/capabilities/api"

// APIRef is the reference of the api server's method for a scope, preset
// or scope set name: https://grain.dev/capabilities/api#<what>.
func APIRef(what string) string { return APICapabilityServerID + "#" + what }

// APIGrantString is the canonical grant string (EncodeCapabilityGrant's form)
// for a scope, preset or scope set name, optionally limited to repo
// (owner/name, canonicalized via ParseRepo when valid).
func APIGrantString(what, repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return APIRef(what)
	}
	if r, err := ParseRepo(repo); err == nil {
		repo = r.String()
	}
	return (Grant{Capability: APIRef(what), With: repoWith(repo)}).Key()
}

// IsAPIGrantRef reports whether s is a capability grant targeting the
// in-process https://grain.dev/capabilities/api server.
func IsAPIGrantRef(s string) bool {
	what, ok := apiMethodOf(CapabilityGrantRef(s))
	return ok && what != ""
}

// The repo server's task-level repository access (unified-grants §1):
// #target is the one repository and base branch a task works on, a
// projection of Task.Target/Base rather than a stored grant. #read and
// #write are also a task's Reads and Writes entries, #write then always
// carrying a base ("" for the repository's default branch), which is
// what tells it apart from a CI job's {repo}-only #write.
const (
	RepoTargetMethod = "target"
	RepoTargetRef    = RepoCapabilityServerID + "#" + RepoTargetMethod
)

// CanonicalGrant reads one stored grant string as the unified grant it
// stands for:
//
//   - api:<scope-or-preset> is APIRef(<scope-or-preset>), and
//     api:<x>@<owner/name> the same with With {"repo": "<owner/name>"}
//     (the repository as ParseRepo prints it);
//   - repo-read:<repo> and repo-write:<repo> are RepoReadRef and
//     RepoWriteRef with {"repo": "<repo>"};
//   - a capability reference, with or without =<json>, is its reference
//     and its with: arguments as written, keys sorted (an api reference
//     with its repo printed the way ParseRepo prints it);
//   - anything else -- a bare in-process name, or a string none of the
//     above parses -- passes through as Capability, With nil.
//
// It does not resolve server aliases or apply a server's defaults: that
// is normalization against the server (NormalizeCapabilityNames), which
// a reader holding only a string cannot do.
func CanonicalGrant(s string) Grant {
	trimmed := strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(trimmed, APIGrantPrefix); ok {
		what, repo, limited := strings.Cut(rest, "@")
		if what == "" {
			return Grant{Capability: s}
		}
		if !limited {
			return Grant{Capability: APIRef(what)}
		}
		r, err := ParseRepo(repo)
		if err != nil {
			return Grant{Capability: s}
		}
		return Grant{Capability: APIRef(what), With: repoWith(r.String())}
	}
	if repo, ok := strings.CutPrefix(trimmed, RepoReadCapabilityPrefix); ok && repo != "" {
		return Grant{Capability: RepoReadRef, With: repoWith(repo)}
	}
	if repo, ok := strings.CutPrefix(trimmed, RepoWriteCapabilityPrefix); ok && repo != "" {
		return Grant{Capability: RepoWriteRef, With: repoWith(repo)}
	}
	if !IsCapabilityRef(trimmed) {
		return Grant{Capability: s}
	}
	spec, err := CapabilityGrantSpec(trimmed)
	if err != nil {
		return Grant{Capability: s}
	}
	g := Grant{Capability: spec.Capability}
	if spec.With != nil {
		with := spec.With
		if what, ok := apiMethodOf(spec.Capability); ok && what != "" {
			if len(with) == 0 {
				return g
			}
			if r, isString := with["repo"].(string); isString && len(with) == 1 {
				if strings.TrimSpace(r) == "" {
					return g
				}
				if ref, err := ParseRepo(r); err == nil {
					with = map[string]any{"repo": ref.String()}
				}
			}
		}
		data, err := json.Marshal(with)
		if err != nil {
			return Grant{Capability: s}
		}
		g.With = data
	}
	return g
}

// apiMethodOf is the method an api server reference names, and whether
// ref is one at all.
func apiMethodOf(ref string) (string, bool) {
	return strings.CutPrefix(ref, APICapabilityServerID+"#")
}

func repoWith(repo string) json.RawMessage {
	data, err := json.Marshal(map[string]string{"repo": repo})
	if err != nil {
		panic(fmt.Sprintf("model: encoding repo %q: %v", repo, err))
	}
	return data
}

// Key is g's grant string: its capability, then "=" and its With when
// it has one (EncodeCapabilityGrant's form). Two grants are equal exactly
// when their Keys are: CanonicalGrant sorts With's keys.
func (g Grant) Key() string {
	if len(g.With) == 0 || bytes.Equal(g.With, []byte("null")) {
		return g.Capability
	}
	return g.Capability + "=" + string(g.With)
}

// CanonicalGrantKey is CanonicalGrant(s).Key(): the one key both of a
// grant's spellings compare equal under.
func CanonicalGrantKey(s string) string { return CanonicalGrant(s).Key() }

// LegacyGrantString is the string slice 2 stores for g: api:<x>[@repo]
// for an api reference with no arguments or exactly {repo}, and g's own
// Key for everything else, repo-read:/repo-write: included (schema
// migration 88 already stores those as references). ok is false for an
// api reference whose arguments are anything but {repo}, which no stored
// spelling can carry.
func LegacyGrantString(g Grant) (s string, ok bool) {
	what, isAPI := apiMethodOf(g.Capability)
	if !isAPI {
		return g.Key(), true
	}
	if what == "" {
		return "", false
	}
	if len(g.With) == 0 || bytes.Equal(g.With, []byte("null")) {
		return APIGrantPrefix + what, true
	}
	var with map[string]any
	if err := json.Unmarshal(g.With, &with); err != nil {
		return "", false
	}
	if len(with) == 0 {
		return APIGrantPrefix + what, true
	}
	repo, isString := with["repo"].(string)
	if len(with) != 1 || !isString {
		return "", false
	}
	if strings.TrimSpace(repo) == "" {
		return APIGrantPrefix + what, true
	}
	r, err := ParseRepo(repo)
	if err != nil {
		return "", false
	}
	return APIGrantPrefix + what + "@" + r.String(), true
}

// StoredGrantSpelling is s as slice 2 stores it: an api reference
// rewritten to the api:<x>[@repo] an older binary reads, and every other
// string unchanged. It is how every reader compares two grants, so that
// a reference and today's spelling of the same grant give the same
// answer as two copies of today's spelling always did.
func StoredGrantSpelling(s string) string {
	if _, isAPI := apiMethodOf(CapabilityGrantRef(s)); !isAPI {
		return s
	}
	if legacy, ok := LegacyGrantString(CanonicalGrant(s)); ok {
		return legacy
	}
	return s
}

// APIGrantSpellings is every spelling of capability's agent API grant --
// the canonical https://grain.dev/capabilities/api#<what>[={"repo":...}]
// reference and the legacy api:<what>[@<repo>] form -- with capability
// itself first, so a rule written in either matches a grant held in the
// other. Just capability for anything else.
func APIGrantSpellings(capability string) []string {
	out := []string{capability}
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	trimmed := strings.TrimSpace(capability)
	probe := trimmed
	if len(trimmed) > len(APIGrantPrefix) && strings.EqualFold(trimmed[:len(APIGrantPrefix)], APIGrantPrefix) {
		probe = APIGrantPrefix + trimmed[len(APIGrantPrefix):]
	} else if ref, with, hasWith := strings.Cut(trimmed, "="); strings.EqualFold(CapabilityGrantRef(ref), APICapabilityServerID) ||
		(len(ref) > len(APICapabilityServerID)+1 && strings.EqualFold(ref[:len(APICapabilityServerID)+1], APICapabilityServerID+"#")) {
		probe = APICapabilityServerID + ref[len(APICapabilityServerID):]
		if hasWith {
			probe += "=" + with
		}
	}
	g := CanonicalGrant(probe)
	if _, isAPI := apiMethodOf(g.Capability); isAPI {
		add(g.Key())
		if legacy, ok := LegacyGrantString(g); ok {
			add(legacy)
		}
	}
	return out
}

// storedGrantSpellings is grants with each Capability in its stored
// spelling (StoredGrantSpelling); grants itself when none changes.
func storedGrantSpellings(grants []Grant) []Grant {
	var out []Grant
	for i, g := range grants {
		s := StoredGrantSpelling(g.Capability)
		if s == g.Capability && out == nil {
			continue
		}
		if out == nil {
			out = make([]Grant, len(grants))
			copy(out, grants[:i])
		}
		g.Capability = s
		out[i] = g
	}
	if out == nil {
		return grants
	}
	return out
}

// TaskRepoGrants projects a task's repository access into unified grants
// (unified-grants §1): #target {repo, base} for its Target, #read {repo}
// for each Reads entry, #write {repo, base} for each Writes entry.
// Storage stays Task.Target/Base, Reads and Writes; this is how a reader
// that takes grants sees them.
func TaskRepoGrants(t Task) []Grant {
	var out []Grant
	if t.Target != nil {
		out = append(out, Grant{Capability: RepoTargetRef, With: repoBaseWith(t.Target.String(), t.Base)})
	}
	for _, r := range t.Reads {
		out = append(out, Grant{Capability: RepoReadRef, With: repoWith(r.String())})
	}
	for _, w := range t.Writes {
		out = append(out, Grant{Capability: RepoWriteRef, With: repoBaseWith(w.Repo.String(), w.Base)})
	}
	return out
}

// TaskRepoAccessKeys is the Key of each read and write grant
// TaskRepoGrants projects t's Reads and Writes into, leaving out its
// target: the grants a person adds and removes in the Grants list,
// beside the target that is its own field.
func TaskRepoAccessKeys(t Task) []string {
	var out []string
	for _, g := range TaskRepoGrants(t) {
		if g.Capability != RepoTargetRef {
			out = append(out, g.Key())
		}
	}
	return out
}

// GrantChanges is what turning the grant strings before into after
// attached and detached, each in the order its own list holds them.
func GrantChanges(before, after []string) (attached, detached []string) {
	for _, g := range after {
		if !slices.Contains(before, g) && !slices.Contains(attached, g) {
			attached = append(attached, g)
		}
	}
	for _, g := range before {
		if !slices.Contains(after, g) && !slices.Contains(detached, g) {
			detached = append(detached, g)
		}
	}
	return attached, detached
}

func repoBaseWith(repo, base string) json.RawMessage {
	data, err := json.Marshal(map[string]string{"repo": repo, "base": base})
	if err != nil {
		panic(fmt.Sprintf("model: encoding repo %q: %v", repo, err))
	}
	return data
}

// RepoGrantArgs is the {repo, base} of a task-level repository grant
// (#target, #read, #write with a base). hasBase is false where With has
// no base key at all -- for #write, a CI job's {repo}-only grant rather
// than a Writes entry.
func RepoGrantArgs(g Grant) (repo RepoRef, base string, hasBase bool, err error) {
	var with map[string]any
	if err := json.Unmarshal(g.With, &with); err != nil || with == nil {
		return RepoRef{}, "", false, fmt.Errorf("grant %s: with: must be an object naming a repo", g.Capability)
	}
	for k := range with {
		if k != "repo" && k != "base" {
			return RepoRef{}, "", false, fmt.Errorf("grant %s: unknown argument %q (want repo, base)", g.Capability, k)
		}
	}
	name, ok := with["repo"].(string)
	if !ok || name == "" {
		return RepoRef{}, "", false, fmt.Errorf("grant %s: with: needs a repo", g.Capability)
	}
	repo, err = ParseRepo(name)
	if err != nil {
		return RepoRef{}, "", false, fmt.Errorf("grant %s: %v", g.Capability, err)
	}
	if b, present := with["base"]; present {
		base, ok = b.(string)
		if !ok {
			return RepoRef{}, "", false, fmt.Errorf("grant %s: base must be a string", g.Capability)
		}
		hasBase = true
	}
	return repo, base, hasBase, nil
}

// WriteTargetsOfGrants is the write targets among grants -- #target, then
// every #write that carries a base -- in order: what WriteTargets reads
// off a task, read off its grants instead.
func WriteTargetsOfGrants(grants []Grant) []WriteTarget {
	var out []WriteTarget
	for _, g := range grants {
		if g.Capability != RepoTargetRef && g.Capability != RepoWriteRef {
			continue
		}
		repo, base, hasBase, err := RepoGrantArgs(g)
		if err != nil || (g.Capability == RepoWriteRef && !hasBase) {
			continue
		}
		out = append(out, WriteTarget{Repo: repo, Base: base})
	}
	return out
}

// GrantSpelling says which of a request's spellings carried a kind of
// grant, for SplitRequestGrants.
type GrantSpelling struct {
	Capabilities, Reads, Writes, Target bool
	// TargetFields names the target-setting field(s) the request sent
	// alongside grants ("repo", "branch", "base", "no_repo", "noRepo", or
	// a change-target kind). Non-empty implies Target; when Target is true
	// and TargetFields is empty, the refusal falls back to "repo".
	TargetFields []string
}

func targetFieldsPhrase(fields []string) string {
	switch len(fields) {
	case 0:
		return "repo field"
	case 1:
		return fields[0] + " field"
	case 2:
		return fields[0] + " and " + fields[1] + " fields"
	default:
		return strings.Join(fields[:len(fields)-1], ", ") + " and " + fields[len(fields)-1] + " fields"
	}
}

// RequestGrants is a request's grants: list, sorted into the fields that
// store them (slice 2 writes today's spelling).
type RequestGrants struct {
	Capabilities []string
	Reads        []RepoRef
	Writes       []WriteTarget
	// Target is a #target grant's repository, with its base; nil for none.
	Target *WriteTarget
}

// SplitRequestGrants sorts an API request's grants: [{capability, with}]
// into the stored fields: an api or other capability reference (or bare
// name) into Capabilities, in canonical reference form (EncodeCapabilityGrant);
// #read {repo} into Reads; #write {repo, base} into Writes; #target
// {repo, base} into Target. A CI job's {repo}-only #write, and a
// #write {refs, force}, stay capabilities.
//
// legacy says which of the request's own capabilities, reads, writes and
// repo fields it also sent: a request that names one kind in both
// spellings is refused rather than merged (unified-grants §3).
func SplitRequestGrants(grants []Grant, legacy GrantSpelling) (RequestGrants, error) {
	var out RequestGrants
	for _, g := range grants {
		g.Capability = strings.TrimSpace(g.Capability)
		if g.Capability == "" {
			return RequestGrants{}, fmt.Errorf("grants: an entry has no capability")
		}
		if len(g.With) > 0 && !bytes.Equal(g.With, []byte("null")) {
			var with map[string]any
			if err := json.Unmarshal(g.With, &with); err != nil {
				return RequestGrants{}, fmt.Errorf("grants: %s: with: must be an object", g.Capability)
			}
			normalized, err := json.Marshal(with)
			if err != nil {
				return RequestGrants{}, fmt.Errorf("grants: %s: %v", g.Capability, err)
			}
			g.With = normalized
		} else {
			g.With = nil
		}
		switch g.Capability {
		case RepoTargetRef:
			repo, base, _, err := RepoGrantArgs(g)
			if err != nil {
				return RequestGrants{}, fmt.Errorf("grants: %v", err)
			}
			if out.Target != nil {
				return RequestGrants{}, fmt.Errorf("grants: more than one %s", RepoTargetRef)
			}
			if legacy.Target || len(legacy.TargetFields) > 0 {
				return RequestGrants{}, fmt.Errorf("grants: %s and the request's %s both name the task's repository; send one", RepoTargetRef, targetFieldsPhrase(legacy.TargetFields))
			}
			out.Target = &WriteTarget{Repo: repo, Base: base}
			continue
		case RepoReadRef:
			repo, _, hasBase, err := RepoGrantArgs(g)
			if err != nil {
				return RequestGrants{}, fmt.Errorf("grants: %v", err)
			}
			if hasBase {
				return RequestGrants{}, fmt.Errorf("grants: %s takes only a repo", RepoReadRef)
			}
			if legacy.Reads {
				return RequestGrants{}, fmt.Errorf("grants: %s and reads both name read access; send one", RepoReadRef)
			}
			out.Reads = append(out.Reads, repo)
			continue
		case RepoWriteRef:
			if repo, base, hasBase, err := RepoGrantArgs(g); err == nil && hasBase {
				if legacy.Writes {
					return RequestGrants{}, fmt.Errorf("grants: %s and writes both name write targets; send one", RepoWriteRef)
				}
				out.Writes = append(out.Writes, WriteTarget{Repo: repo, Base: base})
				continue
			}
		}
		if _, ok := LegacyGrantString(g); !ok {
			return RequestGrants{}, fmt.Errorf("grants: %s takes only a repo", g.Capability)
		}
		if strings.HasPrefix(g.Capability, APIGrantPrefix) && len(g.With) > 0 {
			return RequestGrants{}, fmt.Errorf("grants: %s: only a capability reference (https://...) takes with: arguments", g.Capability)
		}
		s := CanonicalGrant(g.Key()).Key()
		if legacy.Capabilities {
			return RequestGrants{}, fmt.Errorf("grants: %s and capabilities both name capability grants; send one", g.Capability)
		}
		out.Capabilities = append(out.Capabilities, s)
	}
	return out, nil
}

// DecodeRequestGrantsJSON reads a raw JSON grants: list -- each entry a grant
// string or a {capability, with} object -- into RequestGrants via
// SplitRequestGrants. An empty grants: list sets Capabilities to an empty
// non-nil slice (and refuses when legacy.Capabilities is also true).
func DecodeRequestGrantsJSON(raw json.RawMessage, legacy GrantSpelling) (RequestGrants, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return RequestGrants{}, fmt.Errorf("grants must be a list")
	}
	grants := make([]Grant, 0, len(items))
	for _, item := range items {
		names, err := CapabilityEntryNames(item)
		if err != nil {
			return RequestGrants{}, fmt.Errorf("grants: %v", err)
		}
		for _, s := range names {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if !IsCapabilityRef(s) {
				grants = append(grants, Grant{Capability: s})
				continue
			}
			spec, err := CapabilityGrantSpec(s)
			if err != nil {
				return RequestGrants{}, fmt.Errorf("grants: %v", err)
			}
			g := Grant{Capability: spec.Capability}
			if spec.With != nil {
				g.With, _ = json.Marshal(spec.With)
			}
			grants = append(grants, g)
		}
	}
	out, err := SplitRequestGrants(grants, legacy)
	if err != nil {
		return RequestGrants{}, err
	}
	if len(items) == 0 {
		if legacy.Capabilities {
			return RequestGrants{}, fmt.Errorf("grants: grants and capabilities both name capability grants; send one")
		}
		out.Capabilities = []string{}
	}
	return out, nil
}

// DecodeRawRequestGrants decodes an unmarshaled tool-call grants argument (from
// map[string]any) using DecodeRequestGrantsJSON. Returns present=false when raw
// is nil.
func DecodeRawRequestGrants(raw any, legacy GrantSpelling) (out RequestGrants, present bool, err error) {
	if raw == nil {
		return RequestGrants{}, false, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return RequestGrants{}, true, fmt.Errorf("grants must be a list")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return RequestGrants{}, false, nil
	}
	out, err = DecodeRequestGrantsJSON(encoded, legacy)
	return out, true, err
}

// Equal reports whether g and o are the same grant come by the same way:
// equal Capability, With and Via, and the same Folder pointer -- what ==
// compared before Grant held a json.RawMessage, which == cannot.
func (g Grant) Equal(o Grant) bool {
	return g.Capability == o.Capability && g.Via == o.Via && g.Folder == o.Folder && bytes.Equal(g.With, o.With)
}

// childWriteGrants is t's own Writes entries read as #write {repo, base}
// grants (TaskRepoGrants), without its #target.
func childWriteGrants(t Task) []WriteTarget {
	t.Target = nil
	return WriteTargetsOfGrants(TaskRepoGrants(t))
}
