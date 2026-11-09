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

// gcpstub is a local stand-in for the Google boundaries the hosted-instance
// walkthrough needs (task 3731): the GCE metadata server, Secret Manager, IAM,
// Cloud Resource Manager and the Compute instances API. Nothing here talks to
// real GCP; every project name is made up.
//
//	go run gcpstub.go -addr 127.0.0.1:4271 -state /tmp/hic/stub
//
// Control endpoints (all under /_stub):
//
//	POST /_stub/metadata?attr=grain-config   body = attribute value
//	POST /_stub/secret?name=projects/p/secrets/s  body = payload (adds a version)
//	POST /_stub/sm-fail?code=503&count=2     next N Secret Manager reads fail
//	POST /_stub/sa-delay?seconds=N           a new service account stays invisible to
//	                                         Compute for N seconds (-1 = forever)
//	GET  /_stub/log                          every request seen, one per line
//	GET  /_stub/secrets                      secret -> version count (JSON)
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
)

type stub struct {
	mu        sync.Mutex
	attrs     map[string]string
	secrets   map[string][]string // full name -> versions (raw payloads)
	smFail    int
	smFailN   int
	saDelay   time.Duration // <0: never visible
	saCreated map[string]time.Time
	instances map[string]map[string]any
	ops       int
	reqLog    []string
	logFile   *os.File
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4271", "listen address")
	state := flag.String("state", "/tmp/hic/stub", "directory for the request log")
	vmIP := flag.String("vm-ip", "127.0.0.1", "networkIP reported for created instances")
	vmAddr := flag.String("vm-addr", "", "also serve a healthy hosted VM's /healthz and /api/registry/status here (the gce provider probes networkIP on gcp.InstanceHTTPPort; provision-walk.sh passes it)")
	flag.Parse()
	if *vmAddr != "" {
		go func() {
			vm := http.NewServeMux()
			vm.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
			vm.HandleFunc("/api/registry/status", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, api.InstanceStatusResponse{GrainVersion: "v3.2.0-walk", Health: api.HealthOK})
			})
			log.Fatal(http.ListenAndServe(*vmAddr, vm))
		}()
	}
	_ = os.MkdirAll(*state, 0o755)
	lf, err := os.OpenFile(filepath.Join(*state, "requests.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	s := &stub{
		attrs:     map[string]string{},
		secrets:   map[string][]string{},
		saCreated: map[string]time.Time{},
		instances: map[string]map[string]any{},
		logFile:   lf,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_stub/", s.control)
	mux.HandleFunc("/computeMetadata/v1/", s.metadata)
	mux.HandleFunc("/sm/", s.secretManager)
	mux.HandleFunc("/iam/", s.iam)
	mux.HandleFunc("/crm/", s.crm)
	mux.HandleFunc("/compute/", func(w http.ResponseWriter, r *http.Request) { s.compute(w, r, *vmIP) })
	log.Printf("gcpstub listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func (s *stub) record(r *http.Request, status int) {
	// Never log bodies or Authorization headers: the walkthrough greps this
	// log for the database password.
	line := fmt.Sprintf("%s %s %s -> %d", time.Now().UTC().Format(time.RFC3339), r.Method, r.URL.RequestURI(), status)
	s.reqLog = append(s.reqLog, line)
	fmt.Fprintln(s.logFile, line)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gerr(w http.ResponseWriter, status int, reason, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "status": reason, "message": msg}})
}

