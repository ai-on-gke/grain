package model_test

// The inputs a template asks for (grain/task-58): what a declaration may
// say, what an instantiation may supply for it, and what the holes in
// the prose become once both have had their say. Pure functions over the
// declaration, so they are pinned down here rather than only through the
// API that collects the values.

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func textField() model.TemplateField {
	return model.TemplateField{Name: "dependency", Label: "Dependency"}
}

func selectField() model.TemplateField {
	return model.TemplateField{
		Name:    "environment",
		Options: []string{"staging", "production"},
		Default: "staging",
	}
}

func TestTemplateFieldSelectorIsWhetherItOffersOptions(t *testing.T) {
	if textField().Selector() {
		t.Errorf("a field with no options is a text box, not a selector")
	}
	if !selectField().Selector() {
		t.Errorf("a field with options is a selector")
	}
}

func TestTemplateFieldDisplayNameFallsBackToTheName(t *testing.T) {
	if got := textField().DisplayName(); got != "Dependency" {
		t.Errorf("DisplayName = %q, want the label", got)
	}
	if got := selectField().DisplayName(); got != "environment" {
		t.Errorf("DisplayName = %q, want the name where there is no label", got)
	}
}

func TestValidateTemplateFieldsRefusesWhatNobodyCouldFillIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []model.TemplateField
		want   string
	}{
		{
			name:   "a name that is not a key",
			fields: []model.TemplateField{{Name: "Dependency Name"}},
			want:   "field name",
		},
		{
			name:   "a blank option",
			fields: []model.TemplateField{{Name: "env", Options: []string{"staging", " "}}},
			want:   "cannot be blank",
		},
		{
			name:   "the same option twice",
			fields: []model.TemplateField{{Name: "env", Options: []string{"staging", "staging"}}},
			want:   "listed twice",
		},
		{
			// A default the picker could never produce: the form would
			// open showing a value it cannot get back to.
			name:   "a default outside the options",
			fields: []model.TemplateField{{Name: "env", Options: []string{"staging"}, Default: "production"}},
			want:   "not one of its options",
		},
		{
			name:   "two fields sharing a name",
			fields: []model.TemplateField{{Name: "env"}, {Name: "env"}},
			want:   "declared twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := model.ValidateTemplateFields(tc.fields)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestValidateTemplateFieldsAcceptsBothKinds(t *testing.T) {
	if err := model.ValidateTemplateFields([]model.TemplateField{textField(), selectField()}); err != nil {
		t.Fatalf("a text field beside a selector: %v", err)
	}
	// A template that asks for nothing is the ordinary template, and it
	// has to stay saveable.
	if err := model.ValidateTemplateFields(nil); err != nil {
		t.Fatalf("no fields at all: %v", err)
	}
}

func TestValidateTemplateFieldValues(t *testing.T) {
	fields := []model.TemplateField{textField(), selectField()}
	if err := model.ValidateTemplateFieldValues(fields, map[string]string{
		"dependency":  "golang.org/x/net",
		"environment": "production",
	}); err != nil {
		t.Fatalf("values the form could produce: %v", err)
	}
	// A typo'd key would otherwise substitute nothing and be noticed by
	// nobody, so it is refused to whoever is filing.
	err := model.ValidateTemplateFieldValues(fields, map[string]string{"dependancy": "x"})
	if err == nil || !strings.Contains(err.Error(), "no input field") {
		t.Errorf("unknown field: %v, want it named", err)
	}
	err = model.ValidateTemplateFieldValues(fields, map[string]string{"environment": "laptop"})
	if err == nil || !strings.Contains(err.Error(), "not one of its options") {
		t.Errorf("value outside a selector's options: %v, want it refused", err)
	}
	// Blank is always sayable: it means "take the default", which is
	// what an unattended firing gets too.
	if err := model.ValidateTemplateFieldValues(fields, map[string]string{"environment": ""}); err != nil {
		t.Errorf("a blank selector value: %v, want the default to stand", err)
	}
}

func TestValidateTemplateFieldValuesRequired(t *testing.T) {
	required := model.TemplateField{Name: "customer", Required: true}
	err := model.ValidateTemplateFieldValues([]model.TemplateField{required}, nil)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("a required field left blank: %v, want it refused", err)
	}
	if err := model.ValidateTemplateFieldValues(
		[]model.TemplateField{required}, map[string]string{"customer": "acme"}); err != nil {
		t.Errorf("a required field filled in: %v", err)
	}
	// A required field with a default is pre-filled, never blocking:
	// there is a value either way.
	withDefault := model.TemplateField{Name: "customer", Required: true, Default: "acme"}
	if err := model.ValidateTemplateFieldValues([]model.TemplateField{withDefault}, nil); err != nil {
		t.Errorf("a required field with a default: %v", err)
	}
}

func TestTemplateFieldValuesFallBackToDefaults(t *testing.T) {
	tmpl := model.Template{Fields: []model.TemplateField{textField(), selectField()}}
	got := tmpl.FieldValues(map[string]string{"dependency": "  golang.org/x/net  "})
	if got["dependency"] != "golang.org/x/net" {
		t.Errorf("dependency = %q, want it trimmed", got["dependency"])
	}
	// Nothing supplied for the selector, so the declaration's own
	// default stands -- which is the whole of what an unattended firing
	// carries.
	if got["environment"] != "staging" {
		t.Errorf("environment = %q, want the default", got["environment"])
	}
	// A value for a field this template does not declare is ignored
	// here rather than refused: the refusing is done once, where there
	// is somebody to tell.
	if _, ok := tmpl.FieldValues(map[string]string{"nonsense": "x"})["nonsense"]; ok {
		t.Errorf("an undeclared value reached the substitution")
	}
	if tmpl.FieldValues(nil)["environment"] != "staging" {
		t.Errorf("no values at all: want every field on its default")
	}
	if (model.Template{}).FieldValues(nil) != nil {
		t.Errorf("a template that asks for nothing should fill in nothing")
	}
}

