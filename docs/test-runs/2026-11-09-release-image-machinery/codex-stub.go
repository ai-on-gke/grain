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

// codex-stub.go: a local HTTPS stand-in for github.com, api.github.com,
// release-assets.githubusercontent.com and registry.npmjs.org, used by
// run-codex-builds.sh to drive the Codex install step of Dockerfile and
// Dockerfile.k8s-sandbox through a real `docker build` (2026-11-09 §6 item 1).
//
// It mints a throwaway CA and a leaf for those four names, writes ca.pem,
// fake archives' digests (digests.env, as docker --build-arg lines) into
// -out, and logs every request (host, path, Accept, whether an
// Authorization header was sent) to -out/requests.log.
//
// -mode selects the failure being simulated:
//
//	healthy       /releases/download/ serves the archives
//	504-download  /releases/download/ 504s; the Releases API and the asset
//	              endpoint (302 -> release-assets) serve them
//	504-all       /releases/download/ and the API 504; npm serves the tgz
//	tampered      /releases/download/ serves archives that do not match
//	              the digests the build is given
//	tampered-api  /releases/download/ 504s, the asset endpoint serves
//	              tampered bytes
//	tampered-npm  everything on GitHub 504s, npm serves a tampered tgz
//	html200       /releases/download/ answers 200 with an HTML error page
//	html200-api   /releases/download/ 504s, the API answers 200 HTML
//	html200-all   every host answers 200 with an HTML error page
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	version = "rust-v0.166.0"
	npmVer  = "0.166.0"
	target  = "x86_64-unknown-linux-musl"
)

const html = "<!DOCTYPE html><html><body><h1>Unicorn! We're having a really bad day.</h1></body></html>\n"

