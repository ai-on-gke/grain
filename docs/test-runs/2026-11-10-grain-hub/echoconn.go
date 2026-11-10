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

// echoconn is a connected entry for the §7 mixed-version walk that answers
// every request the hub forwards over its ws-mux tunnel with the X-Grain-*
// header names the request arrived with, so the transcript records which
// assertion headers each hub version sends to a daemon. It dials the hub with
// the same pkg/hub/api.ConnectMux the HEAD daemon uses.
//
//	go run echoconn.go -hub http://127.0.0.1:4231 -entry echo -token-file grc.tok
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

func main() {
	hub := flag.String("hub", "", "hub base URL")
	entry := flag.String("entry", "", "entry name")
	tokenFile := flag.String("token-file", "", "file holding the grc_ registration token")
	flag.Parse()
	tok, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	sess, err := api.ConnectMux(ctx, api.ConnectConfig{
		BaseURL: *hub, Entry: *entry, RegistrationToken: strings.TrimSpace(string(tok)),
		GrainVersion: "echoconn", HostKind: "other", AllowInsecureHTTP: true,
	}, nil)
	if err != nil {
		log.Fatalf("echoconn: connect: %v", err)
	}
	log.Printf("echoconn: connected session %s (%s)", sess.SessionID, sess.Protocol)
	log.Fatal(http.Serve(sess.Listener(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var names []string
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-grain-") {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"host": r.Host, "path": r.URL.Path, "x_grain_headers": names})
	})))
}
