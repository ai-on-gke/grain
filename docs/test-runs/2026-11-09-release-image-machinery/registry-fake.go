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

// registry-fake.go: an HTTPS stand-in for hub.docker.com (the v2 tags API
// cmd/ghcr-prune lists and deletes through) and api.github.com (the
// branch list it reads liveness from, and GHCR's package-versions API for
// the legacy guest-base run), for 2026-11-09 §6 item 2.
//
// It answers as hub.docker.com and api.github.com from one listener; the
// run script points both names at 127.0.0.1 in /etc/hosts and hands the
// binary the minted CA through SSL_CERT_FILE, so the real, unmodified
// `go run ./cmd/ghcr-prune` is what talks to it.
//
// The tag mix is what scripts/release-images.sh publishes today, aged
// against the wall clock: `latest`+`v3.1.0`+`sha-8a740f9` (the promoted
// release), its cosign `.sig`, `main`'s head, `release..v3.2.0`'s head,
// the candidate rc (`v3.2.0-rc.2`,`candidate`,`rc`), a discarded rc,
// a `v3.2.0-pending` staging build, a live and a deleted task branch,
// 35 superseded main builds, signatures of kept and of deletable
// builds, an orphan signature, and on sandbox-base bare-hex granule
// cache keys. GHCR's copy (grain/guest-base, grain/grain) adds untagged
// versions, which only GHCR can list.
//
// Every DELETE is appended to -out/deletes.log, and the tag (Docker Hub)
// or version (GHCR) goes away, so a second dry run shows what is left.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type version struct {
	id      int64
	digest  string
	tags    []string
	created time.Time
}

var (
	mu       sync.Mutex
	hub      = map[string][]*version{} // docker hub repo -> versions
	ghcr     = map[string][]*version{} // "grain/guest-base" -> versions
	nextID   int64
	deletes  *os.File
	branches = []string{"main", "release/v3.2.0", "candidate", "grain/098073e4/task-3733-walkthrough-codex-504-fallback"}
)

func digest(seed string) string {
	s := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(s[:])
}

func hexOf(seed string, n int) string { return strings.TrimPrefix(digest(seed), "sha256:")[:n] }

func add(m map[string][]*version, pkg string, created time.Time, tags ...string) *version {
	nextID++
	v := &version{id: nextID, digest: digest(fmt.Sprintf("%s/%d", pkg, nextID)), tags: tags, created: created}
	m[pkg] = append(m[pkg], v)
	return v
}

func sig(m map[string][]*version, pkg string, of *version, created time.Time) {
	add(m, pkg, created, "sha256-"+strings.TrimPrefix(of.digest, "sha256:")+".sig")
}

