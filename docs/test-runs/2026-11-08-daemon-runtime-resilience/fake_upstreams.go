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

// Command fake_upstreams runs controllable local HTTP servers for:
//  1. Google Cloud Secret Manager REST API (-sm-addr)
//  2. Google Cloud Storage JSON/Multipart API (-gcs-addr)
//  3. Minimal GitHub API stub (-gh-addr) so `grain daemon` starts cleanly on loopback
//
// Each server exposes `/control/...` endpoints so the §6 walkthrough script can
// inject 404 / 400 FAILED_PRECONDITION (DESTROYED) / 403 / 503 responses out of
// band and inspect exact uploaded objects and leak counts.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cgi"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type smVersion struct {
	Number int    `json:"number"`
	State  string `json:"state"` // ENABLED or DESTROYED
	Data   []byte `json:"-"`
}

type smSecret struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels,omitempty"`
	Versions []*smVersion      `json:"versions"`
}

type fakeSM struct {
	mu            sync.Mutex
	token         string
	allowedPrefix string
	secrets       map[string]*smSecret
	accessStatus  map[string]int // resource or "*" -> forced HTTP status on :access
	accessCalls   int
	createCalls   int
	addVerCalls   int
	destroyCalls  int
	deleteCalls   int
	listCalls     int
}

func newFakeSM(token, prefix string) *fakeSM {
	return &fakeSM{
		token:         token,
		allowedPrefix: prefix,
		secrets:       make(map[string]*smSecret),
		accessStatus:  make(map[string]int),
	}
}

func (f *fakeSM) writeAPIError(w http.ResponseWriter, code int, status, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"status":  status,
			"message": msg,
		},
	})
}

