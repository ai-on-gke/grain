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

package ui_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// An antigravity connection's model-turn limits are saved through the
// settings API when they parse inside model.ParseModelTurnLimit's bounds,
// and refused -- naming the field -- otherwise, or on a connection of any
// other framework, which has no model-turn deadline to tune.
func TestConnectionTurnLimitsAreValidated(t *testing.T) {
	srv, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	if err := client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	put := func(backend string) (int, string) {
		rec := do(t, srv, http.MethodPut, "/api/settings", `{"agentBackends":[`+backend+`]}`)
		return rec.Code, rec.Body.String()
	}

	code, out := put(`{"id":"corp-agy","framework":"antigravity","modelTurnTimeout":" 40m ","modelRetryCutoff":"2m"}`)
	if code != http.StatusOK {
		t.Fatalf("saving valid turn limits: code %d: %s", code, out)
	}
	cfg, err := client.Store.GetConfig(context.Background())
	if err != nil || cfg == nil || len(cfg.AgentBackends) != 1 {
		t.Fatalf("reading the saved config: %+v, %v", cfg, err)
	}
	if got := cfg.AgentBackends[0]; got.ModelTurnTimeout != "40m" || got.ModelRetryCutoff != "2m" {
		t.Errorf("saved limits = %q/%q, want 40m/2m", got.ModelTurnTimeout, got.ModelRetryCutoff)
	}

	if code, out := put(`{"id":"corp-agy","framework":"antigravity"}`); code != http.StatusOK {
		t.Fatalf("clearing the limits back to the defaults: code %d: %s", code, out)
	}

	for body, want := range map[string]string{
		`{"id":"a","framework":"antigravity","modelTurnTimeout":"29s"}`:                         "modelTurnTimeout",
		`{"id":"a","framework":"antigravity","modelTurnTimeout":"61m"}`:                         "modelTurnTimeout",
		`{"id":"a","framework":"antigravity","modelRetryCutoff":"soon"}`:                        "modelRetryCutoff",
		`{"id":"a","framework":"antigravity","modelRetryCutoff":"10s"}`:                         "modelRetryCutoff",
		`{"id":"a","framework":"antigravity","modelTurnTimeout":"5m","modelRetryCutoff":"10m"}`: "longer than modelTurnTimeout",
		`{"id":"a","framework":"claude","modelTurnTimeout":"20m"}`:                              "only for antigravity",
	} {
		code, out := put(body)
		if code != http.StatusBadRequest || !strings.Contains(out, want) {
			t.Errorf("PUT %s: code %d %s; want 400 naming %q", body, code, out, want)
		}
	}
}
