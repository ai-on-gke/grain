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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const testChangeTargetKind = "monorepo"

func TestProposedChangeTargetReadsRegisteredArgument(t *testing.T) {
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: testChangeTargetKind}))

	for name, c := range map[string]struct {
		arg      any
		wantKind string
		wantSpec string
		err      string
	}{
		"absent":    {arg: nil},
		"false":     {arg: false},
		"true":      {arg: true, wantKind: testChangeTargetKind},
		"empty":     {arg: map[string]any{}, wantKind: testChangeTargetKind},
		"spec":      {arg: map[string]any{"workspace": "w", "start_cl": float64(12)}, wantKind: testChangeTargetKind, wantSpec: `{"start_cl":12,"workspace":"w"}`},
		"not a map": {arg: "w", err: "must be an object"},
	} {
		args := map[string]any{}
		if c.arg != nil {
			args[testChangeTargetKind] = c.arg
		}
		ct, err := ProposedChangeTarget(args)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: error %v, want %q", name, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if c.wantKind == "" {
			if ct != nil {
				t.Errorf("%s: change target %+v, want none", name, ct)
			}
			continue
		}
		if ct == nil || ct.Kind != c.wantKind || ct.SpecString() != c.wantSpec {
			t.Errorf("%s: change target %+v (spec %q), want %s %q", name, ct, ct.SpecString(), c.wantKind, c.wantSpec)
		}
	}
}

func TestProposeTaskRefusesChangeTargetWithGitArguments(t *testing.T) {
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: testChangeTargetKind}))

	var propose Tool
	for _, tool := range NewMockTools(&MockSink{}, nil) {
		if tool.Name == "propose_task" {
			propose = tool
		}
	}
	if propose.Handler == nil {
		t.Fatal("no propose_task tool")
	}
	for _, extra := range []map[string]any{
		{"repo": "acme/widgets"},
		{"branch": "main"},
		{"no_repo": true},
		{"writes": []any{map[string]any{"repo": "acme/widgets"}}},
		{"fixes_base_branch": true},
		{"auto_merge": true},
		{"goal": map[string]any{"kind": "base-green", "spec": map[string]any{"repo": "a/b", "branch": "main"}}},
	} {
		args := map[string]any{"title": "t", "body": "b", testChangeTargetKind: map[string]any{}}
		for k, v := range extra {
			args[k] = v
		}
		res := propose.Handler(t.Context(), args)
		if !res.IsError || !strings.Contains(res.Text, testChangeTargetKind+" cannot be combined") {
			t.Errorf("%v: result %+v, want a refusal", extra, res)
		}
	}
	res := propose.Handler(t.Context(), map[string]any{
		"title": "t", "body": "b", testChangeTargetKind: map[string]any{"sync_to": "head"}, "reads": []any{"acme/widgets"},
	})
	if res.IsError {
		t.Errorf("%s with reads refused: %s", testChangeTargetKind, res.Text)
	}
}