func seed(now time.Time) {
	h := func(d float64) time.Time { return now.Add(-time.Duration(d * float64(time.Hour))) }
	for _, isGHCR := range []bool{false, true} {
		m, pkgs := hub, []string{"grain", "sandbox", "sandbox-k8s", "sandbox-base"}
		if isGHCR {
			m = ghcr
			pkgs = []string{"grain/grain", "grain/guest-base"}
		}
		for _, pkg := range pkgs {
			rel := add(m, pkg, h(240), "latest", "v3.1.0", "sha-8a740f9")
			sig(m, pkg, rel, h(240))
			head := add(m, pkg, h(1), "main", "sha-a59d1e4")
			sig(m, pkg, head, h(1))
			add(m, pkg, h(72), "release..v3.2.0", "sha-0a1b2c3")
			add(m, pkg, h(70), "v3.2.0-rc.2", "candidate", "rc")
			add(m, pkg, h(140), "v3.2.0-rc.1", "v3.2.0-rc.1-discarded")
			add(m, pkg, h(96), "v3.2.0-pending")
			add(m, pkg, h(100), "grain..098073e4..task-3733-walkthrough-codex-504-fallback", "sha-1234abc")
			add(m, pkg, h(204), "grain..098073e4..task-3600-old", "sha-dead001")
			for i := 0; i < 35; i++ {
				v := add(m, pkg, h(60+float64(i)*4), "sha-"+hexOf(fmt.Sprintf("main-%d", i), 7))
				if i == 34 {
					sig(m, pkg, v, h(60+float64(i)*4)) // signs a build that falls out of the rank
				}
			}
			// An orphan signature: what it signs is long gone.
			add(m, pkg, h(300), "sha256-"+hexOf("gone", 64)+".sig")
			if strings.HasSuffix(pkg, "sandbox-base") || strings.HasSuffix(pkg, "guest-base") {
				head.tags = append(head.tags, hexOf("granule-head", 16))
				for i := 0; i < 8; i++ {
					add(m, pkg, h(72+float64(i)*48), hexOf(fmt.Sprintf("granule-%d", i), 16))
				}
			}
			if isGHCR {
				add(m, pkg, h(500)) // untagged: an index child or debris
				add(m, pkg, h(500))
			}
		}
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:443", "listen address")
	out := flag.String("out", "/tmp/rim-prune", "directory for ca.pem and deletes.log")
	flag.Parse()
	must(os.MkdirAll(*out, 0o755))
	seed(time.Now().UTC())
	var err error
	deletes, err = os.OpenFile(filepath.Join(*out, "deletes.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	must(err)
	cert := mintCerts(*out)

	srv := &http.Server{Addr: *addr, Handler: http.HandlerFunc(serve), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	log.Printf("registry fake on %s", *addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func serve(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	host := strings.Split(r.Host, ":")[0]
	p := r.URL.EscapedPath()
	log.Printf("%s %s%s", r.Method, host, r.URL.RequestURI())
	js := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case host == "hub.docker.com" && p == "/v2/users/login/" && r.Method == "POST":
		js(200, map[string]string{"token": "fake-jwt"})
	case host == "hub.docker.com" && strings.HasPrefix(p, "/v2/repositories/grainai/"):
		rest := strings.Split(strings.TrimPrefix(p, "/v2/repositories/grainai/"), "/")
		pkg := rest[0]
		vs, ok := hub[pkg]
		if !ok {
			js(404, map[string]string{"message": "object not found"})
			return
		}
		if r.Method == "GET" && len(rest) >= 2 && rest[1] == "tags" && (len(rest) == 2 || rest[2] == "") {
			type row struct {
				Name          string `json:"name"`
				Digest        string `json:"digest"`
				LastUpdated   string `json:"last_updated"`
				TagLastPushed string `json:"tag_last_pushed"`
			}
			var rows []row
			for _, v := range vs {
				for _, t := range v.tags {
					ts := v.created.Format(time.RFC3339Nano)
					rows = append(rows, row{t, v.digest, ts, ts})
				}
			}
			// Paged 100 at a time with an absolute `next`, as Docker Hub does.
			page := 1
			fmt.Sscan(r.URL.Query().Get("page"), &page)
			lo, hi := (page-1)*100, page*100
			next := ""
			if hi < len(rows) {
				next = fmt.Sprintf("https://hub.docker.com/v2/repositories/grainai/%s/tags/?page_size=100&page=%d", pkg, page+1)
			} else {
				hi = len(rows)
			}
			if lo > hi {
				lo = hi
			}
			js(200, map[string]any{"count": len(rows), "next": next, "results": rows[lo:hi]})
			return
		}
		if r.Method == "DELETE" && len(rest) >= 3 && rest[1] == "tags" {
			if r.Header.Get("Authorization") != "Bearer fake-jwt" {
				js(401, map[string]string{"detail": "auth"})
				return
			}
			tag, _ := url.PathUnescape(rest[2])
			for _, v := range vs {
				for i, t := range v.tags {
					if t == tag {
						v.tags = append(v.tags[:i], v.tags[i+1:]...)
						fmt.Fprintf(deletes, "DELETE docker.io/grainai/%s:%s\n", pkg, tag)
						w.WriteHeader(204)
						return
					}
				}
			}
			js(404, map[string]string{"message": "tag not found"})
			return
		}
		js(404, map[string]string{"message": "not found"})
	case host == "api.github.com" && strings.HasPrefix(p, "/repos/ai-on-gke/grain/branches"):
		var items []map[string]string
		for _, b := range branches {
			items = append(items, map[string]string{"name": b})
		}
		js(200, items)
	case host == "api.github.com" && strings.HasPrefix(p, "/orgs/ai-on-gke/packages/container/"):
		rest := strings.Split(strings.TrimPrefix(p, "/orgs/ai-on-gke/packages/container/"), "/")
		pkg, _ := url.PathUnescape(rest[0])
		vs, ok := ghcr[pkg]
		if !ok || len(rest) < 2 || rest[1] != "versions" {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		if r.Method == "GET" && len(rest) == 2 {
			sorted := append([]*version(nil), vs...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].created.After(sorted[j].created) })
			var items []map[string]any
			for _, v := range sorted {
				tags := v.tags
				if tags == nil {
					tags = []string{}
				}
				items = append(items, map[string]any{"id": v.id, "name": v.digest, "created_at": v.created.Format(time.RFC3339),
					"metadata": map[string]any{"package_type": "container", "container": map[string]any{"tags": tags}}})
			}
			if r.URL.Query().Get("per_page") == "1" && len(items) > 0 {
				items = items[:1]
			}
			js(200, items)
			return
		}
		if r.Method == "DELETE" && len(rest) == 3 {
			var id int64
			fmt.Sscan(rest[2], &id)
			for i, v := range vs {
				if v.id == id {
					ghcr[pkg] = append(vs[:i], vs[i+1:]...)
					fmt.Fprintf(deletes, "DELETE ghcr.io/ai-on-gke/%s id=%d tags=%v\n", pkg, id, v.tags)
					w.WriteHeader(204)
					return
				}
			}
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(404, map[string]string{"message": "Not Found"})
	default:
		js(404, map[string]string{"message": "Not Found"})
	}
}

func mintCerts(out string) tls.Certificate {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rim registry fake CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	must(err)
	caCert, _ := x509.ParseCertificate(caDER)
	must(os.WriteFile(filepath.Join(out, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644))
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "hub.docker.com"},
		DNSNames:  []string{"hub.docker.com", "api.github.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	must(err)
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
