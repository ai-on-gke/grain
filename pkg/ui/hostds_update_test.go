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

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentdrelease "github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

func TestHostUpdateForOffersOnlySelfUpdatingAgents(t *testing.T) {
	latest := func(platform string) (string, bool) {
		return "v3.9.0", platform == "linux/amd64" || platform == "../../x"
	}
	agent := func(edit func(*hostd.RemoteStatus)) *hostd.RemoteStatus {
		st := &hostd.RemoteStatus{Connected: true, RemotePeer: hostd.RemotePeer{
			Build: "v3.8.0", AgentsOnly: true, Platform: "linux/amd64", UpdateMode: hostd.UpdateOffer,
			Features: []hostd.Feature{hostd.FeatureStart, hostd.FeatureSelfUpdate},
		}}
		if edit != nil {
			edit(st)
		}
		return st
	}
	if u := hostUpdateFor(agent(nil), latest); u == nil || u.Available != "v3.9.0" || u.Build != "v3.8.0" || u.Command != "grain-agentd update -version v3.9.0" {
		t.Fatalf("hostUpdateFor = %+v", u)
	}
	for name, edit := range map[string]func(*hostd.RemoteStatus){
		"root grain host":   func(st *hostd.RemoteStatus) { st.AgentsOnly = false },
		"no self_update":    func(st *hostd.RemoteStatus) { st.Features = []hostd.Feature{hostd.FeatureStart} },
		"mode off":          func(st *hostd.RemoteStatus) { st.UpdateMode = hostd.UpdateOff },
		"outside the pin":   func(st *hostd.RemoteStatus) { st.UpdatePin = "v3.8.*" },
		"up to date":        func(st *hostd.RemoteStatus) { st.Build = "v3.9.0" },
		"newer than offer":  func(st *hostd.RemoteStatus) { st.Build = "v4.0.0" },
		"unversioned build": func(st *hostd.RemoteStatus) { st.Build = "dev" },
		"no release":        func(st *hostd.RemoteStatus) { st.Platform = "linux/arm64" },
		"offline":           func(st *hostd.RemoteStatus) { st.Connected = false },
		"unknown platform":  func(st *hostd.RemoteStatus) { st.Platform = "../../x" },
	} {
		if u := hostUpdateFor(agent(edit), latest); u != nil {
			t.Errorf("%s: offered %+v", name, u)
		}
	}
	if u := hostUpdateFor(nil, latest); u != nil {
		t.Errorf("no connection: offered %+v", u)
	}
}

// The hosts list waits only briefly for a platform's release, but the
// upstream fetch it starts is not cut short by that wait: it finishes and
// is cached, rather than cached as a failure that would leave the install
// and update routes with no release to serve for agentdFetchBackoff.
func TestHostUpdateLookupDoesNotCutShortTheUpstreamFetch(t *testing.T) {
	pub, priv, _ := agentdrelease.GenerateKeyPair()
	pubPEM, _ := agentdrelease.FormatPublicKeyPEM(pub)
	binPayload := []byte("slow-upstream-agentd-binary")
	const rel = "v9.4.4"
	m, _ := agentdrelease.NewManifestForBinary(rel, "linux/amd64", binPayload, nil)
	signed, _ := agentdrelease.SignManifest(m, priv, nil)
	rawManifest, _ := json.Marshal(signed)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/" + rel + "/grain-agentd-linux-amd64.manifest.json":
			_, _ = w.Write(rawManifest)
		case "/releases/" + rel + "/grain-agentd-linux-amd64":
			time.Sleep(300 * time.Millisecond) // longer than the list waits
			_, _ = w.Write(binPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	t.Setenv(agentdDistDirEnv, t.TempDir())
	t.Setenv(agentdReleaseURLEnv, upstream.URL+"/releases")
	t.Setenv(agentdReleasePublicKeyEnv, string(pubPEM))
	oldRel := cliInstallRelease
	cliInstallRelease = func() string { return rel }
	t.Cleanup(func() { cliInstallRelease = oldRel })

	srv := NewServer(Config{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if _, ok := srv.resolveAgentdReleaseFor(ctx, "linux/amd64", 20*time.Millisecond); ok {
		t.Fatal("the lookup answered before the slow upstream did")
	}
	cancel() // the list's request is over

	deadline := time.Now().Add(5 * time.Second)
	for {
		entry, ok := srv.resolveAgentdReleaseFor(context.Background(), "linux/amd64", time.Second)
		if ok {
			if entry.manifest.Version != rel {
				t.Fatalf("resolved %q, want %q", entry.manifest.Version, rel)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the release never resolved: the list's wait cut the upstream fetch short")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/linux/amd64", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), binPayload) {
		t.Fatalf("GET the binary after a list = %d %q", rec.Code, rec.Body.String())
	}
}
