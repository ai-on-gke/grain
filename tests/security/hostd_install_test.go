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
)

// TestHostdInstallScriptIsPublicAndHoldsNoSecret: GET
// /api/hostd/install.sh answers a caller from off the machine with no
// access code and no session -- the machine it installs a hostd on has
// nothing to sign in with yet -- and what it answers holds neither the
// access code nor any hostd's token, which the script reads on the
// machine instead, nor the daemon's release or the image it runs, which
// only a caller with access is told (Settings -> Hostds passes them).
func TestHostdInstallScriptIsPublicAndHoldsNoSecret(t *testing.T) {
	f := newRouteFixtureFor(t, true)
	_, token, err := f.store.CreateHostd(context.Background(), model.Hostd{Name: "build-box", CreatedBy: "operator"})
	if err != nil {
		t.Fatalf("registering a hostd: %v", err)
	}
	if token == "" {
		t.Fatal("registering a hostd issued no token")
	}

	stranger := &routeFixture{server: f.server, store: f.store, from: publicAddr}
	rec := stranger.serve(t, http.MethodGet, "http://grain.example.com/api/hostd/install.sh", "", http.Header{})
	if rec.code != http.StatusOK {
		t.Fatalf("GET /api/hostd/install.sh from %s without the access code: %d, want 200: %s", publicAddr, rec.code, rec.body.String())
	}
	body := rec.body.String()
	if !strings.HasPrefix(body, "#!/bin/sh") {
		t.Fatalf("GET /api/hostd/install.sh answered something that is not the script: %.200q", body)
	}
	if !strings.Contains(body, "GRAIN_DAEMON_URL='http://grain.example.com'") {
		t.Error("the script does not name the daemon it was fetched from")
	}
	for _, blank := range []string{"GRAIN_DAEMON_RELEASE=''", "GRAIN_DAEMON_IMAGE=''"} {
		if !strings.Contains(body, blank) {
			t.Errorf("a caller without access was told what %s holds", strings.TrimSuffix(blank, "=''"))
		}
	}
	if strings.Contains(body, token) {
		t.Error("the install script holds a hostd's token")
	}
	if strings.Contains(body, testAccessCode) {
		t.Error("the install script holds the access code")
	}
}