func (f *fakeSM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, "/control/") {
		f.serveControl(w, r)
		return
	}

	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		f.writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid bearer token")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/v1/")

	if strings.HasPrefix(sub, "projects/") && strings.HasSuffix(sub, "/secrets") && strings.Count(sub, "/") == 2 {
		if r.Method == http.MethodGet {
			f.listCalls++
			f.writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "secretmanager.secrets.list denied")
			return
		}
		if r.Method == http.MethodPost {
			proj := strings.TrimSuffix(strings.TrimPrefix(sub, "projects/"), "/secrets")
			secretID := r.URL.Query().Get("secretId")
			resName := "projects/" + proj + "/secrets/" + secretID
			if f.allowedPrefix != "" && !strings.HasPrefix(resName, f.allowedPrefix) {
				f.writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "outside prefix")
				return
			}
			f.createCalls++
			if _, ok := f.secrets[resName]; ok {
				f.writeAPIError(w, http.StatusConflict, "ALREADY_EXISTS", "secret exists")
				return
			}
			var body struct {
				Labels map[string]string `json:"labels"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			f.secrets[resName] = &smSecret{Name: resName, Labels: body.Labels}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": resName, "labels": body.Labels})
			return
		}
	}

	if strings.HasSuffix(sub, ":addVersion") && r.Method == http.MethodPost {
		resName := strings.TrimSuffix(sub, ":addVersion")
		if f.allowedPrefix != "" && !strings.HasPrefix(resName, f.allowedPrefix) {
			f.writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "outside prefix")
			return
		}
		f.addVerCalls++
		sec, ok := f.secrets[resName]
		if !ok {
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "secret not found")
			return
		}
		var req struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &req); err != nil {
			f.writeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(req.Payload.Data)
		if err != nil {
			f.writeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		num := len(sec.Versions) + 1
		sec.Versions = append(sec.Versions, &smVersion{Number: num, State: "ENABLED", Data: decoded})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  fmt.Sprintf("%s/versions/%d", resName, num),
			"state": "ENABLED",
		})
		return
	}

	if strings.HasSuffix(sub, ":access") && r.Method == http.MethodGet {
		trimmed := strings.TrimSuffix(sub, ":access")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f.allowedPrefix != "" && !strings.HasPrefix(resName, f.allowedPrefix) {
			f.writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "outside prefix")
			return
		}
		f.accessCalls++
		if code := f.overrideFor(resName); code != 0 {
			status := "UNAVAILABLE"
			if code == http.StatusForbidden {
				status = "PERMISSION_DENIED"
			} else if code == http.StatusUnauthorized {
				status = "UNAUTHENTICATED"
			}
			f.writeAPIError(w, code, status, fmt.Sprintf("injected HTTP %d on %s", code, resName))
			return
		}
		sec, exists := f.secrets[resName]
		if !exists {
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Secret [%s] not found.", resName))
			return
		}
		var chosen *smVersion
		var latestDestroyed *smVersion
		if verStr == "latest" {
			for i := len(sec.Versions) - 1; i >= 0; i-- {
				if sec.Versions[i].State == "ENABLED" {
					chosen = sec.Versions[i]
					break
				}
				if latestDestroyed == nil && sec.Versions[i].State == "DESTROYED" {
					latestDestroyed = sec.Versions[i]
				}
			}
		} else if n, err := strconv.Atoi(verStr); err == nil && n >= 1 && n <= len(sec.Versions) {
			v := sec.Versions[n-1]
			if v.State == "ENABLED" {
				chosen = v
			} else if v.State == "DESTROYED" {
				latestDestroyed = v
			}
		}
		if chosen == nil {
			if latestDestroyed != nil {
				f.writeAPIError(w, http.StatusBadRequest, "FAILED_PRECONDITION",
					fmt.Sprintf("Secret Version [%s/versions/%d] is in DESTROYED state.", resName, latestDestroyed.Number))
				return
			}
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "no enabled version")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": fmt.Sprintf("%s/versions/%d", resName, chosen.Number),
			"payload": map[string]any{
				"data": base64.StdEncoding.EncodeToString(chosen.Data),
			},
		})
		return
	}

	if strings.HasSuffix(sub, ":destroy") && r.Method == http.MethodPost {
		trimmed := strings.TrimSuffix(sub, ":destroy")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.destroyCalls++
		sec, exists := f.secrets[resName]
		if !exists {
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "secret not found")
			return
		}
		n, err := strconv.Atoi(verStr)
		if err != nil || n < 1 || n > len(sec.Versions) {
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "version not found")
			return
		}
		v := sec.Versions[n-1]
		if v.State == "DESTROYED" {
			f.writeAPIError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "already DESTROYED")
			return
		}
		v.State = "DESTROYED"
		v.Data = nil
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  fmt.Sprintf("%s/versions/%d", resName, v.Number),
			"state": "DESTROYED",
		})
		return
	}

	if strings.HasPrefix(sub, "projects/") && strings.Count(sub, "/") == 3 && r.Method == http.MethodDelete {
		f.deleteCalls++
		if _, exists := f.secrets[sub]; !exists {
			f.writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "secret not found")
			return
		}
		delete(f.secrets, sub)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
		return
	}

	http.NotFound(w, r)
}

func (f *fakeSM) overrideFor(resName string) int {
	if c := f.accessStatus[resName]; c != 0 {
		return c
	}
	return f.accessStatus["*"]
}

func (f *fakeSM) serveControl(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/control/delete-secret":
		res := r.URL.Query().Get("resource")
		delete(f.secrets, res)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "deleted %s\n", res)
	case "/control/destroy-all-versions":
		res := r.URL.Query().Get("resource")
		sec, ok := f.secrets[res]
		if !ok {
			http.Error(w, "not found: "+res, http.StatusNotFound)
			return
		}
		for _, v := range sec.Versions {
			v.State = "DESTROYED"
			v.Data = nil
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "destroyed all versions of %s\n", res)
	case "/control/set-access-status":
		res := r.URL.Query().Get("resource")
		if res == "" {
			res = "*"
		}
		code, _ := strconv.Atoi(r.URL.Query().Get("status"))
		if code == 0 || code == http.StatusOK {
			delete(f.accessStatus, res)
		} else {
			f.accessStatus[res] = code
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "override %s -> %d\n", res, code)
	case "/control/stats":
		names := make([]string, 0, len(f.secrets))
		for k := range f.secrets {
			names = append(names, k)
		}
		sort.Strings(names)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secrets":      names,
			"accessCalls":  f.accessCalls,
			"createCalls":  f.createCalls,
			"addVerCalls":  f.addVerCalls,
			"destroyCalls": f.destroyCalls,
			"deleteCalls":  f.deleteCalls,
			"listCalls":    f.listCalls,
			"overrides":    f.accessStatus,
		})
	default:
		http.NotFound(w, r)
	}
}

type storedObject struct {
	Body    []byte `json:"-"`
	Updated string `json:"updated"`
}

type putAttempt struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	Status int    `json:"status"`
	Leaked bool   `json:"leaked"`
}

type fakeGCS struct {
	mu           sync.Mutex
	bucket       string
	putStatus    int
	forbiddenSub string
	objects      map[string]storedObject
	putAttempts  []putAttempt
}

func newFakeGCS(bucket, forbiddenSub string) *fakeGCS {
	return &fakeGCS{
		bucket:       bucket,
		forbiddenSub: forbiddenSub,
		objects:      make(map[string]storedObject),
	}
}

func (g *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, "/control/") {
		g.serveControl(w, r)
		return
	}

	if r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/"+g.bucket+"/o" {
		var meta struct {
			Name string `json:"name"`
		}
		var payload []byte
		ct, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && strings.HasPrefix(ct, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			p1, err := mr.NextPart()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			metaBytes, _ := io.ReadAll(p1)
			_ = json.Unmarshal(metaBytes, &meta)
			p2, err := mr.NextPart()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			payload, _ = io.ReadAll(p2)
		} else {
			meta.Name = r.URL.Query().Get("name")
			payload, _ = io.ReadAll(r.Body)
		}
		leaked := g.forbiddenSub != "" && strings.Contains(string(payload), g.forbiddenSub)
		if g.putStatus != 0 && g.putStatus != http.StatusOK {
			g.putAttempts = append(g.putAttempts, putAttempt{
				Name: meta.Name, Bytes: len(payload), Status: g.putStatus, Leaked: leaked,
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(g.putStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": g.putStatus, "message": "injected object store failure"},
			})
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		g.objects[meta.Name] = storedObject{Body: payload, Updated: now}
		g.putAttempts = append(g.putAttempts, putAttempt{
			Name: meta.Name, Bytes: len(payload), Status: http.StatusOK, Leaked: leaked,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bucket":  g.bucket,
			"name":    meta.Name,
			"size":    strconv.Itoa(len(payload)),
			"updated": now,
		})
		return
	}

	if r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/"+g.bucket+"/o" {
		qPrefix := r.URL.Query().Get("prefix")
		var items []map[string]any
		keys := make([]string, 0, len(g.objects))
		for k := range g.objects {
			if strings.HasPrefix(k, qPrefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			obj := g.objects[k]
			items = append(items, map[string]any{
				"name":    k,
				"size":    strconv.Itoa(len(obj.Body)),
				"updated": obj.Updated,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		return
	}

	objPrefix := "/storage/v1/b/" + g.bucket + "/o/"
	rawPath := r.URL.EscapedPath()
	if strings.HasPrefix(rawPath, objPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(rawPath, objPrefix))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		obj, ok := g.objects[name]
		switch r.Method {
		case http.MethodGet:
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not Found"}}`))
				return
			}
			if r.URL.Query().Get("alt") == "media" {
				w.Header().Set("Content-Length", strconv.Itoa(len(obj.Body)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(obj.Body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"bucket":  g.bucket,
				"name":    name,
				"size":    strconv.Itoa(len(obj.Body)),
				"updated": obj.Updated,
			})
			return
		case http.MethodDelete:
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not Found"}}`))
				return
			}
			delete(g.objects, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	http.NotFound(w, r)
}

func (g *fakeGCS) serveControl(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/control/set-put-status":
		code, _ := strconv.Atoi(r.URL.Query().Get("status"))
		g.putStatus = code
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "putStatus -> %d\n", code)
	case "/control/objects":
		out := make(map[string]string, len(g.objects))
		for k, v := range g.objects {
			out[k] = string(v.Body)
		}
		leaks := 0
		for _, a := range g.putAttempts {
			if a.Leaked {
				leaks++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"objects":     out,
			"putAttempts": g.putAttempts,
			"leakCount":   leaks,
		})
	default:
		http.NotFound(w, r)
	}
}