func (s *stub) control(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	q := r.URL.Query()
	switch r.URL.Path {
	case "/_stub/metadata":
		if r.Method == http.MethodDelete {
			delete(s.attrs, q.Get("attr"))
		} else {
			s.attrs[q.Get("attr")] = string(body)
		}
	case "/_stub/secret":
		s.secrets[q.Get("name")] = append(s.secrets[q.Get("name")], string(body))
	case "/_stub/sm-fail":
		s.smFail, _ = strconv.Atoi(q.Get("code"))
		s.smFailN, _ = strconv.Atoi(q.Get("count"))
	case "/_stub/sa-delay":
		n, _ := strconv.Atoi(q.Get("seconds"))
		s.saDelay = time.Duration(n) * time.Second
	case "/_stub/log":
		fmt.Fprintln(w, strings.Join(s.reqLog, "\n"))
		return
	case "/_stub/secrets":
		counts := map[string]int{}
		for k, v := range s.secrets {
			counts[k] = len(v)
		}
		writeJSON(w, 200, counts)
		return
	case "/_stub/reset":
		s.secrets = map[string][]string{}
		s.saCreated = map[string]time.Time{}
		s.instances = map[string]map[string]any{}
	default:
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(204)
}

func (s *stub) metadata(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Metadata-Flavor") != "Google" {
		s.record(r, 403)
		http.Error(w, "missing Metadata-Flavor", 403)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/computeMetadata/v1/")
	switch {
	case rel == "instance/service-accounts/default/token":
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"access_token": "stub-vm-token", "expires_in": 3600, "token_type": "Bearer"})
	case rel == "instance/service-accounts/default/email":
		s.record(r, 200)
		fmt.Fprint(w, "gi-walk@made-up-proj.iam.gserviceaccount.com")
	case strings.HasPrefix(rel, "instance/attributes/"):
		v, ok := s.attrs[strings.TrimPrefix(rel, "instance/attributes/")]
		if !ok {
			s.record(r, 404)
			http.NotFound(w, r)
			return
		}
		s.record(r, 200)
		fmt.Fprint(w, v)
	default:
		s.record(r, 404)
		http.NotFound(w, r)
	}
}

