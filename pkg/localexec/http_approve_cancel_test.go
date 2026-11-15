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

package localexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A call cancelled while the server is still answering its ask -- the
// approval already recorded as pending -- still expires that approval.
// Before, the ask was made with the call's own context, so the cancel cut
// it short, Approve returned with no approval ID, and the approval sat
// pending in the inbox until its deadline (seen as a flaky
// pkg/ui TestHostScriptGrantAndScopedCallbackToken).
func TestHTTPApproverExpiresAnApprovalCancelledMidAsk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var expired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/local-script-approvals"):
			// Recorded; the caller gives up before the answer is written.
			cancel()
			time.Sleep(50 * time.Millisecond)
			w.Write([]byte(`{"id":"lsa-1","status":"pending"}`))
		case strings.HasSuffix(r.URL.Path, "/lsa-1/expire"):
			expired.Store(true)
			w.Write([]byte(`{"id":"lsa-1","status":"expired"}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := HTTPApprover{BaseURL: srv.URL, TaskID: "1", Token: "tok"}
	ok, err := a.Approve(ctx, Request{ID: "hs-1", Tool: "t", Script: "s", Deadline: time.Now().Add(time.Minute)})
	if ok || err == nil {
		t.Fatalf("Approve = %v, %v; want false and the context's error", ok, err)
	}
	if !expired.Load() {
		t.Fatalf("the approval recorded before the cancel was never expired")
	}
}
