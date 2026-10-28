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
	"reflect"
	"testing"
)

func TestArgString(t *testing.T) {
	args := map[string]any{
		"text":   "hello",
		"number": 42.0,
		"bool":   true,
	}
	if s, ok := argString(args, "text"); !ok || s != "hello" {
		t.Errorf("argString(text) = %q, %v, want hello, true", s, ok)
	}
	if s, ok := argString(args, "number"); ok || s != "" {
		t.Errorf("argString(number) = %q, %v, want empty, false", s, ok)
	}
	if s, ok := argString(args, "missing"); ok || s != "" {
		t.Errorf("argString(missing) = %q, %v, want empty, false", s, ok)
	}
}

func TestArgFloat(t *testing.T) {
	args := map[string]any{
		"num":     123.45,
		"int":     42,
		"int64":   int64(100),
		"float32": float32(3.5),
		"text":    "123",
	}
	if f, ok := argFloat(args, "num"); !ok || f != 123.45 {
		t.Errorf("argFloat(num) = %v, %v, want 123.45, true", f, ok)
	}
	if f, ok := argFloat(args, "int"); !ok || f != 42.0 {
		t.Errorf("argFloat(int) = %v, %v, want 42.0, true", f, ok)
	}
	if f, ok := argFloat(args, "int64"); !ok || f != 100.0 {
		t.Errorf("argFloat(int64) = %v, %v, want 100.0, true", f, ok)
	}
	if f, ok := argFloat(args, "float32"); !ok || f != 3.5 {
		t.Errorf("argFloat(float32) = %v, %v, want 3.5, true", f, ok)
	}
	if f, ok := argFloat(args, "text"); ok || f != 0 {
		t.Errorf("argFloat(text) = %v, %v, want 0, false", f, ok)
	}
	if f, ok := argFloat(args, "missing"); ok || f != 0 {
		t.Errorf("argFloat(missing) = %v, %v, want 0, false", f, ok)
	}
}

func TestArgInt(t *testing.T) {
	args := map[string]any{
		"int":     42,
		"int64":   int64(100),
		"float":   4096.0,
		"float32": float32(8.0),
		"text":    "123",
	}
	if i, ok := argInt(args, "int"); !ok || i != 42 {
		t.Errorf("argInt(int) = %v, %v, want 42, true", i, ok)
	}
	if i, ok := argInt(args, "int64"); !ok || i != 100 {
		t.Errorf("argInt(int64) = %v, %v, want 100, true", i, ok)
	}
	if i, ok := argInt(args, "float"); !ok || i != 4096 {
		t.Errorf("argInt(float) = %v, %v, want 4096, true", i, ok)
	}
	if i, ok := argInt(args, "float32"); !ok || i != 8 {
		t.Errorf("argInt(float32) = %v, %v, want 8, true", i, ok)
	}
	if i, ok := argInt(args, "text"); ok || i != 0 {
		t.Errorf("argInt(text) = %v, %v, want 0, false", i, ok)
	}
	if i, ok := argInt(args, "missing"); ok || i != 0 {
		t.Errorf("argInt(missing) = %v, %v, want 0, false", i, ok)
	}
}

func TestArgBool(t *testing.T) {
	args := map[string]any{
		"flag": true,
		"num":  1.0,
	}
	if b, ok := argBool(args, "flag"); !ok || !b {
		t.Errorf("argBool(flag) = %v, %v, want true, true", b, ok)
	}
	if b, ok := argBool(args, "num"); ok || b {
		t.Errorf("argBool(num) = %v, %v, want false, false", b, ok)
	}
	if b, ok := argBool(args, "missing"); ok || b {
		t.Errorf("argBool(missing) = %v, %v, want false, false", b, ok)
	}
}

func TestArgCustomFields(t *testing.T) {
	args := map[string]any{
		"valid": map[string]any{
			"team":      "infrastructure",
			"tags":      []any{"backend", "urgent"},
			"direct":    []string{"foo", "bar"},
			"bad_array": []any{"valid", 123},
			"bad_type":  42,
		},
		"not_a_map": "string_val",
	}

	got := argCustomFields(args, "valid")
	want := map[string][]string{
		"team":   {"infrastructure"},
		"tags":   {"backend", "urgent"},
		"direct": {"foo", "bar"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argCustomFields(valid) = %+v, want %+v", got, want)
	}

	if got := argCustomFields(args, "not_a_map"); got != nil {
		t.Errorf("argCustomFields(not_a_map) = %+v, want nil", got)
	}
	if got := argCustomFields(args, "missing"); got != nil {
		t.Errorf("argCustomFields(missing) = %+v, want nil", got)
	}
}

func TestArgStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		key  string
		want []string
	}{
		{
			name: "single string",
			args: map[string]any{"key": " single "},
			key:  "key",
			want: []string{"single"},
		},
		{
			name: "empty string",
			args: map[string]any{"key": "   "},
			key:  "key",
			want: nil,
		},
		{
			name: "any slice with strings and empties",
			args: map[string]any{"key": []any{"a", "  ", "b", 123}},
			key:  "key",
			want: []string{"a", "b"},
		},
		{
			name: "string slice directly",
			args: map[string]any{"key": []string{"x", " y ", ""}},
			key:  "key",
			want: []string{"x", "y"},
		},
		{
			name: "missing key",
			args: map[string]any{},
			key:  "key",
			want: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := argStrings(tc.args, tc.key)
			if !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Errorf("argStrings() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestArgWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		key  string
		want []WriteTarget
	}{
		{
			name: "typed slice",
			args: map[string]any{
				"writes": []WriteTarget{
					{Repo: "owner/repo1", Base: "main"},
					{Repo: "owner/repo2", Base: "feature"},
				},
			},
			key: "writes",
			want: []WriteTarget{
				{Repo: "owner/repo1", Base: "main"},
				{Repo: "owner/repo2", Base: "feature"},
			},
		},
		{
			name: "any slice with map[string]any",
			args: map[string]any{
				"writes": []any{
					map[string]any{"repo": "owner/repo1", "base": "main"},
					map[string]any{"repo": " owner/repo2 ", "base": " feature "},
					map[string]any{"repo": ""},
					"invalid_entry",
				},
			},
			key: "writes",
			want: []WriteTarget{
				{Repo: "owner/repo1", Base: "main"},
				{Repo: "owner/repo2", Base: "feature"},
			},
		},
		{
			name: "slice of maps",
			args: map[string]any{
				"writes": []map[string]any{
					{"repo": "owner/repo1", "base": "dev"},
				},
			},
			key: "writes",
			want: []WriteTarget{
				{Repo: "owner/repo1", Base: "dev"},
			},
		},
		{
			name: "missing or nil",
			args: map[string]any{"writes": nil},
			key:  "writes",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := argWrites(tc.args, tc.key)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argWrites() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
