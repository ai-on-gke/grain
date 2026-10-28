package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// defaultArgsSchema is what an absent inputSchema.properties.args means
// (§4.2.1): a method that takes no arguments at all.
var defaultArgsSchema = map[string]any{"type": "object", "additionalProperties": false}

// ValidateArgs validates a grant's with: object against a method's args
// schema and returns the normalized arguments (§4.2.1, §2.3.1): top-level
// defaults applied first, then type, required, enum, const, pattern,
// minimum, maximum, exclusiveMinimum, exclusiveMaximum, minLength,
// maxLength, items, minItems, maxItems, uniqueItems, additionalProperties,
// allOf, anyOf, oneOf and not enforced, with local $refs resolved into
// $defs. A nil schema is defaultArgsSchema; a nil with is {}.
//
// The schema goes through NormalizeArgsSchema first, so a schema written
// as Go literals is enforced exactly as its JSON would be, and one using a
// keyword this validator does not enforce is an error rather than a
// restriction silently dropped.
//
// with is not modified. The result is a deep copy, round-tripped through
// JSON so that every number is a float64 and two normalized objects
// compare with reflect.DeepEqual.
func ValidateArgs(schema map[string]any, with map[string]any) (map[string]any, error) {
	return validateObject("args", schema, with)
}

// validateObject is ValidateArgs with its error paths rooted at root:
// "args" for a grant's with:, "config" for a server's configuration
// (ValidateConfig).
func validateObject(root string, schema map[string]any, with map[string]any) (map[string]any, error) {
	schema, err := NormalizeArgsSchema(schema)
	if err != nil {
		return nil, err
	}
	args, err := jsonCopy(with)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", root, err)
	}
	if args == nil {
		args = map[string]any{}
	}
	obj, ok := args.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: must be an object", root)
	}
	v := schemaValidator{root: schema, depth: new(int)}
	resolved, err := v.deref(schema)
	if err != nil {
		return nil, err
	}
	if props, ok := resolved["properties"].(map[string]any); ok {
		for name, p := range props {
			if _, present := obj[name]; present {
				continue
			}
			ps, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if ps, err = v.deref(ps); err != nil {
				return nil, err
			}
			if d, ok := ps["default"]; ok {
				if obj[name], err = jsonCopy(d); err != nil {
					return nil, fmt.Errorf("%s.%s: default: %v", root, name, err)
				}
			}
		}
	}
	if err := v.validate(root, schema, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// ArgsHaveNoRequirement reports whether {} validates against the schema
// once defaults are applied -- the condition under which a method may be
// granted as a bare string (§2.2.1).
func ArgsHaveNoRequirement(schema map[string]any) bool {
	_, err := ValidateArgs(schema, nil)
	return err == nil
}

// NormalizeArgsSchema returns schema as JSON would carry it -- every
// array an []any, every object a map[string]any, every number a float64 --
// after checking that ValidateArgs enforces every keyword in it. An args
// schema is the allowlist a capability decides what a grant may mint by
// (§6.3), so a keyword the validator would skip (anyOf's siblings
// patternProperties, propertyNames, multipleOf, if/then/else, a tuple
// items, ...) is refused here rather than accepted and ignored. Annotations
// that assert nothing (title, description, default, examples, format,
// ...) are allowed. A nil schema is defaultArgsSchema.
func NormalizeArgsSchema(schema map[string]any) (map[string]any, error) {
	if schema == nil {
		return defaultArgsSchema, nil
	}
	raw, err := jsonCopy(schema)
	if err != nil {
		return nil, fmt.Errorf("schema: %v", err)
	}
	out, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema: must be an object")
	}
	if err := checkSchema("schema", out); err != nil {
		return nil, err
	}
	return out, nil
}

// annotationKeywords assert nothing about a value; format is an
// annotation by default under JSON Schema 2020-12.
var annotationKeywords = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$anchor": true,
	"title": true, "description": true, "default": true, "examples": true,
	"format": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

var schemaTypes = map[string]bool{
	"object": true, "array": true, "string": true, "boolean": true,
	"number": true, "integer": true, "null": true,
}