func TestSubstituteFields(t *testing.T) {
	values := map[string]string{"dependency": "golang.org/x/net", "environment": "staging"}
	for _, tc := range []struct {
		in, want string
	}{
		{"Bump {{dependency}}", "Bump golang.org/x/net"},
		{"Deploy to {{ environment }}", "Deploy to staging"},
		{"{{dependency}} in {{environment}}", "golang.org/x/net in staging"},
		// A name this template does not declare is left exactly as
		// written: a body may legitimately contain another system's
		// braces -- a Go template, a Helm chart, an example of this very
		// syntax -- and mangling those would be worse than leaving a
		// typo visible.
		{"Run {{ .Values.image }}", "Run {{ .Values.image }}"},
		{"Mention {{unknown}} once", "Mention {{unknown}} once"},
		{"Nothing to fill in here", "Nothing to fill in here"},
	} {
		if got := model.SubstituteFields(tc.in, values); got != tc.want {
			t.Errorf("SubstituteFields(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// An empty value fills the hole with nothing rather than leaving the
	// braces where an agent would read them as instructions.
	if got := model.SubstituteFields("Bump {{dependency}}", map[string]string{"dependency": ""}); got != "Bump " {
		t.Errorf("empty value = %q, want the hole emptied", got)
	}
}

func TestComposeTemplateTaskFillsTheHoles(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Title = "Bump {{dependency}}"
	tmpl.Body = "Bump {{dependency}} and deploy to {{environment}}."
	tmpl.Fields = []model.TemplateField{textField(), selectField()}

	comp := model.ComposeTemplateTask(tmpl,
		map[string]string{"dependency": "golang.org/x/net"},
		composeRepo("bwsalmon", "grain"), "main", true)

	if comp.Title != "Bump golang.org/x/net" {
		t.Errorf("title = %q, want the value filled in", comp.Title)
	}
	// environment was not supplied, so the declaration's default filled
	// its hole.
	if comp.Body != "Bump golang.org/x/net and deploy to staging." {
		t.Errorf("body = %q, want both holes filled", comp.Body)
	}
	if comp.Target == nil || comp.Target.Name != "grain" || comp.Base != "main" {
		t.Errorf("placement = (%+v, %q), want the filing's own repo and branch", comp.Target, comp.Base)
	}
	if comp.Sources.Target != model.LayerFiling || comp.Sources.Approved != model.LayerFiling {
		t.Errorf("sources = %+v, want the filing to own its placement and approval", comp.Sources)
	}
	// Content is still the template's, holes and all, and so is
	// auto-merge: there is no batch policy over a single filing.
	if comp.Sources.Title != model.LayerTemplate || !comp.AutoMerge {
		t.Errorf("content = %+v, want the template's", comp.Sources)
	}
	if !comp.Approved {
		t.Errorf("approved = false, want what the filing asked for")
	}
}

// Nothing that fires unattended has anybody to ask, so every one of them
// carries the defaults -- and never a `{{name}}` into an agent's prompt.
func TestUnattendedFiringsCarryTheDefaults(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Title = "Deploy to {{environment}}"
	tmpl.Body = "Deploy to {{environment}} now."
	tmpl.Fields = []model.TemplateField{selectField()}

	sched := composeSchedule()
	for _, tc := range []struct {
		name string
		comp model.Composition
	}{
		{"schedule", model.ComposeScheduleTask(sched, &tmpl)},
		{"suite", model.ComposeSuiteTask(tmpl, composeRepo("bwsalmon", "grain"), "main", false, false, false)},
		{"qualification", model.ComposeQualificationTask(tmpl, composeRepo("bwsalmon", "grain"), "candidate", false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.comp.Title != "Deploy to staging" {
				t.Errorf("title = %q, want the default filled in", tc.comp.Title)
			}
			if strings.Contains(tc.comp.Body, "{{") {
				t.Errorf("body = %q, want no holes left for an agent to read", tc.comp.Body)
			}
		})
	}
}

func TestUnfillableRequiredField(t *testing.T) {
	fields := []model.TemplateField{
		{Name: "level", Required: true, Default: "high"},
		{Name: "note"},
		{Name: "service", Label: "Service", Required: true, Default: "  "},
	}
	f, ok := model.UnfillableRequiredField(fields)
	if !ok || f.Name != "service" {
		t.Fatalf("UnfillableRequiredField = %+v, %v, want service", f, ok)
	}
	err := model.ValidateUnattendedTemplate(fields, "a schedule")
	if err == nil || !strings.Contains(err.Error(), `field "Service" is required and has no default, so a schedule cannot fill it`) {
		t.Errorf("ValidateUnattendedTemplate = %v", err)
	}
	if err := model.ValidateUnattendedTemplate(fields[:2], "a schedule"); err != nil {
		t.Errorf("every required field has a default: %v, want nil", err)
	}
}
