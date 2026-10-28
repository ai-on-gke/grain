package ui

import (
	"encoding/json"
	"fmt"
)

// What a task's custom field values look like on the wire.
//
// A field may hold one value or a set of them (model.CustomField.
// Multiple), so a value here is either a string or an array of strings
// -- and which of the two it arrives as says nothing about the
// declaration: that is what the declaration is for
// (CustomFieldDeclaration.Multiple, on GET /api/config). A caller may
// send either shape for either kind of field, and gets back a string
// whenever there is exactly one value to send.
//
// That last rule is why this is not simply an array everywhere. Before a
// field could hold more than one value, `customFields` was a map of
// plain strings, and every reader of this API -- the frontend included
// -- was written against that. Emitting a bare string for the one-value
// case keeps all of them right, and leaves the array for what is
// genuinely new: a field carrying several. ui/src/taskAttributes.js's
// customAttribute already reads both.

// CustomFieldValue is one field's values on the wire: a JSON string when
// there is exactly one, and an array of strings otherwise.
type CustomFieldValue []string

// MarshalJSON writes the one-value case as a bare string -- see this
// file's own doc comment on why the wire is not simply an array.
//
// A value with nothing in it is written as an empty array rather than as
// "": nothing stores one (model.NormalizeCustomFieldValues drops it), so
// this is only reachable by a caller building a request, and an empty
// array is the shape that cannot be mistaken for a value.
func (v CustomFieldValue) MarshalJSON() ([]byte, error) {
	if len(v) == 1 {
		return json.Marshal(v[0])
	}
	return json.Marshal([]string(v))
}

// UnmarshalJSON accepts either shape, and nothing else: a string is one
// value, an array is however many it holds, and JSON null is none at all
// (which is how a request clears a field, exactly as "" was).
func (v *CustomFieldValue) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*v = CustomFieldValue{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*v = CustomFieldValue(many)
		return nil
	}
	if string(data) == "null" {
		*v = nil
		return nil
	}
	return fmt.Errorf("a custom field value must be a string or an array of strings, not %s", data)
}

// customFieldValues is what the store holds, as this package sends it.
// nil for a task carrying nothing, so that "says nothing about any
// field" stays a key that is simply absent (model.Task.CustomFields).
func customFieldValues(values map[string][]string) map[string]CustomFieldValue {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]CustomFieldValue, len(values))
	for name, v := range values {
		out[name] = CustomFieldValue(v)
	}
	return out
}

// customFieldModelValues is customFieldValues' inverse: what a request
// says, in the shape model.NormalizeCustomFieldValues checks.
func customFieldModelValues(values map[string]CustomFieldValue) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string][]string, len(values))
	for name, v := range values {
		out[name] = []string(v)
	}
	return out
}
