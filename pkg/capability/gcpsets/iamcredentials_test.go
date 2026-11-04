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

package gcpsets_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
)

// The real connection's impersonation calls, against a fake IAM
// Credentials API: generateAccessToken with the lifetime asked for,
// generateIdToken, and testIamPermissions on the account.
func TestGoogleCloudImpersonatesThroughIAMCredentials(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var lifetimes []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			http.Error(w, `{"error":{"code":401}}`, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
			lt, _ := body["lifetime"].(string)
			mu.Lock()
			lifetimes = append(lifetimes, lt)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"accessToken": "ya29.impersonated", "expireTime": "2026-11-03T13:00:00Z"})
		case strings.HasSuffix(r.URL.Path, ":generateIdToken"):
			json.NewEncoder(w).Encode(map[string]string{"token": "eyJ.id." + body["audience"].(string)})
		case strings.HasSuffix(r.URL.Path, ":testIamPermissions"):
			json.NewEncoder(w).Encode(map[string]any{"permissions": []string{"iam.serviceAccounts.getAccessToken"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	g := gcpsets.GoogleConnector{
		ADC: func(context.Context, ...string) (*google.Credentials, error) {
			return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "admin-token", Expiry: time.Now().Add(time.Hour)})}, nil
		},
		Options: []option.ClientOption{option.WithEndpoint(ts.URL + "/")},
	}
	ctx := context.Background()
	c, err := g.Connect(ctx, gcpsets.Admin{Name: "me", CredentialSource: gcpsets.SourceAmbient})
	if err != nil {
		t.Fatal(err)
	}
	imp, ok := c.(gcpsets.Impersonator)
	if !ok {
		t.Fatal("the Google connection cannot impersonate")
	}
	tok, exp, err := imp.GenerateAccessToken(ctx, orgSA, []string{"https://www.googleapis.com/auth/cloud-platform"}, 20*time.Minute)
	if err != nil || tok != "ya29.impersonated" || !exp.Equal(time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("GenerateAccessToken = %q %s %v", tok, exp, err)
	}
	if id, err := imp.GenerateIDToken(ctx, orgSA, "https://svc.example.com"); err != nil || id != "eyJ.id.https://svc.example.com" {
		t.Fatalf("GenerateIDToken = %q %v", id, err)
	}
	if has, err := imp.TestAccountPermissions(ctx, orgSA, []string{"iam.serviceAccounts.getAccessToken"}); err != nil || len(has) != 1 {
		t.Fatalf("TestAccountPermissions = %v %v", has, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "POST /v1/projects/-/serviceAccounts/" + orgSA + ":generateAccessToken"
	if len(paths) == 0 || paths[0] != want || len(lifetimes) != 1 || lifetimes[0] != "1200s" {
		t.Fatalf("calls %v lifetimes %v, want %s with 1200s", paths, lifetimes, want)
	}
}
