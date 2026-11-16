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

// kmsfakesrv runs pkg/secrets/kmsfake -- the stand-in for Cloud KMS's
// encrypt/decrypt REST methods -- as a standalone server, so a real
// `grain daemon` and `grain state key wrap` reach it through
// GRAIN_KMS_ENDPOINT. kmsfake.Start wants a testing.TB, so this hands it
// one whose Setenv and Cleanup are enough for a process that never ends;
// the caller exports GRAIN_KMS_ENDPOINT=http://<addr>/v1/ and
// GRAIN_KMS_TOKEN=fake-kms-token itself.
//
// In front of the fake sits a control plane on -ctl:
//
//	GET /deny?on=1|0   every call answers 403 (IAM revoked)
//	GET /down?on=1|0   every call answers 503 (KMS unreachable)
//	GET /counts        {"encrypts":N,"decrypts":N,"signs":N}
//
// It is the 2026-11-11 secrets-store walkthrough's helper, with the
// asymmetricSign count added for 2026-11-13 §9 item 5.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
)

type tb struct{ testing.TB }

func (tb) Helper()                  {}
func (tb) Cleanup(func())           {}
func (tb) Setenv(key, value string) { _ = os.Setenv(key, value) }

func main() {
	addr := flag.String("addr", "127.0.0.1:4275", "address GRAIN_KMS_ENDPOINT points at")
	ctl := flag.String("ctl", "127.0.0.1:4276", "control address")
	flag.Parse()

	f := kmsfake.Start(tb{})
	upstream, _ := url.Parse(f.URL())
	upstream.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var down atomic.Bool

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/deny", func(w http.ResponseWriter, r *http.Request) {
			f.Deny(r.URL.Query().Get("on") == "1")
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
			down.Store(r.URL.Query().Get("on") == "1")
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("/counts", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]int{"encrypts": f.Encrypts(), "decrypts": f.Decrypts(), "signs": f.Signs()})
		})
		log.Fatal(http.ListenAndServe(*ctl, mux))
	}()

	log.Printf("kmsfakesrv: GRAIN_KMS_ENDPOINT=http://%s/v1/ (control on %s)", *addr, *ctl)
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, `{"error":{"code":503,"message":"The service is currently unavailable."}}`, http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})))
}