func main() {
	mode := flag.String("mode", "healthy", "failure to simulate")
	addr := flag.String("addr", "127.0.0.1:443", "listen address")
	out := flag.String("out", "/tmp/rim-codex", "directory for ca.pem, digests.env and requests.log")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	codexTar := tgz(map[string]string{"codex-" + target: "#!/bin/sh\necho 'codex-cli 0.166.0 (stub)'\n"})
	hostTar := tgz(map[string]string{"codex-code-mode-host-" + target: "#!/bin/sh\necho 'usage: codex-code-mode-host (stub)'\n"})
	npmTar := tgz(map[string]string{
		"package/package.json":                                   `{"name":"@openai/codex","version":"` + npmVer + `-linux-x64"}`,
		"package/vendor/" + target + "/bin/codex":                "#!/bin/sh\necho 'codex-cli 0.166.0 (npm stub)'\n",
		"package/vendor/" + target + "/bin/codex-code-mode-host": "#!/bin/sh\necho 'usage: codex-code-mode-host (npm stub)'\n",
	})
	tampered := tgz(map[string]string{"codex-" + target: "#!/bin/sh\necho pwned\n"})

	env := fmt.Sprintf("CODEX_CLI_SHA256_AMD64=%s\nCODEX_CODE_MODE_HOST_SHA256_AMD64=%s\nCODEX_CLI_NPM_SHA256_AMD64=%s\n",
		sum(codexTar), sum(hostTar), sum(npmTar))
	must(os.WriteFile(filepath.Join(*out, "digests.env"), []byte(env), 0o644))

	cert := mintCerts(*out)
	logf, err := os.OpenFile(filepath.Join(*out, "requests.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	must(err)
	var mu sync.Mutex
	record := func(r *http.Request, status int) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(logf, "mode=%s %s %s%s accept=%q auth=%t -> %d\n", *mode, r.Method, r.Host, r.URL.Path,
			r.Header.Get("Accept"), r.Header.Get("Authorization") != "", status)
	}
	reply := func(w http.ResponseWriter, r *http.Request, status int, ctype string, body []byte) {
		record(r, status)
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		w.Write(body)
	}
	gw := func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, http.StatusGatewayTimeout, "text/html", []byte("<html><body>504 Gateway Time-out</body></html>\n"))
	}
	m := *mode
	asset := func(name string) []byte {
		switch name {
		case "codex-" + target + ".tar.gz":
			return codexTar
		case "codex-code-mode-host-" + target + ".tar.gz":
			return hostTar
		}
		return nil
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		p := r.URL.Path
		if m == "html200-all" {
			reply(w, r, 200, "text/html", []byte(html))
			return
		}
		switch {
		case host == "github.com" && strings.HasPrefix(p, "/openai/codex/releases/download/"+version+"/"):
			switch m {
			case "healthy":
			case "tampered":
				reply(w, r, 200, "application/octet-stream", tampered)
				return
			case "html200":
				reply(w, r, 200, "text/html; charset=utf-8", []byte(html))
				return
			default:
				gw(w, r)
				return
			}
			b := asset(filepath.Base(p))
			if b == nil {
				reply(w, r, 404, "text/plain", []byte("Not Found"))
				return
			}
			reply(w, r, 200, "application/octet-stream", b)
		case host == "api.github.com" && p == "/repos/openai/codex/releases/tags/"+version:
			switch m {
			case "504-all", "tampered-npm":
				gw(w, r)
				return
			case "html200-api":
				reply(w, r, 200, "text/html", []byte(html))
				return
			}
			// Shaped like the real payload: the release's own "url" and
			// the uploader's "url" sit at other depths and must not be
			// picked up; a decoy asset precedes the wanted ones.
			body := `{"url":"https://api.github.com/repos/openai/codex/releases/1","tag_name":"` + version + `","author":{"login":"x","url":"https://api.github.com/users/x"},"assets":[` +
				`{"url":"https://api.github.com/repos/openai/codex/releases/assets/9","name":"codex-` + target + `.zst","uploader":{"url":"https://api.github.com/users/x"}},` +
				`{"url":"https://api.github.com/repos/openai/codex/releases/assets/101","name":"codex-` + target + `.tar.gz","uploader":{"login":"y","url":"https://api.github.com/users/y"},"digest":"sha256:` + sum(codexTar) + `"},` +
				`{"name":"codex-code-mode-host-` + target + `.tar.gz","url":"https://api.github.com/repos/openai/codex/releases/assets/102","digest":"sha256:` + sum(hostTar) + `"}` +
				`]}`
			reply(w, r, 200, "application/json; charset=utf-8", []byte(body))
		case host == "api.github.com" && strings.HasPrefix(p, "/repos/openai/codex/releases/assets/"):
			if r.Header.Get("Accept") != "application/octet-stream" {
				reply(w, r, 200, "application/json", []byte(`{"id":1,"name":"metadata-not-bytes"}`))
				return
			}
			record(r, 302)
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/github-production-release-asset/"+filepath.Base(p), http.StatusFound)
		case host == "release-assets.githubusercontent.com":
			var b []byte
			switch filepath.Base(p) {
			case "101":
				b = codexTar
			case "102":
				b = hostTar
			}
			if m == "tampered-api" {
				b = tampered
			}
			reply(w, r, 200, "application/octet-stream", b)
		case host == "registry.npmjs.org" && p == "/@openai/codex/-/codex-"+npmVer+"-linux-x64.tgz":
			if m == "tampered-npm" {
				reply(w, r, 200, "application/octet-stream", tampered)
				return
			}
			reply(w, r, 200, "application/octet-stream", npmTar)
		default:
			reply(w, r, 404, "text/plain", []byte("Not Found"))
		}
	})

	srv := &http.Server{Addr: *addr, Handler: h, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	log.Printf("codex stub mode=%s on %s; CA at %s", m, *addr, filepath.Join(*out, "ca.pem"))
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func tgz(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)
	// Sorted for a stable digest across runs.
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, n := range names {
		body := files[n]
		must(tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}))
		tw.Write([]byte(body))
	}
	must(tw.Close())
	must(gz.Close())
	return buf.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func mintCerts(out string) tls.Certificate {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rim walkthrough stub CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	must(err)
	caCert, _ := x509.ParseCertificate(caDER)
	must(os.WriteFile(filepath.Join(out, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644))
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "github.com"},
		DNSNames:  []string{"github.com", "api.github.com", "release-assets.githubusercontent.com", "registry.npmjs.org"},
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
