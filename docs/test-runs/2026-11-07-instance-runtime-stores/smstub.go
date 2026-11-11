//go:build ignore

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

// smstub is a Secret Manager v1 REST stand-in for the 2026-11-07 §2
// walkthrough (pkg/secrets talks REST, not gRPC): secrets.create,
// secrets.addVersion, versions.access (latest), versions.destroy and
// secrets.delete, with a static bearer token. GET /_dump prints every
// secret's versions and states (values base64, as stored); POST
// /_drop?id=ID deletes a secret out of band, as somebody in the Cloud
// console would.
//
//	go run smstub.go -addr 127.0.0.1:4303 -token fake-sm-token
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type version struct {
	Data  string `json:"data"`
	State string `json:"state"`
}

var (
	mu      sync.Mutex
	secrets = map[string][]version{} // "projects/p/secrets/id" -> versions (index+1 = number)
	token   string
)

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q}}`, code, msg)
}

func handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	log.Printf("%s %s", r.Method, r.URL.RequestURI())
	mu.Lock()
	defer mu.Unlock()
	switch {
	case path == "/_dump":
		names := make([]string, 0, len(secrets))
		for n := range secrets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(w, "%s\n", n)
			for i, v := range secrets[n] {
				fmt.Fprintf(w, "  v%d %s %s\n", i+1, v.State, v.Data)
			}
		}
		return
	case path == "/_drop":
		id := r.URL.Query().Get("id")
		for n := range secrets {
			if strings.HasSuffix(n, "/secrets/"+id) {
				delete(secrets, n)
				fmt.Fprintf(w, "dropped %s\n", n)
			}
		}
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		jsonErr(w, 401, "missing or wrong bearer token")
		return
	}
	if !strings.HasPrefix(path, "/v1/projects/") {
		jsonErr(w, 404, "not found")
		return
	}
	rest := strings.TrimPrefix(path, "/v1/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/secrets"):
		name := rest + "/" + r.URL.Query().Get("secretId")
		if _, ok := secrets[name]; ok {
			jsonErr(w, 409, "already exists")
			return
		}
		secrets[name] = nil
		fmt.Fprintf(w, `{"name":%q}`, name)
	case r.Method == http.MethodPost && strings.HasSuffix(rest, ":addVersion"):
		name := strings.TrimSuffix(rest, ":addVersion")
		vs, ok := secrets[name]
		if !ok {
			jsonErr(w, 404, "secret not found")
			return
		}
		var body struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		secrets[name] = append(vs, version{Data: body.Payload.Data, State: "ENABLED"})
		fmt.Fprintf(w, `{"name":"%s/versions/%d","state":"ENABLED"}`, name, len(secrets[name]))
	case r.Method == http.MethodPost && strings.HasSuffix(rest, ":destroy"):
		base := strings.TrimSuffix(rest, ":destroy")
		i := strings.LastIndex(base, "/versions/")
		name, num := base[:i], base[i+len("/versions/"):]
		n, _ := strconv.Atoi(num)
		vs, ok := secrets[name]
		if !ok || n < 1 || n > len(vs) {
			jsonErr(w, 404, "version not found")
			return
		}
		vs[n-1] = version{State: "DESTROYED"}
		fmt.Fprintf(w, `{"name":"%s","state":"DESTROYED"}`, base)
	case r.Method == http.MethodGet && strings.HasSuffix(rest, "/versions/latest:access"):
		name := strings.TrimSuffix(rest, "/versions/latest:access")
		vs, ok := secrets[name]
		if !ok {
			jsonErr(w, 404, "secret not found")
			return
		}
		for i := len(vs) - 1; i >= 0; i-- {
			if vs[i].State == "ENABLED" {
				fmt.Fprintf(w, `{"name":"%s/versions/%d","payload":{"data":%q}}`, name, i+1, vs[i].Data)
				return
			}
		}
		jsonErr(w, 404, "no enabled version")
	case r.Method == http.MethodDelete:
		if _, ok := secrets[rest]; !ok {
			jsonErr(w, 404, "secret not found")
			return
		}
		delete(secrets, rest)
		io.WriteString(w, "{}")
	default:
		jsonErr(w, 400, "unsupported "+r.Method+" "+path)
	}
}

func main() {
	addr := flag.String("addr", "", "listen address (required; the walkthrough uses loopback port 4303)")
	flag.StringVar(&token, "token", "fake-sm-token", "bearer token to require")
	flag.Parse()
	if *addr == "" {
		log.Fatal("smstub: -addr is required")
	}
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("smstub listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(handler)))
}
