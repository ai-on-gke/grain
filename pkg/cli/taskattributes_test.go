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

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-65: the CLI and the UI ask the same questions of a task
// list, out of two tables in two languages (taskattributes.go says why
// there are two). This is what stops them drifting: it reads the
// frontend's own vocabulary off disk and fails when either side has an
// attribute, a type or a comparison the other has not.
//
// It compares the *shape* of the vocabulary, not the behaviour behind
// it: that "repo" exists on both sides as an enum that can be empty, not
// that both read it off the same field. The second is what
// list_test.go's own cases are for, on this side, and taskFilters.test.
// js on the other.
//
// Read with regexes rather than by running a JS parser, because the
// alternative is making `go test ./...` depend on node -- which the
// go-test job does not have to hand at that point and which would cost
// far more than this check is worth. The files it reads are written as a
// plain table for exactly this reason; a rewrite that defeats the
// patterns below fails loudly here rather than silently.

// jsAttribute is one descriptor as taskAttributes.js writes it, reduced
// to what a CLI can act on.
//
// `order` is the fixed vocabulary the attribute's values come out of,
// where it has one -- nil for the open-ended attributes (a repo, an
// author), which are whatever the deployment's tasks happen to hold.
// list_vocabulary_test.go is what compares it.
//
// `custom` marks the ones a deployment declared rather than the ones
// every grain has: those are read off a declaration this test sets up
// (frontendCustomAttributes) rather than out of the ATTRIBUTES table, so
// nothing but the declarations decides whether one is expected to exist.
type jsAttribute struct {
	id       string
	typ      string
	haveNone bool
	order    []string
	custom   bool
}

func frontendSource(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "ui", "src", name)
	body, err := os.ReadFile(path)
	if err != nil {
		// A checkout without the frontend in it -- not something CI
		// runs, but not a reason to fail a Go test suite either.
		t.Skipf("skipping: cannot read %s: %v", path, err)
	}
	return string(body)
}

// jsBlock is the text between a declaration and the line that closes it
// ("];" or "};" in the first column), which is how every table this
// reads is written.
func jsBlock(t *testing.T, source, opening, closing string) string {
	t.Helper()
	start := strings.Index(source, opening)
	if start < 0 {
		t.Fatalf("cannot find %q in the frontend source: this test's patterns need updating", opening)
	}
	rest := source[start+len(opening):]
	end := strings.Index(rest, "\n"+closing)
	if end < 0 {
		t.Fatalf("cannot find the end of %q in the frontend source: this test's patterns need updating", opening)
	}
	return rest[:end]
}

var (
	jsFieldType = regexp.MustCompile(`(?m)^\s*(\w+): TYPE_(\w+),`)
	jsBoolOrder = regexp.MustCompile(`const BOOLEAN_ORDER = \[([^\]]*)\];`)
	jsAttrID    = regexp.MustCompile(`(?m)^\s*id: "([^"]+)",`)
	jsAttrType  = regexp.MustCompile(`(?m)^\s*type: TYPE_(\w+),`)
	jsAttrNone  = regexp.MustCompile(`(?m)^\s*noneLabel: "`)
	jsAttrOrder = regexp.MustCompile(`(?m)^\s*order: (.+),$`)
	jsOperator  = regexp.MustCompile(`(?m)^\s*(\w+): \{ label: "([^"]+)", input: "([^"]+)" \},`)
	jsByType    = regexp.MustCompile(`(?m)^\s*\[TYPE_(\w+)\]: \[([^\]]*)\],`)
	jsString    = regexp.MustCompile(`"([^"]+)"`)
)

func frontendAttributes(t *testing.T) []jsAttribute {
	t.Helper()
	block := jsBlock(t, frontendSource(t, "taskAttributes.js"), "export const ATTRIBUTES = [", "];")
	var attrs []jsAttribute
	// Descriptors open at a fixed indent, which is what separates one
	// from the next.
	for _, chunk := range strings.Split(block, "\n  {\n")[1:] {
		id := jsAttrID.FindStringSubmatch(chunk)
		typ := jsAttrType.FindStringSubmatch(chunk)
		if id == nil || typ == nil {
			t.Fatalf("a descriptor in ATTRIBUTES has no id or no type this test can read:\n%s", chunk)
		}
		attrs = append(attrs, jsAttribute{
			id:       id[1],
			typ:      strings.ToLower(typ[1]),
			haveNone: jsAttrNone.MatchString(chunk),
			order:    frontendOrder(t, chunk),
		})
	}
	if len(attrs) == 0 {
		t.Fatal("read no attributes at all out of taskAttributes.js: this test's patterns need updating")
	}
	return attrs
}

