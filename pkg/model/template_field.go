package model

import (
	"fmt"
	"regexp"
	"strings"
)

// A template's input fields are the blanks it deliberately leaves
// (grain/task-58). A template's title and body are written once and
// filed many times, and most reusable work is *nearly* the same every
// time: "upgrade <dependency> to <version>", "write the release notes
// for <repo>", "triage the <severity> reports". Without somewhere to
// say which words change, either every variant needs a template of its
// own or whoever instantiates one edits prose by hand at the moment they
// are trying to file quickly.
//
// A field is therefore a named hole in the content -- `{{dependency}}`
// -- and a declaration of how it is filled: bare text, or one of a set
// of options, which is what makes filling a template in a matter of
// picking from two dropdowns rather than re-reading a paragraph. The
// values are supplied at the point of use (ui.CreateTaskRequest's own
// TemplateID and Fields, and the composition preview beside the form
// that collects them); the substitution happens once, in
// SubstituteFields, so what a preview shows and what a filing writes
// cannot disagree.
//
// Nothing that fires unattended -- a schedule, a suite pass, a
// qualification run, a review -- has anybody to ask, so those get each
// field's Default (Template.FieldValues with no values at all). That is
// also why a field with no default is not an error at the point the
// template is saved: a template whose fields are all optional prose is
// as legitimate as one that is only ever filed by hand, and the firing
// simply carries the empty string where the hole was rather than the
// `{{name}}` that would otherwise reach an agent's prompt.

// templateFieldName is what an input field may be called, and it is
// customFieldName's own alphabet for its own reasons: the name is a key
// -- in the store, in the JSON on the wire, in the `{{name}}` written
// into the template's own prose, and in the query string of the
// composition preview -- so it has to survive being written down in all
// of those without quoting. Label is where the spaces and the capital
// letters go.
var templateFieldName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// templatePlaceholder is a hole in a template's title or body:
// `{{name}}`, with whitespace inside the braces tolerated because
// somebody will write `{{ name }}` and mean the same thing. Only a name
// this template actually declares is substituted (SubstituteFields), so
// a body that happens to contain braces of another system's -- a Go
// template, a Helm chart, an example of this very syntax -- is left
// exactly as written.
var templatePlaceholder = regexp.MustCompile(`\{\{\s*([a-z0-9][a-z0-9_-]*)\s*\}\}`)

// TemplateField is one input a template asks for when somebody
// instantiates it.
type TemplateField struct {
	// Name is the key: what `{{name}}` in the title and body refers to,
	// and what a caller supplying a value names it by. Unique within one
	// template.
	Name string
	// Label is what a human reads over the input -- "Dependency to
	// upgrade" for `dependency`. Empty means the name itself will do
	// (DisplayName), which for a short name it usually does.
	Label string
	// Options, when there are any, make this field a selector: the
	// values offered, in the declared order, and the only ones a caller
	// may supply (ValidateTemplateFieldValues). No options at all is the
	// other kind of field, and the default one: bare text, whatever the
	// person filling it in types.
	//
	// A list rather than a separate type-and-options pair, for the
	// reason model.CustomField's own Type/Options split does *not* apply
	// here: a template field has exactly two shapes and "has choices" is
	// the whole of the difference between them, so a type column would
	// be a second place to contradict this list.
	Options []string
	// Default is what this field is worth when nobody supplies a value:
	// what an unattended firing carries (Template.FieldValues), and what
	// a form filling this template in starts from. It must be one of
	// Options when there are any -- a default nothing offers is a value
	// the picker could never produce.
	Default string
	// Required refuses to file a task with this field left blank
	// (ValidateTemplateFieldValues). It is about the human at the form,
	// not about the template: an unattended firing has nobody to refuse
	// to, so it carries the default exactly as an optional field does.
	// A required field with a default is therefore only ever
	// pre-filled, never blocking -- and one without a default cannot be
	// attached to a schedule or suite at all (ValidateUnattendedTemplate),
	// since every firing of it would substitute nothing.
	Required bool
}

// DisplayName is what to put over the input -- CustomField.DisplayName's
// own rule, for the same reason.
func (f TemplateField) DisplayName() string {
	if strings.TrimSpace(f.Label) != "" {
		return f.Label
	}
	return f.Name
}

// Selector reports whether this field offers a set of choices rather
// than a text box -- which is exactly whether it declares any options.
func (f TemplateField) Selector() bool { return len(f.Options) > 0 }

// Validate checks one declared field, and reports what is wrong with it
// in words the person writing the template can act on.
func (f TemplateField) Validate() error {
	if !templateFieldName.MatchString(f.Name) {
		return fmt.Errorf(
			"field name %q: use lower-case letters, digits, dash and underscore, starting with a letter or a digit",
			f.Name)
	}
	seen := map[string]bool{}
	for _, opt := range f.Options {
		if strings.TrimSpace(opt) == "" {
			return fmt.Errorf("field %q: an option cannot be blank", f.DisplayName())
		}
		if seen[opt] {
			return fmt.Errorf("field %q: option %q is listed twice", f.DisplayName(), opt)
		}
		seen[opt] = true
	}
	if f.Default != "" && f.Selector() && !seen[f.Default] {
		return fmt.Errorf("field %q: default %q is not one of its options", f.DisplayName(), f.Default)
	}
	return nil
}

