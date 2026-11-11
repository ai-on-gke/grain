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

// forge.go serves the GitHub simulator as a GitHub Enterprise Server
// would be reached by grain daemon -upgrade-reviewed-merge-repo
// (https://<host>/api/v3), over TLS with a throwaway CA, with a history
// built on the commit the daemon under test runs:
//
//	main:         running -- R (merge of PR #1: alice's change, approved by bob, merged by alice)
//	release/v9.9: running -- S (merge of PR #2: mallory's change, merged by mallory, no review)
//	main-stale:   running -- T (merge of PR #3: carol's change, approved by bob at an earlier head)
//
// Usage:
//
//	go run docs/test-runs/2026-11-11-upgrades/forge.go -src /home/debian/work \
//	    -running $(grain version --json | jq -r .revision) -addr 127.0.0.1:8443 -out /tmp/forge
//
// It writes $out/ca.pem (for SSL_CERT_FILE) and $out/revs.env (RUNNING, REVIEWED,
// SELFMERGED, STALE) and serves until killed.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func git(dir string, env []string, args ...string) string {
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func main() {
	src := flag.String("src", "/home/debian/work", "checkout holding the running commit")
	running := flag.String("running", "", "commit the daemon under test runs")
	addr := flag.String("addr", "127.0.0.1:8443", "listen address")
	out := flag.String("out", "/tmp/forge", "output directory")
	flag.Parse()
	if *running == "" {
		log.Fatal("-running is required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	bare := filepath.Join(*out, "widgets.git")
	_ = os.RemoveAll(bare)
	if b, err := exec.Command("git", "clone", "-q", "--bare", "--shared", "--no-tags", *src, bare).CombinedOutput(); err != nil {
		log.Fatalf("clone: %v\n%s", err, b)
	}
	tree := git(bare, nil, "rev-parse", *running+"^{tree}")
	as := func(who string) []string {
		return []string{"GIT_AUTHOR_NAME=" + who, "GIT_AUTHOR_EMAIL=" + who + "@example.com",
			"GIT_COMMITTER_NAME=" + who, "GIT_COMMITTER_EMAIL=" + who + "@example.com"}
	}
	commit := func(who, msg string, parents ...string) string {
		args := []string{"commit-tree", tree, "-m", msg}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		return git(bare, as(who), args...)
	}
	h1 := commit("alice", "widgets: alice's change", *running)
	r := commit("alice", "Merge pull request #1 from acme/alice-change", *running, h1)
	h2 := commit("mallory", "widgets: mallory's change", *running)
	s := commit("mallory", "Merge pull request #2 from acme/mallory-change", *running, h2)
	h3a := commit("carol", "widgets: carol's change", *running)
	h3 := commit("carol", "widgets: carol's change, amended after review", h3a)
	tc := commit("carol", "Merge pull request #3 from acme/carol-change", *running, h3)
	for ref, sha := range map[string]string{
		"refs/heads/main": r, "refs/heads/alice-change": h1,
		"refs/heads/release/v9.9": s, "refs/heads/mallory-change": h2,
		"refs/heads/main-stale": tc, "refs/heads/carol-change": h3,
		"refs/heads/running": *running,
	} {
		git(bare, nil, "update-ref", ref, sha)
	}

	sim := githubsim.New("acme", "widgets", bare, "main")
	now := time.Now().UTC()
	sim.PullRequests = []githubsim.PullRequest{
		{Number: 1, Head: "alice-change", Base: "main", State: "closed", Merged: true, MergedAt: now, Author: "alice", MergedBy: "alice", MergeCommitSHA: r},
		{Number: 2, Head: "mallory-change", Base: "release/v9.9", State: "closed", Merged: true, MergedAt: now, Author: "mallory", MergedBy: "mallory", MergeCommitSHA: s},
		{Number: 3, Head: "carol-change", Base: "main", State: "closed", Merged: true, MergedAt: now, Author: "carol", MergedBy: "carol", MergeCommitSHA: tc},
	}
	sim.PullReviews = map[int][]githubsim.PullReview{
		1: {{Login: "bob", State: "APPROVED", CommitID: h1, Association: "COLLABORATOR"}},
		3: {{Login: "bob", State: "APPROVED", CommitID: h3a, Association: "COLLABORATOR"}},
	}

	// A throwaway CA and a leaf for the listen address.
	host, _, _ := net.SplitHostPort(*addr)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "walkthrough forge"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP(host)}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(*out, "ca.pem"), certPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	if err != nil {
		log.Fatal(err)
	}
	env := fmt.Sprintf("RUNNING=%s\nREVIEWED=%s\nSELFMERGED=%s\nSTALE=%s\nREPO=%s/acme/widgets\n", *running, r, s, tc, *addr)
	if err := os.WriteFile(filepath.Join(*out, "revs.env"), []byte(env), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Print(env)

	api := sim.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/v3/", http.StripPrefix("/api/v3", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("forge: %s %s", r.Method, r.URL.RequestURI())
		api.ServeHTTP(w, r)
	})))
	srv := &http.Server{Addr: *addr, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	log.Printf("forge: serving https://%s/api/v3", *addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