// frontendConst is one string constant declared at the top level of a
// frontend file, by its name.
func frontendConst(t *testing.T, source, name string) string {
	t.Helper()
	m := regexp.MustCompile(fmt.Sprintf(`(?m)^(?:export )?const %s = "([^"]*)";`, name)).FindStringSubmatch(source)
	if m == nil {
		t.Fatalf("cannot find %s in the frontend source: this test's patterns need updating", name)
	}
	return m[1]
}

// frontendFieldTypes is taskAttributes.js's FIELD_TYPES: which of its own
// types each type a deployment may declare is compared as, by the
// declared spelling (model.CustomFieldType).
func frontendFieldTypes(t *testing.T, source string) map[string]string {
	t.Helper()
	found := jsFieldType.FindAllStringSubmatch(jsBlock(t, source, "const FIELD_TYPES = {", "};"), -1)
	if len(found) == 0 {
		t.Fatal("read no field types at all out of taskAttributes.js: this test's patterns need updating")
	}
	types := make(map[string]string, len(found))
	for _, m := range found {
		types[m[1]] = strings.ToLower(m[2])
	}
	return types
}

// frontendBooleanOrder is the two values a declared boolean field holds,
// as the frontend offers them (BOOLEAN_ORDER).
func frontendBooleanOrder(t *testing.T, source string) []string {
	t.Helper()
	m := jsBoolOrder.FindStringSubmatch(source)
	if m == nil {
		t.Fatal("cannot find BOOLEAN_ORDER in taskAttributes.js: this test's patterns need updating")
	}
	values := jsStrings(m[1])
	if len(values) == 0 {
		t.Fatal("read no values out of BOOLEAN_ORDER: this test's patterns need updating")
	}
	return values
}

// frontendCustomAttributes is the descriptors attributesFor(config) adds
// for the fields a deployment declares -- taskAttributes.js's own
// customAttribute, over declarations this test hands it: the id
// CUSTOM_PREFIX and the field's name, the type FIELD_TYPES gives its
// declared one (or the tags type, for a field declared `multiple`), a
// boolean's values BOOLEAN_ORDER and an enum's its declared options, and
// having no value a real answer for every one of them, since no field is
// required and so every custom attribute carries NONE_LABEL_FALLBACK.
//
// It is built out of the frontend's own constants rather than written
// out here for the reason the rest of this file is read off disk: a rule
// this test kept itself would go on agreeing with a frontend that had
// changed.
func frontendCustomAttributes(t *testing.T, fields []ui.CustomFieldDeclaration) []jsAttribute {
	t.Helper()
	source := frontendSource(t, "taskAttributes.js")
	prefix := frontendConst(t, source, "CUSTOM_PREFIX")
	if frontendConst(t, source, "NONE_LABEL_FALLBACK") == "" {
		t.Fatal("the frontend calls having no value nothing at all, so no custom attribute can be filtered as empty")
	}
	types := frontendFieldTypes(t, source)
	booleans := frontendBooleanOrder(t, source)
	var attrs []jsAttribute
	for _, f := range fields {
		// A field with no name is no attribute (attributesFor filters
		// them out), and a type this build of the frontend has never
		// heard of is compared as text rather than dropped.
		if f.Name == "" {
			continue
		}
		valueType, known := types[f.Type]
		if !known {
			valueType = string(typeText)
		}
		attr := jsAttribute{
			id:       prefix + f.Name,
			typ:      valueType,
			haveNone: true,
			custom:   true,
		}
		if f.Multiple {
			attr.typ = string(typeTags)
		}
		switch {
		case f.Type == string(model.CustomFieldBool):
			attr.order = booleans
		case valueType == string(typeEnum):
			attr.order = f.Options
		}
		attrs = append(attrs, attr)
	}
	if len(attrs) == 0 {
		t.Fatal("no declared fields to build attributes from")
	}
	return attrs
}