// secretManager serves /sm/v1/projects/<p>/secrets...
func (s *stub) secretManager(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/sm/v1/")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/versions/latest:access"):
		if s.smFailN != 0 {
			if s.smFailN > 0 {
				s.smFailN--
			}
			s.record(r, s.smFail)
			gerr(w, s.smFail, http.StatusText(s.smFail), "stubbed Secret Manager failure")
			return
		}
		if r.Header.Get("Authorization") != "Bearer stub-vm-token" {
			s.record(r, 401)
			gerr(w, 401, "UNAUTHENTICATED", "bad token")
			return
		}
		name := strings.TrimSuffix(path, "/versions/latest:access")
		vs := s.secrets[name]
		if len(vs) == 0 {
			s.record(r, 404)
			gerr(w, 404, "NOT_FOUND", "Secret ["+name+"] not found or has no versions.")
			return
		}
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": fmt.Sprintf("%s/versions/%d", name, len(vs)),
			"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(vs[len(vs)-1]))}})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/secrets"):
		name := path + "/" + r.URL.Query().Get("secretId")
		if _, ok := s.secrets[name]; ok {
			s.record(r, 409)
			gerr(w, 409, "ALREADY_EXISTS", "exists")
			return
		}
		s.secrets[name] = nil
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": name})
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":addVersion"):
		name := strings.TrimSuffix(path, ":addVersion")
		var body struct {
			Payload struct{ Data string } `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := base64.StdEncoding.DecodeString(body.Payload.Data)
		s.secrets[name] = append(s.secrets[name], string(raw))
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": fmt.Sprintf("%s/versions/%d", name, len(s.secrets[name]))})
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{})
	case r.Method == http.MethodDelete:
		delete(s.secrets, path)
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/secrets"):
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{})
	default:
		s.record(r, 404)
		gerr(w, 404, "NOT_FOUND", path)
	}
}

// iam serves /iam/v1/projects/<p>/serviceAccounts[/<email>].
func (s *stub) iam(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/iam/v1/")
	parts := strings.Split(path, "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		if _, ok := s.saCreated[parts[3]]; ok {
			s.record(r, 200)
			writeJSON(w, 200, map[string]any{"email": parts[3]})
			return
		}
		s.record(r, 404)
		gerr(w, 404, "NOT_FOUND", "Unknown service account")
	case r.Method == http.MethodPost && len(parts) == 3:
		var body struct{ AccountID string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", body.AccountID, parts[1])
		if _, ok := s.saCreated[email]; !ok {
			s.saCreated[email] = time.Now()
		}
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"email": email})
	case r.Method == http.MethodDelete:
		delete(s.saCreated, parts[len(parts)-1])
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{})
	default:
		s.record(r, 404)
		gerr(w, 404, "NOT_FOUND", path)
	}
}

func (s *stub) crm(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(r, 200)
	writeJSON(w, 200, map[string]any{"bindings": []any{}, "etag": "BwX="})
}

func (s *stub) saVisible(email string) bool {
	created, ok := s.saCreated[email]
	if !ok {
		return false
	}
	if s.saDelay < 0 {
		return false
	}
	return time.Since(created) >= s.saDelay
}

// compute serves /compute/v1/projects/<p>/zones/<z>/{instances[/<n>],operations/<op>}.
func (s *stub) compute(w http.ResponseWriter, r *http.Request, vmIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		s.record(r, 404)
		gerr(w, 404, "notFound", path)
		return
	}
	zone := parts[3]
	switch {
	case parts[4] == "operations" && len(parts) >= 6:
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": parts[5], "status": "DONE"})
	case parts[4] == "instances" && len(parts) == 5 && r.Method == http.MethodPost:
		var inst map[string]any
		_ = json.NewDecoder(r.Body).Decode(&inst)
		s.ops++
		op := fmt.Sprintf("op-%d", s.ops)
		sa := ""
		if l, ok := inst["serviceAccounts"].([]any); ok && len(l) > 0 {
			sa, _ = l[0].(map[string]any)["email"].(string)
		}
		if sa != "" && !s.saVisible(sa) {
			// What GCE does while a just-created account propagates: the insert
			// operation completes with EXTERNAL_RESOURCE_NOT_FOUND.
			s.record(r, 200)
			writeJSON(w, 200, map[string]any{"name": op, "status": "DONE", "error": map[string]any{"errors": []any{map[string]any{
				"code":    "EXTERNAL_RESOURCE_NOT_FOUND",
				"message": fmt.Sprintf("The resource '%s' of type 'serviceAccount' was not found.", sa),
			}}}})
			return
		}
		name, _ := inst["name"].(string)
		inst["status"] = "RUNNING"
		inst["zone"] = "projects/" + parts[1] + "/zones/" + zone
		inst["id"] = "1234567890"
		if nis, ok := inst["networkInterfaces"].([]any); ok && len(nis) > 0 {
			nis[0].(map[string]any)["networkIP"] = vmIP
		} else {
			inst["networkInterfaces"] = []any{map[string]any{"networkIP": vmIP}}
		}
		s.instances[name] = inst
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": op, "status": "DONE"})
	case parts[4] == "instances" && len(parts) == 5:
		items := []any{}
		for _, v := range s.instances {
			items = append(items, v)
		}
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"items": items})
	case parts[4] == "instances" && len(parts) == 6 && r.Method == http.MethodGet:
		inst, ok := s.instances[parts[5]]
		if !ok {
			s.record(r, 404)
			gerr(w, 404, "notFound", "The resource '"+parts[5]+"' was not found")
			return
		}
		s.record(r, 200)
		writeJSON(w, 200, inst)
	case parts[4] == "instances" && len(parts) == 6 && r.Method == http.MethodDelete:
		delete(s.instances, parts[5])
		s.ops++
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": fmt.Sprintf("op-%d", s.ops), "status": "DONE"})
	default:
		s.ops++
		s.record(r, 200)
		writeJSON(w, 200, map[string]any{"name": fmt.Sprintf("op-%d", s.ops), "status": "DONE", "items": []any{}})
	}
}
