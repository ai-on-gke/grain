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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/checkpost"
)

func TestUsage(t *testing.T) {
	usage()
}

func TestServeRequiresSecret(t *testing.T) {
	t.Setenv(secretEnv, "")
	err := serve([]string{"-addr", "127.0.0.1:0"})
	if err == nil {
		t.Fatal("serve with no CHECKPOST_SECRET: got nil error, want error")
	}
}

func TestReportRequiresSecret(t *testing.T) {
	t.Setenv(secretEnv, "")
	err := report([]string{"-url", "http://127.0.0.1:8080"})
	if err == nil {
		t.Fatal("report with no CHECKPOST_SECRET: got nil error, want error")
	}
}

func TestReportRequiresURL(t *testing.T) {
	t.Setenv(secretEnv, "test-secret")
	err := report([]string{"-repo", "owner/repo"})
	if err == nil {
		t.Fatal("report with empty url: got nil error, want error")
	}
}

func TestReportValidatesVerdict(t *testing.T) {
	t.Setenv(secretEnv, "test-secret")
	err := report([]string{
		"-url", "http://127.0.0.1:8080",
		"-repo", "owner/repo",
		"-commit", "shortsha",
		"-name", "go",
		"-conclusion", "success",
	})
	if err == nil {
		t.Fatal("report with short commit sha: got nil error, want validation error")
	}
}

func TestReportSuccess(t *testing.T) {
	secret := "test-secret-12345"
	t.Setenv(secretEnv, secret)

	var receivedBody []byte
	var receivedSig string
	var receivedTs string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/verdicts" {
			http.NotFound(w, r)
			return
		}
		receivedSig = r.Header.Get(checkpost.SignatureHeader)
		receivedTs = r.Header.Get(checkpost.TimestampHeader)
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var v checkpost.Verdict
		if err := json.Unmarshal(receivedBody, &v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	commit := "0123456789012345678901234567890123456789"
	err := report([]string{
		"-url", srv.URL,
		"-repo", "owner/repo",
		"-commit", commit,
		"-name", "go",
		"-conclusion", "success",
		"-check-url", "https://ci.example.com/build/1",
	})
	if err != nil {
		t.Fatalf("report failed: %v", err)
	}

	if receivedSig == "" || receivedTs == "" {
		t.Fatalf("expected headers %s and %s, got sig=%q ts=%q", checkpost.SignatureHeader, checkpost.TimestampHeader, receivedSig, receivedTs)
	}
	if err := checkpost.Verify(secret, receivedSig, receivedTs, http.MethodPost, "/v1/verdicts", receivedBody, time.Now()); err != nil {
		t.Fatalf("the report's signature does not verify over its method, path and body: %v", err)
	}
	var v checkpost.Verdict
	if err := json.Unmarshal(receivedBody, &v); err != nil {
		t.Fatalf("unmarshal received body: %v", err)
	}
	if v.Repo != "owner/repo" || v.Commit != commit || v.Name != "go" || v.Conclusion != "success" || v.URL != "https://ci.example.com/build/1" {
		t.Fatalf("received verdict mismatch: %+v", v)
	}
}

func TestReportServerError(t *testing.T) {
	secret := "test-secret-12345"
	t.Setenv(secretEnv, secret)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	}))
	defer srv.Close()

	commit := "0123456789012345678901234567890123456789"
	err := report([]string{
		"-url", srv.URL,
		"-repo", "owner/repo",
		"-commit", commit,
		"-name", "go",
		"-conclusion", "success",
	})
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}
