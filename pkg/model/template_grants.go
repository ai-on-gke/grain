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
	"fmt"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// TemplateWithinTask reports why tmpl hands out more than acting holds, or
// nil when it does not: every one of the template's grants must be one
// acting holds itself and none may be one only a person may give
// (IsPersonOnlyGrant: a dangerous api grant, or priority-dispatch); every repository
// it may push to must be one acting may push to (WriteTargetFor); every
// repository it reads must be one acting may already fetch.
//
// It is the rule for an agent naming a template on work it files -- the
// template a task is filed from (ui's agentFilingEscalation, "templateId")
// and the one its review is filed from once it is done
// ("reviewTemplateId", propose_task's review_template_id). The second is
// the sharper of the two: a review is filed approved and auto-merge, on
// the branch the agent wrote, so a template naming more than the agent
// holds is that much handed to a task nobody looked at. What else a
// template filed directly may not be (auto-merge, a target the agent's
// scope does not reach) is the caller's, since a review takes neither
// from its template.
func TemplateWithinTask(acting Task, tmpl Template) error {
	if adm := NormalizeAdmission(tmpl.Admission); adm.AtLeast(AdmissionBypass) {
		return fmt.Errorf("template %s sets admission %s, which only a person may give", tmpl.ID, adm)
	}
	held := make([]string, 0, len(acting.Grants))
	for _, g := range acting.Grants {
		for _, c := range CanonicalLegacyGrants(g.Capability) {
			held = append(held, CanonicalGrantKey(c))
		}
	}
	for _, g := range tmpl.Grants {
		if IsPersonOnlyGrant(g.Capability) {
			return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
		}
		for _, needed := range CanonicalLegacyGrants(g.Capability) {
			if IsPersonOnlyGrant(needed) {
				return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
			}
			if !slices.Contains(held, CanonicalGrantKey(needed)) {
				return fmt.Errorf("template %s grants %s, which the agent's own task does not hold", tmpl.ID, g.Capability)
			}
		}
	}
	for _, w := range tmpl.Writes {
		if _, ok := WriteTargetFor(acting, w.Repo); !ok {
			return fmt.Errorf("template %s grants push access to %s, which the agent's own task cannot push to", tmpl.ID, w.Repo)
		}
	}
	for _, r := range tmpl.Reads {
		if !mayRead(acting, r) {
			return fmt.Errorf("template %s grants read access to %s, which the agent's own task cannot read", tmpl.ID, r)
		}
	}
	if len(tmpl.Reviewers) > 0 {
		return fmt.Errorf("template %s sets reviewers, which only a person may set", tmpl.ID)
	}
	return nil
}

// ReviewTemplateWithinTask is TemplateWithinTask for the review template
// a task filed by acting names (Task.ReviewTemplateID), filed against
// target: the repository's own declared default review
// (DefaultReviewTemplateID) is always allowed, since it is the review
// that task gets when it names none, and any other must pass
// TemplateWithinTask.
func ReviewTemplateWithinTask(acting Task, tmpl Template, target *RepoRef) error {
	if target != nil && tmpl.ID == DefaultReviewTemplateID(*target) {
		return nil
	}
	return TemplateWithinTask(acting, tmpl)
}

// ReviewTemplateRefusedNote is the line added to a proposal whose
// review_template_id was refused (ReviewTemplateWithinTask) and replaced
// by fallback ("" for none).
func ReviewTemplateRefusedNote(proposer, asked, fallback string, why error) string {
	instead := "no review"
	if fallback != "" {
		instead = fmt.Sprintf("the repository's default review (`%s`)", fallback)
	}
	return fmt.Sprintf("\n\n---\n\ngrain: task %s asked for this work to be reviewed from template `%s`, "+
		"which was refused (%v): a review is filed approved and auto-merge, so its template may not "+
		"hand out more than the task asking for it holds. It was filed with %s instead.",
		proposer, asked, why, instead)
}

// IsDangerousGrant reports whether name is a dangerous grant that only a person
// may grant: any dangerous api grant (including bare preset aliases), a retired
// dangerous capability (db-write, self-repair), or a capability-server reference whose
// method or arguments require a person (protocol.GrantByPerson).
func IsDangerousGrant(name string) bool {
	s := strings.TrimSpace(StoredGrantSpelling(name))
	if s == "db-write" || s == "self-repair" {
		return true
	}
	if preset, ok := apiAliasCapabilities[s]; ok {
		s = APIGrantPrefix + preset
	}
	if IsDangerousAPIGrant(s) {
		return true
	}
	if IsCapabilityRef(s) {
		if spec, err := CapabilityGrantSpec(s); err == nil {
			if ref, err := protocol.ParseRef(spec.Capability); err == nil {
				return protocol.GrantKindOf(protocol.Grant{Ref: ref, Args: spec.With}) == protocol.GrantByPerson
			}
		}
	}
	if repl := CanonicalLegacyGrants(s); !(len(repl) == 1 && repl[0] == s) {
		for _, c := range repl {
			if c != s && IsDangerousGrant(c) {
				return true
			}
		}
	}
	return false
}

