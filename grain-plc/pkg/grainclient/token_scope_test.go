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

package grainclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGrainTokenIsSentOnlyToTheConfiguredDaemon pins that -grain-token
// ($GRAIN_TOKEN) reaches the -grain-url daemon and no other host: an
// instance the registry names could be anybody's, and the token is this
// daemon's access code.
func TestGrainTokenIsSentOnlyToTheConfiguredDaemon(t *testing.T) {
	t.Parallel()
	seen := func(got *string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*got = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1","title":"t","state":"proposed"}`))
		}))
	}
	var home, other string
	homeSrv := seen(&home)
	defer homeSrv.Close()
	otherSrv := seen(&other)
	defer otherSrv.Close()

	c := New(Config{GrainURL: homeSrv.URL + "/", GrainToken: "access-code"})
	if _, err := c.ForURL(homeSrv.URL).GetTask(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if home != "Bearer access-code" {
		t.Fatalf("configured daemon got Authorization %q, want the grain token", home)
	}
	if _, err := c.ForURL(otherSrv.URL).GetTask(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if other != "" {
		t.Fatalf("another host got Authorization %q; the grain token must stay with -grain-url", other)
	}
}
