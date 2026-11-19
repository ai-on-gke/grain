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

//go:build ignore

// smfakesrv serves the Secret Manager v1 REST subset grain uses
// (secrets.{create,get,delete}, versions.{add,access,destroy}) on
// 127.0.0.1:4277, for docs/test-plans/2026-11-18.md §5 item 6. It is
// pkg/secrets/secretmanager_test.go's fakeSecretManagerServer, copied
// verbatim and run as a server: no Secret Manager emulator exists to run
// in a sandbox. Bearer token: fake-sm-oauth-token. Prefix:
// projects/demo-project/secrets/grain-walk-.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

func main() {
	f := &fakeSecretManagerServer{
		token:         "fake-sm-oauth-token",
		allowedPrefix: "projects/demo-project/secrets/grain-walk-",
		secrets:       make(map[string]*fakeSMSecret),
	}
	log.Fatal(http.ListenAndServe("127.0.0.1:4277", http.HandlerFunc(f.serveHTTP)))
}

type fakeSMVersion struct {
	number int
	state  string // "ENABLED" or "DESTROYED"
	data   []byte
}

type fakeSMSecret struct {
	name     string // projects/<p>/secrets/<id>
	labels   map[string]string
	versions []*fakeSMVersion
}

// fakeSecretManagerServer implements the Google Cloud Secret Manager v1 REST
// endpoints granted to grainInstanceSecrets:
//   - secrets.{create,get,delete}
//   - versions.{add,access,destroy}
//
// Project-wide secrets.list and versions.list are deliberately denied with 403
// and counted so tests can assert grain never calls them.
type fakeSecretManagerServer struct {
	mu            sync.Mutex
	token         string
	allowedPrefix string
	secrets       map[string]*fakeSMSecret

	listCalls       int
	accessCalls     int
	createCalls     int
	addVersionCalls int
	destroyCalls    int
	deleteCalls     int

	failNextDestroyStatus int
	conflictNextCreate    bool
}

func (f *fakeSecretManagerServer) checkPrefix(w http.ResponseWriter, resource string) bool {
	if f.allowedPrefix != "" && !strings.HasPrefix(resource, f.allowedPrefix) {
		http.Error(w, fmt.Sprintf(`{"error":{"code":403,"message":"IAM condition denied access to %s"}}`, resource), http.StatusForbidden)
		return false
	}
	return true
}

