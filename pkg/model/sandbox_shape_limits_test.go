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

package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSandboxShapeLimitsResolvedMaxFillsDefaults(t *testing.T) {
	got := model.SandboxShapeLimits{Max: model.SandboxShape{CPUs: 8}}.ResolvedMax()
	want := model.SandboxShape{CPUs: 8, MemoryMB: model.DefaultMaxSandboxMemoryMB, DiskGB: model.DefaultMaxSandboxDiskGB}
	if got != want {
		t.Fatalf("ResolvedMax = %+v, want %+v", got, want)
	}
	if got := (model.SandboxShapeLimits{}).ResolvedMax(); got.CPUs != model.DefaultMaxSandboxCPUs {
		t.Fatalf("zero limits ResolvedMax = %+v, want grain's default maximum", got)
	}
}

func TestSandboxShapeLimitsRoleCeiling(t *testing.T) {
	l := model.SandboxShapeLimits{
		Max: model.SandboxShape{CPUs: 16, MemoryMB: 65536, DiskGB: 200},
		Roles: map[string]model.SandboxShape{
			"small": {CPUs: 2, MemoryMB: 4096},
			"mid":   {CPUs: 8, DiskGB: 500}, // above Max: held to Max
		},
	}
	for _, tc := range []struct {
		roles []string
		want  model.SandboxShape
	}{
		{nil, model.SandboxShape{CPUs: 16, MemoryMB: 65536, DiskGB: 200}},
		{[]string{"small"}, model.SandboxShape{CPUs: 2, MemoryMB: 4096, DiskGB: 200}},
		{[]string{"mid"}, model.SandboxShape{CPUs: 8, MemoryMB: 65536, DiskGB: 200}},
		{[]string{"small", "mid"}, model.SandboxShape{CPUs: 8, MemoryMB: 65536, DiskGB: 200}},
		// A role with no entry of its own is held to Max alone.
		{[]string{"small", "unlisted"}, model.SandboxShape{CPUs: 16, MemoryMB: 65536, DiskGB: 200}},
	} {
		if got := l.RoleCeiling(tc.roles); got != tc.want {
			t.Errorf("RoleCeiling(%v) = %+v, want %+v", tc.roles, got, tc.want)
		}
	}
}

func TestSandboxShapeAbove(t *testing.T) {
	ceiling := model.SandboxShape{CPUs: 4, MemoryMB: 8192}
	if got := (model.SandboxShape{CPUs: 4, MemoryMB: 8192, DiskGB: 9999}).Above(ceiling); len(got) != 0 {
		t.Fatalf("Above at the ceiling (and an unbounded disk) = %v, want none", got)
	}
	got := model.SandboxShape{CPUs: 5, MemoryMB: 16384}.Above(ceiling)
	if len(got) != 2 || !strings.Contains(got[0], "sandboxCpus 5") || !strings.Contains(got[1], "sandboxMemoryMb 16384") {
		t.Fatalf("Above = %v, want both dimensions named", got)
	}
}

func TestSandboxShapeLimitsValidateAndRoundTrip(t *testing.T) {
	if err := (model.SandboxShapeLimits{Max: model.SandboxShape{CPUs: -1}}).Validate(); err == nil {
		t.Fatal("Validate accepted a negative maximum")
	}
	if err := (model.SandboxShapeLimits{Roles: map[string]model.SandboxShape{" ": {CPUs: 1}}}).Validate(); err == nil {
		t.Fatal("Validate accepted an unnamed role")
	}
	if text, err := model.EncodeSandboxShapeLimits(model.SandboxShapeLimits{}); err != nil || text != "" {
		t.Fatalf("Encode(zero) = %q, %v; want empty", text, err)
	}
	in := model.SandboxShapeLimits{
		Max:   model.SandboxShape{CPUs: 12},
		Roles: map[string]model.SandboxShape{"grain/contributor": {CPUs: 4, MemoryMB: 16384}},
	}
	text, err := model.EncodeSandboxShapeLimits(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := model.DecodeSandboxShapeLimits(text)
	if err != nil {
		t.Fatal(err)
	}
	if out.Max != in.Max || out.Roles["grain/contributor"] != in.Roles["grain/contributor"] {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestProposalAboveProposerSandboxIsPersonOnly(t *testing.T) {
	parent := model.Task{ID: "7", SandboxCPUs: 4}
	if why := model.PersonOnlyProposalSettings(model.Task{SandboxCPUs: 4}, parent); why != "" {
		t.Fatalf("a proposal at its proposer's size is held: %s", why)
	}
	why := model.PersonOnlyProposalSettings(model.Task{SandboxCPUs: 8}, parent)
	if !strings.Contains(why, "larger sandbox than task 7") {
		t.Fatalf("PersonOnlyProposalSettings(8 CPUs from a 4-CPU proposer) = %q, want the larger sandbox named", why)
	}
	// A proposer with no override of its own is measured at grain's own
	// default shape.
	if why := model.PersonOnlyProposalSettings(model.Task{SandboxMemoryMB: model.BuiltinSandboxMemoryMB}, model.Task{ID: "8"}); why != "" {
		t.Fatalf("a proposal at grain's default memory is held: %s", why)
	}
	if why := model.ProposalSandboxShapeAbove(model.Task{SandboxDiskGB: model.BuiltinSandboxDiskGB + 1}, model.Task{ID: "8"}); why == "" {
		t.Fatal("a proposal above grain's default disk from a proposer with no override is not held")
	}
}