// cliOnlyAttributes are the ones `grain list` can ask about and the web's
// task views deliberately cannot. kind (interactive or background) is
// one: the browser keeps live chats in its own Sessions view and never
// shows them in a task list or board (taskFilters.js's isSessionTask),
// while the CLI has no such view, so `grain list -interactive` is still
// how a terminal finds them.
var cliOnlyAttributes = map[string]bool{"kind": true}

// The two tables hold the same attributes, in the same order, of the
// same types, with the same answer to "can this be empty" -- which is
// what decides the comparisons each offers.
func TestTaskAttributesMatchTheFrontend(t *testing.T) {
	t.Parallel()
	want := frontendAttributes(t)
	shared := make(attributeSet, 0, len(builtinAttributes))
	for _, a := range builtinAttributes {
		if !cliOnlyAttributes[a.id] {
			shared = append(shared, a)
		}
	}
	if len(want) != len(shared) {
		t.Fatalf("the frontend has %d attributes (%s) and this CLI has %d (%s): one of them has grown one the other cannot ask about",
			len(want), strings.Join(jsAttributeIDs(want), ", "), len(shared), strings.Join(shared.ids(), ", "))
	}
	for i, w := range want {
		got := shared[i]
		if got.id != w.id {
			t.Errorf("attribute %d is %q here and %q in the frontend: the ids are what a link and a -filter both name, so they cannot differ", i, got.id, w.id)
			continue
		}
		if string(got.typ) != w.typ {
			t.Errorf("%s is a %s here and a %s in the frontend, so the two offer different comparisons over it", got.id, got.typ, w.typ)
		}
		if (got.noneLabel != "") != w.haveNone {
			t.Errorf("%s says having no value is %v worth filtering on here and %v in the frontend", got.id, got.noneLabel != "", w.haveNone)
		}
	}
}

// The comparisons themselves: the same ids meaning the same thing, and
// each type offering the same ones.
func TestOperatorsMatchTheFrontend(t *testing.T) {
	t.Parallel()
	source := frontendSource(t, "taskFilters.js")
	block := jsBlock(t, source, "export const OPERATORS = {", "};")
	found := jsOperator.FindAllStringSubmatch(block, -1)
	if len(found) == 0 {
		t.Fatal("read no operators at all out of taskFilters.js: this test's patterns need updating")
	}
	if len(found) != len(operators) {
		t.Fatalf("the frontend makes %d comparisons and this CLI makes %d", len(found), len(operators))
	}
	for i, m := range found {
		got := operators[i]
		if got.id != m[1] || got.label != m[2] || got.input != m[3] {
			t.Errorf("comparison %d is {%s, %s, %s} here and {%s, %s, %s} in the frontend",
				i, got.id, got.label, got.input, m[1], m[2], m[3])
		}
	}

	byType := jsBlock(t, source, "const OPERATORS_BY_TYPE = {", "};")
	rows := jsByType.FindAllStringSubmatch(byType, -1)
	if len(rows) != len(operatorsByType) {
		t.Fatalf("the frontend gives comparisons to %d types and this CLI to %d", len(rows), len(operatorsByType))
	}
	for _, row := range rows {
		typ := attrType(strings.ToLower(row[1]))
		var want []string
		for _, m := range jsString.FindAllStringSubmatch(row[2], -1) {
			want = append(want, m[1])
		}
		got := operatorsByType[typ]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("a %s takes [%s] here and [%s] in the frontend", typ, strings.Join(got, ", "), strings.Join(want, ", "))
		}
	}
}

// "has no value at all" is one string on the wire and in a link
// (taskAttributes.js's NONE), and this CLI accepts it alongside its own
// shorter spelling -- so a condition copied out of the address bar works
// unaltered.
func TestTheFrontendsSpellingOfNoneIsAccepted(t *testing.T) {
	t.Parallel()
	source := frontendSource(t, "taskAttributes.js")
	m := regexp.MustCompile(`export const NONE = "([^"]+)";`).FindStringSubmatch(source)
	if m == nil {
		t.Fatal("cannot find NONE in taskAttributes.js: this test's patterns need updating")
	}
	if m[1] != noneURLValue {
		t.Errorf("the frontend writes %q for a task with no value at all and this CLI reads %q", m[1], noneURLValue)
	}
}

