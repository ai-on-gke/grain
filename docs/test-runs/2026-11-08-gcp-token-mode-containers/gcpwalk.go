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

// gcpwalk <mcp-addr> <token-listen> <control-addr> <bearer>: the walk's gcp
// capability server -- grain's own configurable gcp server
// (intree.NewConfigurableGCP), served over Streamable HTTP the way
// pkg/demo/demo_capability_server.go serves it, over gcpfake's in-memory
// IAM instead of GCP. The existing account deployer@walk-proj is made and
// bound to roles/run.admin, as the demo's is.
//
// A real run's grant deadline is at least an hour and three quarters away
// (orchestrator.capabilityDeadline: setup allowance + max runtime + wait
// credit), so the server and gcpfake read a walk clock that the control
// listener moves forward: POST /advance?by=<duration> skips the clock
// ahead, GET /now prints it. GET /lifetimes prints the lifetimes gcpfake's
// generateAccessToken was asked for.
//
//	go run docs/test-runs/2026-11-08-gcp-token-mode-containers/gcpwalk.go 127.0.0.1:4260 127.0.0.1:4252 127.0.0.1:4261 <64 hex>
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
)

const account = "deployer@walk-proj.iam.gserviceaccount.com"

var (
	mu     sync.Mutex
	offset time.Duration
)

func now() time.Time {
	mu.Lock()
	defer mu.Unlock()
	return time.Now().Add(offset)
}

func main() {
	mcpAddr, tokenListen, controlAddr, bearer := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	logger := log.New(os.Stderr, "gcpwalk: ", log.LstdFlags|log.LUTC)
	cloud := gcpfake.New()
	cloud.Now = now
	cloud.DefaultIdentity = "walk-admin@walk.example"
	cloud.AddAccount("walk-proj", account, "made by hand")
	cloud.Bind("projects/walk-proj", "roles/run.admin", "serviceAccount:"+account)
	gcp, err := intree.NewConfigurableGCP(intree.GCPConfig{Connector: cloud, Listen: tokenListen, Now: now, Log: logger})
	if err != nil {
		log.Fatal(err)
	}
	gcp.CapabilityServer.Token = bearer
	control := http.NewServeMux()
	control.HandleFunc("POST /advance", func(w http.ResponseWriter, r *http.Request) {
		by, err := time.ParseDuration(r.URL.Query().Get("by"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		offset += by
		mu.Unlock()
		logger.Printf("walk clock advanced by %s, now %s", by, now().UTC().Format(time.RFC3339))
		fmt.Fprintln(w, now().UTC().Format(time.RFC3339))
	})
	control.HandleFunc("GET /now", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, now().UTC().Format(time.RFC3339))
	})
	control.HandleFunc("GET /lifetimes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, cloud.Lifetimes)
	})
	go func() { log.Fatal(http.ListenAndServe(controlAddr, control)) }()
	logger.Printf("serving the gcp capability server on http://%s/mcp; token data plane on %s once configured", mcpAddr, tokenListen)
	log.Fatal(http.ListenAndServe(mcpAddr, gcp.CapabilityServer))
}
