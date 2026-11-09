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
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeHostdStatuses is a HostdRemote that reports fixed statuses.
type fakeHostdStatuses struct {
	http.Handler
	st map[string]hostd.RemoteStatus
}

func (f fakeHostdStatuses) Statuses() map[string]hostd.RemoteStatus { return f.st }

// GET /api/hostds lists each host's agent CLI logins: a dialing-in
// host's from its session, the co-located one's from LocalHostdLogins,
// and none for a host that is not connected now.
func TestListHostdsReportsAmbientLogins(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ws-a", "ws-b"} {
		if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	client.Config.HostdRemote = fakeHostdStatuses{st: map[string]hostd.RemoteStatus{
		"ws-a": {Connected: true, RemotePeer: hostd.RemotePeer{Name: "ws-a", Logins: []string{"codex"}, LoginsKnown: true}},
		"ws-b": {Connected: false, RemotePeer: hostd.RemotePeer{Name: "ws-b", Logins: []string{"claude"}, LoginsKnown: true}},
	}}
	client.Config.LocalHostdLogins = func(context.Context) ([]string, bool) { return []string{"antigravity"}, true }

	rows, err := client.ListHostds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type logins struct {
		list  []string
		known bool
	}
	got := map[string]logins{}
	for _, h := range rows {
		got[h.Name] = logins{h.Logins, h.LoginsKnown}
	}
	want := map[string]logins{
		model.LocalHostd: {[]string{"antigravity"}, true},
		"ws-a":           {[]string{"codex"}, true},
		"ws-b":           {nil, false}, // not connected: not known now
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("logins = %+v, want %+v", got, want)
	}
}
