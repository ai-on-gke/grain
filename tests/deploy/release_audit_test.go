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

package deploy

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

const (
	releaseAuditTemplatePath = ".grain/templates/release-audit.yml"
	releaseAuditTemplateID   = "repo:template:ai-on-gke:grain:release-audit"
)

// The release audit's grants (design/playbook/releases.playbook.md
// section 17): the release manager's read scopes, and nothing it steers
// with.
var releaseAuditGrants = []string{
	`https://grain.dev/capabilities/api#ci-read={"repo":"ai-on-gke/grain"}`,
	`https://grain.dev/capabilities/api#releases:read={"repo":"ai-on-gke/grain"}`,
	`https://grain.dev/capabilities/api#repos:read={"repo":"ai-on-gke/grain"}`,
	"https://grain.dev/capabilities/api#self-debug",
}

func releaseAuditTemplate(t *testing.T) model.Template {
	t.Helper()
	decl := declarations(t)
	if len(decl.Refused) > 0 {
		t.Fatalf("declarations refused by this build: %+v", decl.Refused)
	}
	for _, f := range decl.Templates {
		if f.Path == releaseAuditTemplatePath {
			return f.Template
		}
	}
	t.Fatalf("%s is not declared", releaseAuditTemplatePath)
	return model.Template{}
}

// The audit loads under the id the release manager files it by, with
// the version field its body is written around.
func TestTheReleaseAuditLoadsWithAVersion(t *testing.T) {
	tmpl := releaseAuditTemplate(t)
	if tmpl.ID != releaseAuditTemplateID {
		t.Errorf("%s loads as %q, want %q: the release manager files it by that id", releaseAuditTemplatePath, tmpl.ID, releaseAuditTemplateID)
	}
	var fields []string
	for _, f := range tmpl.Fields {
		fields = append(fields, f.Name)
	}
	if !slices.Equal(fields, []string{"version"}) {
		t.Errorf("%s fields = %v, want [version]", releaseAuditTemplatePath, fields)
	}
	if !strings.Contains(tmpl.Title, "{{version}}") {
		t.Errorf("%s title %q does not name its version", releaseAuditTemplatePath, tmpl.Title)
	}
}

// The audit only reads: it may run while the train, a publish or a repair
// is still changing the release, so it holds nothing that changes tasks,
// merges, releases, schedules or dispatch, and no grant only a person may
// give (a manager's proposal of it would otherwise be held for one).
func TestTheReleaseAuditOnlyReads(t *testing.T) {
	tmpl := releaseAuditTemplate(t)
	var declared []string
	for _, g := range tmpl.Grants {
		declared = append(declared, g.Capability)
		if model.DeclaredGrantNeedsPerson(g.Capability) {
			t.Errorf("%s declares %s, which only a person may grant", releaseAuditTemplatePath, g.Capability)
		}
		if model.IsDangerousGrant(g.Capability) {
			t.Errorf("%s declares dangerous %s", releaseAuditTemplatePath, g.Capability)
		}
	}
	if !slices.Equal(declared, releaseAuditGrants) {
		t.Errorf("%s declares %q, want %q", releaseAuditTemplatePath, declared, releaseAuditGrants)
	}
	grants := model.APIGrantsOf(declared)
	for _, scope := range []model.APIScope{"tasks:write", "merge:write", "merge:force", "ci:write",
		"releases:write", "schedules:write", "repos:write", "dispatch:write", "notifications:write"} {
		if grants.Allows(scope, "ai-on-gke/grain") {
			t.Errorf("%s holds %s; a release audit only reads", releaseAuditTemplatePath, scope)
		}
	}
	for _, scope := range []model.APIScope{"tasks:read", "ci:read", "releases:read", "repos:read"} {
		if !grants.Allows(scope, "ai-on-gke/grain") {
			t.Errorf("%s does not hold %s, which its body uses", releaseAuditTemplatePath, scope)
		}
	}
	push, err := repodecl.TemplatePush(releaseAuditTemplatePath, []byte(read(t, releaseAuditTemplatePath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(push) > 0 {
		t.Errorf("%s may push %+v; a release audit never moves a ref", releaseAuditTemplatePath, push)
	}
}

// Every release ends with its audit: the release manager's close files it
// by its template id and version.
func TestTheReleaseManagerFilesTheAuditWhenItCloses(t *testing.T) {
	tmpl, _ := releaseManagerDeclarations(t)
	_, closing, ok := strings.Cut(tmpl.Body, "## 5. Close")
	if !ok {
		t.Fatalf("%s has no section 5 (Close)", releaseManagerTemplatePath)
	}
	closing, _, _ = strings.Cut(closing, "## Limits")
	for _, want := range []string{"propose_task", "template_id: " + releaseAuditTemplateID, "template_fields: {version: vX.Y.Z}"} {
		if !strings.Contains(closing, want) {
			t.Errorf("%s section 5 does not say %q", releaseManagerTemplatePath, want)
		}
	}
	audit, close := strings.Index(closing, releaseAuditTemplateID), strings.Index(closing, "nothing_needed")
	if audit < 0 || close < 0 || audit > close {
		t.Errorf("%s section 5 must file the audit before nothing_needed", releaseManagerTemplatePath)
	}
}
