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

package security

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Which hostd an agent CLI runs on -- another machine, and whatever CLI
// login lives there -- is the model connection's and the capability
// server's, set in Settings. No task carries one, so an agent with
// tasks:write cannot move work it files or edits onto a hostd: not on a
// filing, and not through PUT /api/tasks/{id}/hostd. Both are refused,
// never accepted and quietly ignored.
func TestAgentCannotRouteWorkToAnotherHostd(t *testing.T) {
	agent := map[string]string{ui.ActingTaskHeader: "901"}
	srv, store := networkWideningFixture(t, model.NetworkNone)
	ctx := context.Background()
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "someone-laptop"}); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, code int, resp string) {
		t.Helper()
		if code != http.StatusBadRequest && code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want it refused", what, code, resp)
		}
	}
	for _, body := range []string{`{"hostd":"someone-laptop"}`, `{"Hostd":"gpu=a100"}`} {
		code, resp := serveNetworkWidening(t, srv, http.MethodPut, "/api/tasks/902/hostd", body, agent)
		refused("PUT hostd "+body, code, resp)
	}
	code, resp := serveNetworkWidening(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"elsewhere","repo":"acme/widgets","hostd":"someone-laptop"}`, agent)
	refused("filing with a hostd", code, resp)
	if code == http.StatusBadRequest && !strings.Contains(resp, "not the task") {
		t.Errorf("filing with a hostd was refused with %q, want it to say hostd is set on the connection or server", resp)
	}
}