// grain/task-99: the attributes a deployment declares are built on both
// sides from the same declaration (model.CustomField), so the two have to
// agree about what one turns into -- the id it takes, the type its
// comparisons come from, and the values a boolean holds. A field
// compared as a date in the browser and as text here would answer the
// same link two ways.
func TestCustomFieldMappingMatchesTheFrontend(t *testing.T) {
	t.Parallel()
	source := frontendSource(t, "taskAttributes.js")
	for _, tc := range []struct {
		what string
		name string
		got  string
	}{
		{what: "the namespace a declared field's id takes", name: "CUSTOM_PREFIX", got: customPrefix},
		{what: `what "nobody filled this in" is called`, name: "NONE_LABEL_FALLBACK", got: customNoneLabel},
	} {
		if want := frontendConst(t, source, tc.name); want != tc.got {
			t.Errorf("%s is %q in the frontend and %q here", tc.what, want, tc.got)
		}
	}

	types := frontendFieldTypes(t, source)
	if len(types) != len(customFieldTypes) {
		t.Fatalf("the frontend maps %d declared types onto its own and this CLI maps %d", len(types), len(customFieldTypes))
	}
	for declared, want := range types {
		got, ok := customFieldTypes[model.CustomFieldType(declared)]
		if !ok {
			t.Errorf("the frontend compares a declared %s field and this CLI has no reading for one", declared)
			continue
		}
		if string(got) != want {
			t.Errorf("a declared %s field is a %s here and a %s in the frontend", declared, got, want)
		}
	}

	want := frontendBooleanOrder(t, source)
	if strings.Join(want, ",") != strings.Join(booleanValues, ",") {
		t.Errorf("a boolean field holds [%s] in the frontend and [%s] here", strings.Join(want, ", "), strings.Join(booleanValues, ", "))
	}
}

func jsAttributeIDs(attrs []jsAttribute) []string {
	ids := make([]string, 0, len(attrs))
	for _, a := range attrs {
		ids = append(ids, a.id)
	}
	return ids
}

// grain/task-201: "is a legacy value" is the one comparison an
// *attribute* offers rather than a type, so the two tables above cannot
// check it -- OPERATORS_BY_TYPE says nothing about it on either side.
// What has to hold is that both sides hang it off the same thing: a
// field the deployment declares offers it, and nothing built in does,
// since only a deployment's own declaration can change under a value a
// task is already carrying.
func TestTheLegacyComparisonIsOfferedOnBothSides(t *testing.T) {
	t.Parallel()
	filters := frontendSource(t, "taskFilters.js")
	if !strings.Contains(filters, `attr.legacy ? [...withEmpty, "legacy"]`) {
		t.Error("taskFilters.js's operatorsFor no longer offers \"legacy\" for an attribute carrying a legacy predicate: " +
			"this test's patterns need updating, or the two sides have drifted")
	}
	attributes := frontendSource(t, "taskAttributes.js")
	if !regexp.MustCompile(`(?m)^\s*legacy: \(t\) =>`).MatchString(attributes) {
		t.Error("taskAttributes.js's customAttribute no longer gives a declared field a legacy predicate, " +
			"so the browser cannot answer a comparison this CLI offers")
	}

	declared := customAttribute(ui.CustomFieldDeclaration{
		Name: "priority", Type: string(model.CustomFieldEnum), Options: []string{"low", "high"},
	})
	if declared.legacy == nil || !contains(operatorsFor(&declared), "legacy") {
		t.Errorf("a declared field takes [%s] here, without \"legacy\" among them",
			strings.Join(operatorsFor(&declared), ", "))
	}
	for i := range builtinAttributes {
		if builtinAttributes[i].legacy != nil {
			t.Errorf("the built-in %s claims to have legacy values: its vocabulary is this build's own, "+
				"so nothing can strand a value in it", builtinAttributes[i].id)
		}
	}
}
