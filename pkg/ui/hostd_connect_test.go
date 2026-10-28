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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// /api/hostd/connect is refused where no hostd may dial in, refuses a
// hostd with the wrong token, and takes one with the right token through
// the server's own gates -- it authenticates with the hostd token, not a
// signed-in caller -- as the session the daemon's hostd.Client then dials
// over.
func TestHostdConnectEndpoint(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, hostd.RemotePath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s without HostdRemote = %d, want 503", hostd.RemotePath, rec.Code)
	}

	remote := &hostd.Remote{Token: "hostd-token"}
	client, _, _ := testClient(t)
	client.Config.HostdRemote = remote
	ts := httptest.NewServer(ui.NewServerWithClient(client))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := (hostd.Dialer{Server: ts.URL, Token: "wrong"}).Dial(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("dial with the wrong token = %v, want a 401", err)
	}
	conn, err := hostd.Dialer{Server: ts.URL, Token: "hostd-token", Host: "cloudtop"}.Dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := remote.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	if st := remote.Status(); !st.Connected || st.Host != "cloudtop" {
		t.Fatalf("status = %+v", st)
	}
}