// checkSchema refuses any keyword of a (normalized) schema that validate
// does not enforce, and any keyword whose value is not of the shape
// validate reads, recursing into every subschema.
func checkSchema(path string, s map[string]any) error {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sub := func(name string, v any) error {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be a schema object", name)
		}
		return checkSchema(name, m)
	}
	nonNegative := func(k string, v any) error {
		n, ok := number(v)
		if !ok || n < 0 || n != math.Trunc(n) {
			return fmt.Errorf("%s.%s: must be a non-negative integer", path, k)
		}
		return nil
	}
	for _, k := range keys {
		v := s[k]
		at := path + "." + k
		switch k {
		case "type":
			switch t := v.(type) {
			case string:
				if !schemaTypes[t] {
					return fmt.Errorf("%s: unknown type %q", at, t)
				}
			case []any:
				for _, one := range t {
					name, ok := one.(string)
					if !ok || !schemaTypes[name] {
						return fmt.Errorf("%s: unknown type %v", at, one)
					}
				}
			default:
				return fmt.Errorf("%s: must be a type name or a list of them", at)
			}
		case "const":
		case "enum":
			if _, ok := v.([]any); !ok {
				return fmt.Errorf("%s: must be an array", at)
			}
		case "pattern":
			p, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s: must be a string", at)
			}
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("%s: %q: %v", at, p, err)
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
			if _, ok := number(v); !ok {
				return fmt.Errorf("%s: must be a number", at)
			}
		case "minLength", "maxLength", "minItems", "maxItems":
			if err := nonNegative(k, v); err != nil {
				return err
			}
		case "uniqueItems":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s: must be a boolean", at)
			}
		case "items", "not":
			if err := sub(at, v); err != nil {
				return err
			}
		case "properties", "$defs", "definitions":
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: must be an object", at)
			}
			names := make([]string, 0, len(m))
			for name := range m {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if err := sub(at+"."+name, m[name]); err != nil {
					return err
				}
			}
		case "required":
			req, ok := v.([]any)
			if !ok {
				return fmt.Errorf("%s: must be an array of names", at)
			}
			for _, r := range req {
				if _, ok := r.(string); !ok {
					return fmt.Errorf("%s: must be an array of names", at)
				}
			}
		case "additionalProperties":
			if _, ok := v.(bool); ok {
				continue
			}
			if err := sub(at, v); err != nil {
				return err
			}
		case "allOf", "anyOf", "oneOf":
			list, ok := v.([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("%s: must be a non-empty array of schemas", at)
			}
			for i, one := range list {
				if err := sub(fmt.Sprintf("%s[%d]", at, i), one); err != nil {
					return err
				}
			}
		case "$ref":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s: must be a string", at)
			}
		default:
			if !annotationKeywords[k] {
				return fmt.Errorf("%s: keyword is not supported; an args schema may only use keywords Grain enforces", at)
			}
		}
	}
	return nil
}

func jsonCopy(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if m, ok := out.(map[string]any); ok && m == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

type schemaValidator struct {
	root map[string]any
	// depth counts validate's nesting, so a schema whose $refs loop back
	// on themselves through allOf/anyOf/oneOf/not is an error rather than
	// a stack overflow.
	depth *int
}

// maxValidateDepth bounds validate's nesting.
const maxValidateDepth = 256

// deref follows a local "$ref": "#/$defs/<name>" (or "#/definitions/...")
// until it reaches a schema with no $ref.
func (v schemaValidator) deref(s map[string]any) (map[string]any, error) {
	for depth := 0; ; depth++ {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s, nil
		}
		if depth > 32 {
			return nil, fmt.Errorf("schema: $ref %q nests too deeply", ref)
		}
		var target any = v.root
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("schema: only local $refs are supported, not %q", ref)
		}
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, ok := target.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("schema: $ref %q does not resolve", ref)
			}
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			target = m[part]
		}
		next, ok := target.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema: $ref %q does not resolve", ref)
		}
		s = next
	}
}

