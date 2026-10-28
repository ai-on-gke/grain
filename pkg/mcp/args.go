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

// Helpers for pulling typed values out of a tool call's arguments, which
// arrive as map[string]any straight out of encoding/json -- numbers as
// float64, nothing as json.Number, since Handle unmarshals into `any`.

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func argFloat(args map[string]any, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

func argBool(args map[string]any, key string) (bool, bool) {
	v, ok := args[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func argStringMap(args map[string]any, key string) map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		if m, ok := v.(map[string]string); ok {
			return m
		}
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

// argCustomFields is propose_task's custom_fields: an object keyed by
// field name whose values are a string or an array of them, which is
// exactly what a value is on the task API too (ui.CustomFieldValue),
// because a field this deployment declared `multiple` holds a set
// (model.CustomField.Multiple) and one that did not holds one.
//
// A value that is neither is skipped rather than coerced: an agent that
// wrote a number where the schema asks for text has said something this
// process cannot repair, and the one place that can tell it apart from a
// value that simply fails its declaration is the relay, which drops it
// with a note either way (orchestrator.relayProposedTasks).
func argCustomFields(args map[string]any, key string) map[string][]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var out map[string][]string
	for name, item := range raw {
		values, ok := customFieldValue(item)
		if !ok {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[name] = values
	}
	return out
}

// customFieldValue is one such value: a bare string is a set of one, an
// array is itself, and an array with a non-string in it is refused
// whole rather than silently shortened -- half of what the run asked for
// is not what it asked for.
func customFieldValue(item any) ([]string, bool) {
	if s, ok := item.(string); ok {
		return []string{s}, true
	}
	if slice, ok := item.([]string); ok {
		return slice, true
	}
	raw, ok := item.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, one := range raw {
		s, ok := one.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// argStrings is a list of words an argument may give either way round:
// several of them, or one on its own.
//
// Both, because both are what a caller actually writes. A schema that
// only took an array made every single-valued call ceremonial
// (conclusion: ["failure"]), and one that only took a string left the
// commonest question of all -- "what is red", which is failure *and*
// errored -- unaskable. Empty words are dropped rather than becoming a
// condition nothing can satisfy.
func argStrings(args map[string]any, key string) []string {
	if s, ok := argString(args, key); ok {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []string{strings.TrimSpace(s)}
	}
	out := make([]string, 0, 4)
	for _, word := range argStringSlice(args, key) {
		if strings.TrimSpace(word) != "" {
			out = append(out, strings.TrimSpace(word))
		}
	}
	return out
}

func argStringSlice(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	if slice, ok := v.([]string); ok {
		return slice
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func argWrites(args map[string]any, key string) []WriteTarget {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	if wt, ok := v.([]WriteTarget); ok {
		return wt
	}
	var rawItems []any
	switch val := v.(type) {
	case []any:
		rawItems = val
	case []map[string]any:
		for _, m := range val {
			rawItems = append(rawItems, m)
		}
	}
	if rawItems == nil {
		return nil
	}
	out := make([]WriteTarget, 0, len(rawItems))
	for _, item := range rawItems {
		if wt, ok := item.(WriteTarget); ok {
			if strings.TrimSpace(wt.Repo) != "" {
				out = append(out, WriteTarget{
					Repo: strings.TrimSpace(wt.Repo),
					Base: strings.TrimSpace(wt.Base),
				})
			}
			continue
		}
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		repo, _ := obj["repo"].(string)
		base, _ := obj["base"].(string)
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		out = append(out, WriteTarget{
			Repo: repo,
			Base: strings.TrimSpace(base),
		})
	}
	return out
}

// argCapabilities reads a capabilities: list argument: in-process names,
// capability references, and {capability, with} grant objects, each as
// the grant string it stands for (model.CapabilityEntries).
func argCapabilities(args map[string]any, key string) ([]string, error) {
	return model.CapabilityEntries(args[key])
}
