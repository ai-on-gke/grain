package mcp

import (
	"strings"
	"testing"
)

func TestProposedChangeTargetReadsTheGoogle3Argument(t *testing.T) {
	for name, c := range map[string]struct {
		arg      any
		wantKind string
		wantSpec string
		err      string
	}{
		"absent":    {arg: nil},
		"false":     {arg: false},
		"true":      {arg: true, wantKind: "google3"},
		"empty":     {arg: map[string]any{}, wantKind: "google3"},
		"spec":      {arg: map[string]any{"workspace": "w", "start_cl": float64(12)}, wantKind: "google3", wantSpec: `{"start_cl":12,"workspace":"w"}`},
		"not a map": {arg: "w", err: "must be an object"},
	} {
		args := map[string]any{}
		if c.arg != nil {
			args["google3"] = c.arg
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

func TestProposeTaskRefusesGoogle3WithGitArguments(t *testing.T) {
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
		args := map[string]any{"title": "t", "body": "b", "google3": map[string]any{}}
		for k, v := range extra {
			args[k] = v
		}
		res := propose.Handler(t.Context(), args)
		if !res.IsError || !strings.Contains(res.Text, "google3 cannot be combined") {
			t.Errorf("%v: result %+v, want a refusal", extra, res)
		}
	}
	res := propose.Handler(t.Context(), map[string]any{
		"title": "t", "body": "b", "google3": map[string]any{"sync_to": "head"}, "reads": []any{"acme/widgets"},
	})
	if res.IsError {
		t.Errorf("google3 with reads refused: %s", res.Text)
	}
}
