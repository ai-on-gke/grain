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

// renew-probe drives POST /api/hostd/renew for a connected key-bound host
// the way its host agent's in-band renewal does, and prints what the
// daemon answers to: a proof signed with a different Ed25519 key, the
// host's own proof, the very same proof sent again (a replay), and a
// proof for the credential the renewal just replaced. It writes the
// newest renewed credential back to the token file, so restart the
// host agent afterwards to pick it up. A walkthrough helper for
// docs/test-runs/2026-11-07-hosts-enrollment-placement.md:
//
//	go run ./docs/test-runs/2026-11-07-hosts-enrollment-placement/renew-probe.go \
//	    http://127.0.0.1:4280 /tmp/host-b/home/hostd-token /tmp/host-b/home/hostd-key
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

func post(server string, proof hostd.RenewProof) (int, string) {
	body, _ := json.Marshal(proof)
	resp, err := http.Post(server+"/api/hostd/renew", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func redact(s string) string {
	var r hostd.RenewReply
	if json.Unmarshal([]byte(s), &r) == nil && r.Token != "" {
		return fmt.Sprintf("{token: %s…, expires_at: %s}", r.Token[:12], r.ExpiresAt.Format(time.RFC3339))
	}
	return s
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: renew-probe <server> <token-file> <key-file>")
		os.Exit(2)
	}
	server, tokenFile, keyFile := os.Args[1], os.Args[2], os.Args[3]
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		panic(err)
	}
	tok := strings.TrimSpace(string(raw))
	priv, err := hostd.LoadPrivateKey(keyFile)
	if err != nil {
		panic(err)
	}
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)

	p, _ := hostd.SignRenewProof("", tok, wrong, time.Now())
	code, body := post(server, p)
	fmt.Printf("%-34s -> %d %s\n", "proof signed with a different key", code, body)

	p, _ = hostd.SignRenewProof("", tok, priv, time.Now())
	code, body = post(server, p)
	fmt.Printf("%-34s -> %d %s\n", "the host's own proof", code, redact(body))
	var rep hostd.RenewReply
	if code == http.StatusOK && json.Unmarshal([]byte(body), &rep) == nil && rep.Token != "" {
		if err := hostd.WriteSecretFile0600(tokenFile, []byte(rep.Token+"\n")); err != nil {
			panic(err)
		}
	}

	code, body = post(server, p)
	fmt.Printf("%-34s -> %d %s\n", "the same proof replayed", code, redact(body))

	// The superseded credential still renews for hostdRenewGrace (2m) --
	// the window a renewal racing a reconnect needs -- and doing so
	// supersedes the one just written, so keep whichever came last.
	p, _ = hostd.SignRenewProof("", tok, priv, time.Now())
	code, body = post(server, p)
	fmt.Printf("%-34s -> %d %s\n", "fresh proof, superseded credential", code, redact(body))
	rep = hostd.RenewReply{}
	if code == http.StatusOK && json.Unmarshal([]byte(body), &rep) == nil && rep.Token != "" {
		if err := hostd.WriteSecretFile0600(tokenFile, []byte(rep.Token+"\n")); err != nil {
			panic(err)
		}
	}
}
