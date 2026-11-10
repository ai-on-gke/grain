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

// hdrtap is a logging reverse proxy for the §7 mixed-version walk: it sits
// between a grain daemon (or the grain CLI) and a hub and prints, for every
// request, the method, path and which X-Grain-*, Authorization and Upgrade
// headers it carried (values redacted to their first 8 bytes), so the
// transcript records exactly which header names each side sends. Upgrades
// (the ws-mux tunnel) pass through httputil.ReverseProxy unchanged.
//
//	go run hdrtap.go -addr 127.0.0.1:4230 -upstream http://127.0.0.1:4231
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	upstream := flag.String("upstream", "", "hub URL to forward to")
	label := flag.String("label", "tap", "prefix for every log line")
	flag.Parse()
	u, err := url.Parse(*upstream)
	if err != nil || u.Host == "" {
		log.Fatalf("hdrtap: -upstream: %v", err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	out := log.New(os.Stdout, "", log.LstdFlags)
	http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var names []string
		for k, v := range r.Header {
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "x-grain-") || lk == "authorization" || lk == "upgrade" || strings.HasPrefix(lk, "x-test-") {
				val := strings.Join(v, ",")
				if len(val) > 8 {
					val = val[:8] + "…"
				}
				names = append(names, fmt.Sprintf("%s=%s", k, val))
			}
		}
		sort.Strings(names)
		out.Printf("%s %s %s %s", *label, r.Method, r.URL.Path, strings.Join(names, " "))
		rp.ServeHTTP(w, r)
	}))
}
