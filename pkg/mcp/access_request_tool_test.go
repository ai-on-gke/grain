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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type refusingChecker struct{ seen [][]model.AccessRequestItem }

func (r *refusingChecker) CheckAccessRequest(_ context.Context, items []model.AccessRequestItem) error {
	r.seen = append(r.seen, items)
	for _, it := range items {
		if it.Capability == "no-such" {
			return errors.New(`requests[0] (grant no-such): capability "no-such" is not offered`)
		}
	}
	return nil
}

func requestAccessFor(sink *MockSink) Tool {
	for _, tool := range NewMockTools(sink, nil) {
		if tool.Name == "request_access" {
			return tool
		}
	}
	panic("request_access is not among the escape hatches")
}

func TestRequestAccessValidatesEachItem(t *testing.T) {
	t.Parallel()
	checker := &refusingChecker{}
	sink := &MockSink{}
	sink.SetAccessChecker(checker)
	tool := requestAccessFor(sink)
	call := func(requests ...any) Result {
		return tool.Handler(context.Background(), map[string]any{
			"summary": "Grant read of acme/lib", "reason": "the fix spans both", "requests": requests,
		})
	}

	for name, tt := range map[string]struct {
		item any
		want string
	}{
		"no kind":           {map[string]any{"repo": "acme/lib"}, "kind is required"},
		"unknown kind":      {map[string]any{"kind": "root"}, "not read, write, grant or network"},
		"read without repo": {map[string]any{"kind": "read"}, "needs repo"},
		"stray field":       {map[string]any{"kind": "read", "repo": "acme/lib", "base": "dev"}, `takes no "base"`},
		"closed network":    {map[string]any{"kind": "network", "access": "none"}, `only be requested as "open"`},
		"with not object":   {map[string]any{"kind": "grant", "capability": "x#y", "with": "z"}, "with must be an object"},
		"daemon refusal":    {map[string]any{"kind": "grant", "capability": "no-such"}, "is not offered"},
	} {
		res := call(tt.item)
		if !res.IsError || !strings.Contains(res.Text, tt.want) {
			t.Errorf("%s: %v %q, want an error saying %q", name, res.IsError, res.Text, tt.want)
		}
	}
	if res := call(map[string]any{"kind": "read", "repo": "acme/lib"}, map[string]any{"kind": "read", "repo": "ACME/lib"}); !res.IsError {
		t.Error("a request naming one item twice was accepted")
	}
	if res := tool.Handler(context.Background(), map[string]any{"summary": "x", "requests": []any{map[string]any{"kind": "network", "access": "open"}}}); !res.IsError {
		t.Error("a request with no reason was accepted")
	}
	if sink.AccessRequest() != nil {
		t.Fatal("a refused call was recorded")
	}

	res := call(map[string]any{"kind": "write", "repo": "acme/lib", "base": "dev"},
		map[string]any{"kind": "grant", "capability": "gcp#gcp-key", "with": map[string]any{"permission_set": "reader"}})
	if res.IsError || !strings.Contains(res.Text, "no further actions") {
		t.Fatalf("a well-formed call: %v %q", res.IsError, res.Text)
	}
	call(map[string]any{"kind": "network", "access": "open"})
	got := sink.AccessRequest()
	if got == nil || len(got.Items) != 2 || got.Items[0].Base != "dev" {
		t.Fatalf("recorded %+v, want the first well-formed call", got)
	}
	if last := checker.seen[len(checker.seen)-1]; len(last) != 1 || last[0].Kind != model.AccessNetwork {
		t.Errorf("the daemon was not asked about the second call: %+v", checker.seen)
	}
	if !isTurnEndingTool("request_access", Result{}) {
		t.Error("request_access does not end the turn")
	}
}