// ValidateTemplateFields checks a whole declaration: every field on its
// own, and no two of them sharing a name. Called where a template is
// saved (ui.CreateTemplate, ui.UpdateTemplate) so a hole nothing could
// ever fill is refused to the person writing it rather than discovered
// by whoever reaches for the template months later.
func ValidateTemplateFields(fields []TemplateField) error {
	seen := map[string]bool{}
	for _, f := range fields {
		if err := f.Validate(); err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("field %q is declared twice", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

// ValidateTemplateFieldValues checks what one instantiation supplies
// against what the template declares: a value for a field nothing
// declares is a typo that would otherwise substitute nothing and be
// noticed by nobody, a value outside a selector's options is a choice
// that picker could not have produced, and a required field left blank
// is the one thing a template can insist on.
//
// Only for callers with somebody to tell -- the API request a human or
// an agent is waiting on. The unattended firings never come through
// here, for the reason this file's own preamble gives.
func ValidateTemplateFieldValues(fields []TemplateField, values map[string]string) error {
	byName := make(map[string]TemplateField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	// Sorted so a request with two bad values fails on the same one
	// every time (NormalizeCustomFieldValues' own reasoning).
	for _, name := range sortedKeys(values) {
		f, ok := byName[name]
		if !ok {
			return fmt.Errorf("this template has no input field %q", name)
		}
		value := strings.TrimSpace(values[name])
		if value == "" || !f.Selector() {
			continue
		}
		if !contains(f.Options, value) {
			return fmt.Errorf("field %q: %q is not one of its options (%s)",
				f.DisplayName(), value, strings.Join(f.Options, ", "))
		}
	}
	for _, f := range fields {
		if !f.Required {
			continue
		}
		if strings.TrimSpace(values[f.Name]) == "" && strings.TrimSpace(f.Default) == "" {
			return fmt.Errorf("field %q is required", f.DisplayName())
		}
	}
	return nil
}

// UnfillableRequiredField is the first of fields that is Required and
// has no Default: the one a schedule or suite cannot fill. Those fire
// with nobody to ask (FieldValues with nil values), so such a field
// would substitute nothing on every firing -- "Audit  for X" -- while
// the same template refuses a human leaving it blank
// (grain/task-2433). ok is false when every required field has a
// default to fall back on.
func UnfillableRequiredField(fields []TemplateField) (TemplateField, bool) {
	for _, f := range fields {
		if f.Required && strings.TrimSpace(f.Default) == "" {
			return f, true
		}
	}
	return TemplateField{}, false
}

// ValidateUnattendedTemplate refuses fields that a schedule or a suite
// could not fill (UnfillableRequiredField), in words saying what would
// let it -- for the place a template is attached to one, and for an
// edit of a template already attached.
func ValidateUnattendedTemplate(fields []TemplateField, user string) error {
	f, ok := UnfillableRequiredField(fields)
	if !ok {
		return nil
	}
	return fmt.Errorf(
		"field %q is required and has no default, so %s cannot fill it -- give it a default or untick Required",
		f.DisplayName(), user)
}

func contains(options []string, value string) bool {
	for _, opt := range options {
		if opt == value {
			return true
		}
	}
	return false
}

// FieldValues is what each of this template's input fields is actually
// worth for one instantiation: the value supplied for it, trimmed, and
// its Default wherever nothing was supplied -- which, for every
// unattended firing, is all of them (values nil).
//
// Values naming fields this template does not declare are ignored here
// rather than refused: the refusing is
// ValidateTemplateFieldValues' job, done once where there is somebody to
// hand the message to, and doing it again here would mean a firing could
// fail on a value the store still holds.
func (t Template) FieldValues(values map[string]string) map[string]string {
	if len(t.Fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(t.Fields))
	for _, f := range t.Fields {
		value := strings.TrimSpace(values[f.Name])
		if value == "" {
			value = f.Default
		}
		out[f.Name] = value
	}
	return out
}

// FilledTitleBody is t's Title and Body with every input field at its
// Default -- what an unattended firing of t files (composeTemplate with
// values nil), and so what a template-backed schedule caches for display
// (Schedule.TemplateID's own doc comment). Copying the raw Title instead
// listed a schedule as "Audit {{service}}" while every task it filed read
// "Audit api" (grain/task-1425).
func (t Template) FilledTitleBody() (title, body string) {
	filled := t.FieldValues(nil)
	return SubstituteFields(t.Title, filled), SubstituteFields(t.Body, filled)
}

// SubstituteFields fills the holes in s: every `{{name}}` naming a field
// in values becomes that field's value, and every other brace pair is
// left exactly as written (templatePlaceholder's own doc comment on
// why).
func SubstituteFields(s string, values map[string]string) string {
	if len(values) == 0 || !strings.Contains(s, "{{") {
		return s
	}
	return templatePlaceholder.ReplaceAllStringFunc(s, func(match string) string {
		name := templatePlaceholder.FindStringSubmatch(match)[1]
		value, ok := values[name]
		if !ok {
			return match
		}
		return value
	})
}