func (f *fakeSecretManagerServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
		return
	}

	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/v1/")

	// POST /v1/projects/<p>/secrets?secretId=<id> or GET /v1/projects/<p>/secrets (denied)
	if strings.HasPrefix(sub, "projects/") && strings.HasSuffix(sub, "/secrets") && strings.Count(sub, "/") == 2 {
		if r.Method == http.MethodGet {
			f.listCalls++
			http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"secretmanager.secrets.list denied"}}`, http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			proj := strings.TrimSuffix(strings.TrimPrefix(sub, "projects/"), "/secrets")
			secretID := r.URL.Query().Get("secretId")
			if !isValidSecretID(secretID) {
				http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid secretId"}}`, http.StatusBadRequest)
				return
			}
			resName := "projects/" + proj + "/secrets/" + secretID
			if !f.checkPrefix(w, resName) {
				return
			}
			f.createCalls++
			body, _ := io.ReadAll(r.Body)
			var req smCreateSecretRequest
			if err := json.Unmarshal(body, &req); err != nil || req.Replication.Automatic == nil {
				http.Error(w, `{"error":{"code":400,"message":"missing replication"}}`, http.StatusBadRequest)
				return
			}
			if f.conflictNextCreate {
				f.conflictNextCreate = false
				f.secrets[resName] = &fakeSMSecret{name: resName, labels: req.Labels}
				http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
				return
			}
			if _, exists := f.secrets[resName]; exists {
				http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
				return
			}
			labelsCopy := make(map[string]string, len(req.Labels))
			for k, v := range req.Labels {
				labelsCopy[k] = v
			}
			f.secrets[resName] = &fakeSMSecret{name: resName, labels: labelsCopy}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": resName, "labels": labelsCopy})
			return
		}
	}

	// POST /v1/projects/<p>/secrets/<id>:addVersion
	if strings.HasSuffix(sub, ":addVersion") && r.Method == http.MethodPost {
		resName := strings.TrimSuffix(sub, ":addVersion")
		if !f.checkPrefix(w, resName) {
			return
		}
		f.addVersionCalls++
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req smAddVersionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(req.Payload.Data)
		if err != nil {
			http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
			return
		}
		num := len(sec.versions) + 1
		sec.versions = append(sec.versions, &fakeSMVersion{
			number: num,
			state:  "ENABLED",
			data:   raw,
		})
		verName := fmt.Sprintf("%s/versions/%d", resName, num)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smVersionResponse{Name: verName, State: "ENABLED"})
		return
	}

	// GET /v1/projects/<p>/secrets/<id>/versions (versions.list - denied!)
	if strings.HasSuffix(sub, "/versions") && r.Method == http.MethodGet {
		f.listCalls++
		http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"secretmanager.versions.list denied"}}`, http.StatusForbidden)
		return
	}

	// GET /v1/projects/<p>/secrets/<id>/versions/<ver>:access
	if strings.HasSuffix(sub, ":access") && r.Method == http.MethodGet {
		trimmed := strings.TrimSuffix(sub, ":access")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !f.checkPrefix(w, resName) {
			return
		}
		f.accessCalls++
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		var chosen *fakeSMVersion
		if verStr == "latest" {
			for i := len(sec.versions) - 1; i >= 0; i-- {
				if sec.versions[i].state == "ENABLED" {
					chosen = sec.versions[i]
					break
				}
			}
		} else if n, err := strconv.Atoi(verStr); err == nil && n >= 1 && n <= len(sec.versions) {
			if sec.versions[n-1].state == "ENABLED" {
				chosen = sec.versions[n-1]
			}
		}
		if chosen == nil {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smAccessResponse{
			Name:    fmt.Sprintf("%s/versions/%d", resName, chosen.number),
			Payload: smPayloadData{Data: base64.StdEncoding.EncodeToString(chosen.data)},
		})
		return
	}

	// POST /v1/projects/<p>/secrets/<id>/versions/<ver>:destroy
	if strings.HasSuffix(sub, ":destroy") && r.Method == http.MethodPost {
		trimmed := strings.TrimSuffix(sub, ":destroy")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !f.checkPrefix(w, resName) {
			return
		}
		f.destroyCalls++
		if f.failNextDestroyStatus != 0 {
			st := f.failNextDestroyStatus
			f.failNextDestroyStatus = 0
			http.Error(w, `{"error":{"message":"injected destroy error"}}`, st)
			return
		}
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		n, err := strconv.Atoi(verStr)
		if err != nil || n < 1 || n > len(sec.versions) {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		v := sec.versions[n-1]
		if v.state == "DESTROYED" {
			http.Error(w, `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"SecretVersion is in DESTROYED state"}}`, http.StatusBadRequest)
			return
		}
		v.state = "DESTROYED"
		v.data = nil
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smVersionResponse{
			Name:  fmt.Sprintf("%s/versions/%d", resName, v.number),
			State: "DESTROYED",
		})
		return
	}

	// DELETE /v1/projects/<p>/secrets/<id>
	if strings.HasPrefix(sub, "projects/") && strings.Count(sub, "/") == 3 && r.Method == http.MethodDelete {
		if !f.checkPrefix(w, sub) {
			return
		}
		f.deleteCalls++
		if _, exists := f.secrets[sub]; !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		delete(f.secrets, sub)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
		return
	}

	http.NotFound(w, r)
}

// From pkg/secrets/secretmanager.go.
const maxSecretManagerIDLen = 255

func isValidSecretID(s string) bool {
	if s == "" || len(s) > maxSecretManagerIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type smCreateSecretRequest struct {
	Replication smReplication     `json:"replication"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type smReplication struct {
	Automatic *struct{} `json:"automatic"`
}

type smAddVersionRequest struct {
	Payload smPayloadData `json:"payload"`
}

type smPayloadData struct {
	Data string `json:"data"`
}

type smVersionResponse struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type smAccessResponse struct {
	Name    string        `json:"name"`
	Payload smPayloadData `json:"payload"`
}
