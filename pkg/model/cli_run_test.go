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
	"context"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCLIRunRoundTripsThroughGrainConfig(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	want := map[string]model.CLIRunSetting{
		model.AgentFrameworkClaude:      {Location: model.CLIRunContainer},
		model.AgentFrameworkAntigravity: {Location: model.CLIRunHost},
	}
	if err := store.UpdateConfig(ctx, func(c *model.Config) error { c.CLIRun = want; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.CLIRun, want) {
		t.Fatalf("CLIRun = %+v, want %+v", got.CLIRun, want)
	}
}

func TestValidateCLIRun(t *testing.T) {
	for _, bad := range []map[string]model.CLIRunSetting{
		{"claude": {Location: "sideways"}},
		{"cursor": {Location: model.CLIRunHost}},
		{model.AgentFrameworkCustom: {Location: "auto"}},
	} {
		if err := model.ValidateCLIRun(bad); err == nil {
			t.Errorf("model.ValidateCLIRun(%+v) = nil, want an error", bad)
		}
	}
	if err := model.ValidateCLIRun(map[string]model.CLIRunSetting{
		"claude": {Location: model.CLIRunContainer}, "codex": {}, model.AgentFrameworkAntigravity: {Location: model.CLIRunHost},
	}); err != nil {
		t.Fatal(err)
	}
	if s, _ := model.EncodeCLIRun(map[string]model.CLIRunSetting{"claude": {}}); s != "" {
		t.Fatalf("an empty entry encodes as %q, want nothing stored", s)
	}
}

// What a release before v2.3.0 stored is dropped as it is read: an "auto"
// location leaves that CLI to the daemon's default, and an image -- which
// only the per-run docker launcher those releases had ever used -- is
// not kept.
func TestDecodeCLIRunDropsWhatOlderReleasesStored(t *testing.T) {
	got, err := model.DecodeCLIRun(`{"claude":{"location":"auto","image":"ghcr.io/bwsalmon/grain/grain:v2.1.0"},` +
		`"codex":{"location":"container","image":"mine:1"},"antigravity":{"location":"auto"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.CLIRunSetting{model.AgentFrameworkCodex: {Location: model.CLIRunContainer}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeCLIRun = %+v, want %+v", got, want)
	}
	if got, err := model.DecodeCLIRun(`{"claude":{"location":"auto"}}`); err != nil || got != nil {
		t.Fatalf("DecodeCLIRun of only an auto = %+v, %v, want nothing stored", got, err)
	}
}