func serveFakeGitHub(gitRoot string) http.Handler {
	var gitCGI *cgi.Handler
	if gitRoot != "" {
		gitCGI = &cgi.Handler{
			Path: "/usr/lib/git-core/git-http-backend",
			Env: []string{
				"GIT_PROJECT_ROOT=" + gitRoot,
				"GIT_HTTP_EXPORT_ALL=1",
			},
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"walkthrough-owner","id":1}`))
	})
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"walkthrough-owner","id":1}`))
	})
	mux.HandleFunc("/api/v3/repos/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		sub := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/")
		parts := strings.Split(sub, "/")
		if len(parts) == 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name":      parts[0] + "/" + parts[1],
				"default_branch": "main",
				"permissions":    map[string]bool{"admin": true, "push": true, "pull": true},
			})
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"devcode-1","user_code":"WALK-0001","verification_uri":"http://127.0.0.1/device","expires_in":600,"interval":1}`))
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_walkthrough_stub","token_type":"bearer","scope":"read:user"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if gitCGI != nil && (strings.Contains(r.URL.Path, ".git/") || strings.HasSuffix(r.URL.Path, ".git")) {
			gitCGI.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"number":1,"html_url":"http://127.0.0.1/acme/widgets/pull/1"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func main() {
	smAddr := flag.String("sm-addr", "127.0.0.1:4281", "listen address for fake Secret Manager")
	smToken := flag.String("sm-token", "fake-sm-token", "expected Bearer token for fake Secret Manager")
	smPrefix := flag.String("sm-prefix", "projects/test-proj/secrets/grain-s6-", "allowed Secret Manager resource prefix")
	gcsAddr := flag.String("gcs-addr", "127.0.0.1:4282", "listen address for fake GCS object store")
	gcsBucket := flag.String("gcs-bucket", "grain-s6-bucket", "GCS bucket name")
	forbidden := flag.String("forbidden-secret", "", "substring that must never appear in any uploaded object")
	ghAddr := flag.String("gh-addr", "127.0.0.1:4283", "listen address for fake GitHub stub")
	gitRoot := flag.String("git-root", "", "optional directory of bare .git repositories served via git-http-backend")
	flag.Parse()

	sm := newFakeSM(*smToken, *smPrefix)
	gcs := newFakeGCS(*gcsBucket, *forbidden)

	errCh := make(chan error, 3)
	go func() {
		log.Printf("fake Secret Manager listening on http://%s", *smAddr)
		errCh <- http.ListenAndServe(*smAddr, sm)
	}()
	go func() {
		log.Printf("fake GCS listening on http://%s", *gcsAddr)
		errCh <- http.ListenAndServe(*gcsAddr, gcs)
	}()
	go func() {
		log.Printf("fake GitHub listening on http://%s", *ghAddr)
		errCh <- http.ListenAndServe(*ghAddr, serveFakeGitHub(*gitRoot))
	}()
	log.Fatal(<-errCh)
}