func (v schemaValidator) validate(path string, s map[string]any, val any) error {
	if *v.depth++; *v.depth > maxValidateDepth {
		return fmt.Errorf("%s: schema nests too deeply", path)
	}
	defer func() { *v.depth-- }()
	// A $ref's siblings apply alongside its target (2020-12), so the
	// target is validated and then the rest of s is.
	if _, ok := s["$ref"]; ok {
		target, err := v.deref(s)
		if err != nil {
			return err
		}
		if err := v.validate(path, target, val); err != nil {
			return err
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, one := range all {
			sub, _ := one.(map[string]any)
			if err := v.validate(path, sub, val); err != nil {
				return err
			}
		}
	}
	if anyOf, ok := s["anyOf"].([]any); ok {
		var first error
		for _, one := range anyOf {
			sub, _ := one.(map[string]any)
			err := v.validate(path, sub, val)
			if err == nil {
				first = nil
				break
			}
			if first == nil {
				first = err
			}
		}
		if first != nil {
			return fmt.Errorf("%s: matches none of anyOf (first: %v)", path, first)
		}
	}
	if one, ok := s["oneOf"].([]any); ok {
		matched := 0
		for _, o := range one {
			sub, _ := o.(map[string]any)
			if v.validate(path, sub, val) == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("%s: must match exactly one of oneOf, matches %d", path, matched)
		}
	}
	if not, ok := s["not"].(map[string]any); ok {
		if v.validate(path, not, val) == nil {
			return fmt.Errorf("%s: must not match the schema under not", path)
		}
	}
	if t, ok := s["type"]; ok {
		if !typeMatches(t, val) {
			return fmt.Errorf("%s: must be of type %v", path, t)
		}
	}
	if c, ok := s["const"]; ok && !jsonEqual(c, val) {
		return fmt.Errorf("%s: must be %v", path, c)
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, want := range e {
			if jsonEqual(want, val) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: must be one of %v", path, e)
		}
	}
	switch x := val.(type) {
	case string:
		n := float64(utf8.RuneCountInString(x))
		if m, ok := number(s["minLength"]); ok && n < m {
			return fmt.Errorf("%s: must be at least %v characters", path, m)
		}
		if m, ok := number(s["maxLength"]); ok && n > m {
			return fmt.Errorf("%s: must be at most %v characters", path, m)
		}
		if p, ok := s["pattern"].(string); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				return fmt.Errorf("%s: schema pattern %q: %v", path, p, err)
			}
			if !re.MatchString(x) {
				return fmt.Errorf("%s: %q does not match %s", path, x, p)
			}
		}
	case float64:
		if m, ok := number(s["minimum"]); ok && x < m {
			return fmt.Errorf("%s: must be at least %v", path, m)
		}
		if m, ok := number(s["maximum"]); ok && x > m {
			return fmt.Errorf("%s: must be at most %v", path, m)
		}
		if m, ok := number(s["exclusiveMinimum"]); ok && x <= m {
			return fmt.Errorf("%s: must be greater than %v", path, m)
		}
		if m, ok := number(s["exclusiveMaximum"]); ok && x >= m {
			return fmt.Errorf("%s: must be less than %v", path, m)
		}
	case []any:
		n := float64(len(x))
		if m, ok := number(s["minItems"]); ok && n < m {
			return fmt.Errorf("%s: must have at least %v items", path, m)
		}
		if m, ok := number(s["maxItems"]); ok && n > m {
			return fmt.Errorf("%s: must have at most %v items", path, m)
		}
		if u, _ := s["uniqueItems"].(bool); u {
			for i := range x {
				for j := i + 1; j < len(x); j++ {
					if jsonEqual(x[i], x[j]) {
						return fmt.Errorf("%s: items %d and %d are the same", path, i, j)
					}
				}
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, item := range x {
				if err := v.validate(fmt.Sprintf("%s[%d]", path, i), items, item); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, present := x[name]; !present {
					return fmt.Errorf("%s.%s: is required", path, name)
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					return fmt.Errorf("%s.%s: is not an accepted argument", path, k)
				}
				if aps, ok := s["additionalProperties"].(map[string]any); ok {
					if err := v.validate(path+"."+k, aps, x[k]); err != nil {
						return err
					}
				}
				continue
			}
			if err := v.validate(path+"."+k, ps, x[k]); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMatches(t any, val any) bool {
	switch tt := t.(type) {
	case string:
		return oneTypeMatches(tt, val)
	case []any:
		for _, one := range tt {
			if s, ok := one.(string); ok && oneTypeMatches(s, val) {
				return true
			}
		}
		return false
	}
	return true
}

func oneTypeMatches(t string, val any) bool {
	switch t {
	case "object":
		_, ok := val.(map[string]any)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "number":
		_, ok := val.(float64)
		return ok
	case "integer":
		f, ok := val.(float64)
		return ok && f == math.Trunc(f)
	case "null":
		return val == nil
	}
	return false
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func jsonEqual(a, b any) bool {
	ca, errA := jsonCopy(a)
	cb, errB := jsonCopy(b)
	return errA == nil && errB == nil && reflect.DeepEqual(ca, cb)
}