// ReviewGrants is grants, the grants a review of reviewed is about to be
// filed with, less every one only a person may give (IsPersonOnlyGrant)
// reviewed does not hold itself. A review is filed approved and auto-merge on the
// reviewed task's own branch, so a dangerous grant reaching it is one a
// person never gave that work; checked when the review is filed rather
// than only when its template was chosen, because the template may have
// been edited since. Dropped names are returned beside the rest.
func ReviewGrants(grants []Grant, reviewed Task) (kept []Grant, dropped []string) {
	for _, g := range grants {
		key := CanonicalGrantKey(g.Capability)
		if IsPersonOnlyGrant(g.Capability) && !slices.ContainsFunc(reviewed.Grants, func(h Grant) bool {
			return CanonicalGrantKey(h.Capability) == key
		}) {
			dropped = append(dropped, g.Capability)
			continue
		}
		kept = append(kept, g)
	}
	return kept, dropped
}

// ReviewAdmission is the admission level a review of reviewed is filed with
// when its review template asks for tmplAdmission: capped to reviewed's own
// admission if tmplAdmission exceeds it, since raising admission above
// standard is person-only.
func ReviewAdmission(tmplAdmission Admission, reviewed Task) (Admission, bool) {
	want := NormalizeAdmission(tmplAdmission)
	if !want.AtLeast(AdmissionBypass) {
		return AdmissionStandard, false
	}
	held := NormalizeAdmission(reviewed.Admission)
	if held.AtLeast(want) {
		return want, false
	}
	return held, true
}

// declaredHeldServers are the capability servers whose grants hand a task
// credentials or code that runs outside its sandbox: a GCP key, a script
// on a host, a GitHub credential. Each is an operator's to give, so one
// arriving in a repository's own declaration is held for a person
// (DeclaredGrantNeedsPerson) even where its server would let an agent
// pass it on.
var declaredHeldServers = map[string]bool{"gcp": true, "host-script": true, "github-credential": true}

// DeclaredGrantNeedsPerson reports whether a grant written in a
// repository's own `.grain/` declaration needs a person to accept it
// before the work it files runs (task 3965): anybody who can merge to
// the repository writes those files, with no grain access of their own,
// so a declaration is not a person's grant. A dangerous or person-only
// grant (IsDangerousGrant, IsPersonOnlyGrant) needs one, as does any
// grant of a server in declaredHeldServers.
func DeclaredGrantNeedsPerson(name string) bool {
	s := strings.TrimSpace(StoredGrantSpelling(name))
	if s == "" {
		return false
	}
	if IsDangerousGrant(s) || IsPersonOnlyGrant(s) {
		return true
	}
	for _, c := range append([]string{s}, CanonicalLegacyGrants(s)...) {
		if declaredHeldServers[grantServerID(c)] {
			return true
		}
	}
	return false
}

// grantServerID is the capability server a server#method grant names, in
// either its short ("gcp#gcp-key") or its canonical reference
// ("https://.../gcp#gcp-key") spelling, and "" for any other grant.
func grantServerID(grant string) string {
	base, _, ok := strings.Cut(CapabilityGrantRef(grant), "#")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// DeclaredFiringHold is why a firing of sched (through tmpl, nil for none)
// composed as comp must wait for a person rather than land approved:
// what a repository's own declaration put there that only a person may
// accept (DeclaredCompositionHold). A schedule or template an operator
// wrote holds nothing here -- it is a person's standing approval already
// -- so only a repo-declared one is asked about. Whether a person has
// accepted the set is DeclarationAcceptance's question
// (composeScheduleTask).
func DeclaredFiringHold(sched Schedule, tmpl *Template, comp Composition) []string {
	if ScheduleDeclarationID(sched, tmpl) == "" {
		return nil
	}
	return DeclaredCompositionHold(comp)
}

// ScheduleDeclarationID is the declaration a firing of sched (through
// tmpl) is accepted under (DeclarationAcceptance): the schedule's own id
// when a repository declared it, else its template's when one did, and
// "" for a firing nothing declared.
func ScheduleDeclarationID(sched Schedule, tmpl *Template) string {
	if IsRepoDeclaredID(sched.ID) {
		return sched.ID
	}
	if tmpl != nil && IsRepoDeclaredID(tmpl.ID) {
		return tmpl.ID
	}
	return ""
}
