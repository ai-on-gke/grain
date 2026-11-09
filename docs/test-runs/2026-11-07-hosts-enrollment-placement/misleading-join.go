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

// misleading-join files signed host join requests whose self-description
// could mislead the deployment owner (an ANSI escape, a forged newline, a
// bidi override, an oversized field), plus one honest control request, and
// prints what the daemon answers for each. A walkthrough helper for
// docs/test-runs/2026-11-07-hosts-enrollment-placement.md:
//
//	go run ./docs/test-runs/2026-11-07-hosts-enrollment-placement/misleading-join.go \
//	    http://127.0.0.1:4280 <enrollment-code>
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func main() {
	server, code := os.Args[1], os.Args[2]
	cases := []struct {
		name string
		mut  func(*hostd.JoinSubmitRequest)
	}{
		{"ansi escape in hostname", func(r *hostd.JoinSubmitRequest) { r.Hostname = "ws\x1b[2K\x1b[1Gtrusted-box" }},
		{"newline forging a line", func(r *hostd.JoinSubmitRequest) { r.LocalUser = "eve\nEnrolled by: owner@example.com" }},
		{"bidi override in version", func(r *hostd.JoinSubmitRequest) { r.Version = "v3.2.0 \u202egpj.exe" }},
		{"oversized os", func(r *hostd.JoinSubmitRequest) { r.OS = strings.Repeat("l", 65) }},
		{"comma inside a cli name", func(r *hostd.JoinSubmitRequest) { r.CLIs = []string{"claude,codex"} }},
		{"honest control (expect 401 github_device)", func(*hostd.JoinSubmitRequest) {}},
	}
	for _, c := range cases {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		req := hostd.JoinSubmitRequest{
			EnrollmentCode: code, Hostname: "walk-misleading", OS: "linux", Arch: "amd64",
			CLIs: []string{"claude"}, Version: "v0.0.0-walk", LocalUser: "debian",
		}
		c.mut(&req)
		if err := hostd.SignJoinSubmit(&req, priv, time.Now().UTC()); err != nil {
			panic(err)
		}
		body, _ := json.Marshal(req)
		resp, err := http.Post(server+"/api/hostd/join", "application/json", bytes.NewReader(body))
		if err != nil {
			panic(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("%-42s -> %d %s\n", c.name, resp.StatusCode, strings.TrimSpace(string(out)))
	}
}
