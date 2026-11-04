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

package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeviceWebBaseURL(t *testing.T) {
	cases := []struct {
		host    string
		baseURL string
		want    string
	}{
		{"", "", "https://github.com"},
		{"github.com", "", "https://github.com"},
		{"ghe.example.com", "", "https://ghe.example.com"},
		{"ghe.example.com", "https://ghe.example.com:8443/api/v3/", "https://ghe.example.com:8443"},
	}
	for _, tc := range cases {
		if got := DeviceWebBaseURL(tc.host, tc.baseURL); got != tc.want {
			t.Errorf("DeviceWebBaseURL(%q, %q) = %q, want %q", tc.host, tc.baseURL, got, tc.want)
		}
	}
}

func TestRequestDeviceCodeAndPollDeviceToken(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got := r.FormValue("client_id"); got != "client-123" {
				t.Errorf("client_id = %q, want client-123", got)
			}
			if got := r.FormValue("scope"); got != "repo read:org workflow" {
				t.Errorf("scope = %q, want 'repo read:org workflow'", got)
			}
			_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
				DeviceCode:      "dev-code-xyz",
				UserCode:        "ABCD-1234",
				VerificationURI: "https://github.com/login/device",
				ExpiresIn:       900,
				Interval:        1,
			})
		case "/login/oauth/access_token":
			n := polls.Add(1)
			switch n {
			case 1:
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			case 2:
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "slow_down", "interval": 0})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_delegated_token_999"})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dc, err := RequestDeviceCode(ctx, srv.Client(), srv.URL, "client-123", DefaultOAuthScopes)
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if dc.UserCode != "ABCD-1234" || dc.DeviceCode != "dev-code-xyz" {
		t.Fatalf("unexpected DeviceCodeResponse: %+v", dc)
	}

	// Poll with a tiny initial interval so the test completes quickly.
	reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
	defer reqCancel()

	// Test pollDeviceTokenOnce directly for slow_down and authorization_pending,
	// then PollDeviceToken for the final success.
	tok, next, err := pollDeviceTokenOnce(reqCtx, srv.Client(), srv.URL+"/login/oauth/access_token", "client_id=client-123&device_code=dev-code-xyz", 10*time.Millisecond)
	if err != nil || tok != "" {
		t.Fatalf("poll 1: tok=%q err=%v, want pending", tok, err)
	}
	tok, next, err = pollDeviceTokenOnce(reqCtx, srv.Client(), srv.URL+"/login/oauth/access_token", "client_id=client-123&device_code=dev-code-xyz", next)
	if err != nil || tok != "" || next <= 10*time.Millisecond {
		t.Fatalf("poll 2 (slow_down): tok=%q next=%v err=%v", tok, next, err)
	}
	tok, err = PollDeviceToken(reqCtx, srv.Client(), srv.URL, "client-123", dc.DeviceCode, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if tok != "gho_delegated_token_999" {
		t.Fatalf("PollDeviceToken = %q, want gho_delegated_token_999", tok)
	}
}

func TestPollDeviceTokenRefusals(t *testing.T) {
	for _, errCode := range []string{"expired_token", "access_denied"} {
		t.Run(errCode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"error": errCode})
			}))
			defer srv.Close()

			_, err := PollDeviceToken(context.Background(), srv.Client(), srv.URL, "client-123", "dev-code", 5*time.Millisecond)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", errCode)
			}
		})
	}
}
