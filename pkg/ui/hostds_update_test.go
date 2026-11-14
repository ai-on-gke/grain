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
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func TestHostUpdateForOffersOnlySelfUpdatingAgents(t *testing.T) {
	latest := func(platform string) (string, bool) { return "v3.9.0", platform == "linux/amd64" }
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
	} {
		if u := hostUpdateFor(agent(edit), latest); u != nil {
			t.Errorf("%s: offered %+v", name, u)
		}
	}
	if u := hostUpdateFor(nil, latest); u != nil {
		t.Errorf("no connection: offered %+v", u)
	}
}
