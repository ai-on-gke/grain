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

package mcp

import (
	"fmt"
	"regexp"
	"sort"
)

// FunctionDeclarationProblems lists every way one MCP tool declaration,
// its schema as JSON decodes it (a tools/list answer, or a Go literal
// round-tripped through encoding/json),
// would be refused by a model provider in the deployment's default_model
// chain: Gemini (agy, and Gemini's function-declaration Schema, an
// OpenAPI 3.0 subset) and Claude (tool names and a JSON Schema whose
// root is an object). An empty answer means both accept it.
//
// It exists because a refused declaration fails the whole tools list,
// not the one tool, and fails it only against a real provider: the
// grants array that shipped without "items" in v3.5.0 (#4225) passed
// every scripted test and broke every Gemini run that held an api:*
// grant. The rules here are the intersection of what both providers
// document, so a declaration that passes is one neither refuses:
//
//   - the name is 1..64 of [A-Za-z0-9_-], starting with a letter or
//     underscore (Claude allows no '.' or ':', Gemini no leading digit);
//   - the root schema is "type": "object";
//   - every "type" is one of Gemini's six (a ["T", "null"] pair is
//     allowed, being how nullable is written);
//   - a "format" is one Gemini lists for that type;
//   - every "array" has an object "items";
//   - "enum" values are strings, on a string schema;
//   - every "required" name is a declared property;
//   - no keyword outside Gemini's Schema ($ref, oneOf, allOf, const ...)
//     is used.
func FunctionDeclarationProblems(name string, schema map[string]any) []string {
	var out []string
	if !declNamePattern.MatchString(name) {
		out = append(out, fmt.Sprintf("name %q must match %s (Claude and Gemini both limit tool names to 64 of [A-Za-z0-9_-], starting with a letter or underscore)", name, declNamePattern))
	}
	if schema == nil {
		return out
	}
	if t, _ := schema["type"].(string); t != "object" {
		out = append(out, fmt.Sprintf("inputSchema: the root must be \"type\": \"object\" (Claude refuses anything else), got %v", schema["type"]))
	}
	checkDeclSchema("inputSchema", schema, &out)
	return out
}

// ArrayItemsProblems is the one rule of FunctionDeclarationProblems that
// also binds a schema grain validates itself rather than hands a model --
// a capability method's with: arguments, which may use oneOf or not but
// still reach Gemini whenever a tool mirrors them -- so path's arrays all
// need an object "items".
func ArrayItemsProblems(path string, node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		if hasArrayType(v["type"]) {
			if _, ok := v["items"].(map[string]any); !ok {
				out = append(out, path+": array schema must define an object \"items\" field")
			}
		}
		var keys []string
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, ArrayItemsProblems(path+"."+k, v[k])...)
		}
	case []any:
		for i, child := range v {
			out = append(out, ArrayItemsProblems(fmt.Sprintf("%s[%d]", path, i), child)...)
		}
	}
	return out
}

func hasArrayType(raw any) bool {
	switch typ := raw.(type) {
	case string:
		return typ == "array"
	case []any:
		for _, e := range typ {
			if s, _ := e.(string); s == "array" {
				return true
			}
		}
	}
	return false
}

var declNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// declTypes are Gemini's Schema types; "null" is only ever half of a
// nullable pair.
var declTypes = map[string]bool{
	"string": true, "number": true, "integer": true,
	"boolean": true, "array": true, "object": true,
}

// declFormats are the formats Gemini's Schema documents, per type.
var declFormats = map[string]map[string]bool{
	"string":  {"enum": true, "date-time": true},
	"number":  {"float": true, "double": true},
	"integer": {"int32": true, "int64": true},
}

// declKeywords are the Schema fields Gemini accepts. Claude accepts any
// JSON Schema, so this is the narrower of the two. additionalProperties
// is tolerated: the Gemini SDKs and agy drop it rather than refuse it,
// and grain's schemas use it to say "no extras".
var declKeywords = map[string]bool{
	"type": true, "format": true, "title": true, "description": true,
	"nullable": true, "enum": true, "default": true, "example": true,
	"items": true, "minItems": true, "maxItems": true,
	"properties": true, "required": true, "propertyOrdering": true,
	"minProperties": true, "maxProperties": true, "additionalProperties": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true, "anyOf": true,
}

func checkDeclSchema(path string, node map[string]any, out *[]string) {
	add := func(format string, args ...any) {
		*out = append(*out, path+": "+fmt.Sprintf(format, args...))
	}
	var keys []string
	for k := range node {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !declKeywords[k] {
			add("keyword %q is not part of Gemini's function-declaration Schema", k)
		}
	}

	typ := ""
	switch t := node["type"].(type) {
	case nil:
		// An untyped schema ({} for "any value") is accepted by both:
		// it is how #4225 gave grants its items.
	case string:
		typ = t
	case []any:
		for _, e := range t {
			s, _ := e.(string)
			switch {
			case s == "null":
			case typ == "" && declTypes[s]:
				typ = s
			default:
				add("type list %v must be a single type, optionally paired with \"null\"", t)
			}
		}
	default:
		add("\"type\" must be a string, got %T", t)
	}
	if typ != "" && !declTypes[typ] {
		add("type %q is not one of Gemini's string/number/integer/boolean/array/object", typ)
	}
	if f, ok := node["format"]; ok {
		s, _ := f.(string)
		if !declFormats[typ][s] {
			add("format %v is not one Gemini supports on a %q", f, typ)
		}
	}
	if e, ok := node["enum"]; ok {
		vals, isList := e.([]any)
		if !isList {
			add("\"enum\" must be a list")
		}
		for _, v := range vals {
			if _, ok := v.(string); !ok {
				add("enum value %v is not a string (Gemini enums are strings)", v)
				break
			}
		}
		if typ != "" && typ != "string" {
			add("\"enum\" on a %q schema (Gemini allows enums only on strings)", typ)
		}
	}

	if typ == "array" {
		items, ok := node["items"].(map[string]any)
		if !ok {
			add("array schema must define an object \"items\" field")
		} else {
			checkDeclSchema(path+".items", items, out)
		}
	}
	props, _ := node["properties"].(map[string]any)
	if p, ok := node["properties"]; ok && props == nil {
		add("\"properties\" must be an object, got %T", p)
	}
	var names []string
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		child, ok := props[k].(map[string]any)
		if !ok {
			add("property %q must be a schema object, got %T", k, props[k])
			continue
		}
		checkDeclSchema(path+".properties."+k, child, out)
	}
	if r, ok := node["required"]; ok {
		req, isList := r.([]any)
		if !isList {
			add("\"required\" must be a list")
		}
		for _, v := range req {
			s, _ := v.(string)
			if _, declared := props[s]; !declared {
				add("required %v is not a declared property (Gemini refuses it)", v)
			}
		}
	}
	if a, ok := node["additionalProperties"].(map[string]any); ok {
		checkDeclSchema(path+".additionalProperties", a, out)
	}
	if anyOf, ok := node["anyOf"]; ok {
		alts, isList := anyOf.([]any)
		if !isList {
			add("\"anyOf\" must be a list")
		}
		for i, alt := range alts {
			m, ok := alt.(map[string]any)
			if !ok {
				add("anyOf[%d] must be a schema object", i)
				continue
			}
			checkDeclSchema(fmt.Sprintf("%s.anyOf[%d]", path, i), m, out)
		}
	}
}
