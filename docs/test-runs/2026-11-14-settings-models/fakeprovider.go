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

// fakeprovider is the one stubbed external service of the
// docs/test-plans/2026-11-14.md §4 walkthrough: an Anthropic-compatible
// GET /v1/models served over TLS as api.anthropic.com (the run maps
// that name to 127.0.0.1 in /etc/hosts and points only the daemon's
// SSL_CERT_FILE at the CA written here). A plain-HTTP control port sets
// what the next listing answers:
//
//	PUT  /models  body: one model id per line
//	PUT  /mode    body: ok | 500 | hang | slow (answers, after 15s, the
//	              list served when the request arrived: a stale answer)
//	GET  /hits    how many listings were asked for, one line per request
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu     sync.Mutex
	models = []string{"claude-walk-a"}
	mode   = "ok"
	hits   []string
)

func main() {
	tlsAddr := flag.String("tls", "127.0.0.1:443", "TLS listen address for the fake provider")
	ctl := flag.String("control", "127.0.0.1:4291", "plain-HTTP control address")
	caOut := flag.String("ca", "fake-ca.pem", "where to write the CA certificate")
	flag.Parse()

	// The leaf and its CA are kept beside -ca and reused, so restarting
	// this server does not invalidate the roots a running daemon loaded.
	cert, err := tls.LoadX509KeyPair(*caOut+".leaf", *caOut+".key")
	if err != nil {
		var caPEM, leafPEM, keyPEM []byte
		cert, caPEM, leafPEM, keyPEM = issue()
		for path, b := range map[string][]byte{*caOut: caPEM, *caOut + ".leaf": leafPEM, *caOut + ".key": keyPEM} {
			if err := os.WriteFile(path, b, 0o644); err != nil {
				log.Fatal(err)
			}
		}
	}

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		m, ids := mode, append([]string(nil), models...)
		hits = append(hits, fmt.Sprintf("%s %s auth=%t", time.Now().UTC().Format(time.RFC3339Nano), m,
			r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != ""))
		mu.Unlock()
		switch m {
		case "500":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"fake provider is down"}}`)
			return
		case "hang":
			<-r.Context().Done()
			return
		case "slow":
			select {
			case <-time.After(15 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		type entry struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
		}
		out := struct {
			Data    []entry `json:"data"`
			HasMore bool    `json:"has_more"`
		}{}
		for _, id := range ids {
			out.Data = append(out.Data, entry{ID: id, DisplayName: strings.ToUpper(id), Type: "model"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{Addr: *tlsAddr, Handler: api, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() { log.Fatal(srv.ListenAndServeTLS("", "")) }()

	c := http.NewServeMux()
	c.HandleFunc("PUT /models", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var ids []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ids = append(ids, l)
			}
		}
		mu.Lock()
		models = ids
		mu.Unlock()
	})
	c.HandleFunc("PUT /mode", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		mode = strings.TrimSpace(string(b))
		mu.Unlock()
	})
	c.HandleFunc("GET /hits", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, strings.Join(hits, "\n")+"\n")
	})
	log.Fatal(http.ListenAndServe(*ctl, c))
}

// issue makes a throwaway CA and an api.anthropic.com leaf it signs.
func issue() (cert tls.Certificate, caPEM, leafPEM, keyPEM []byte) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "walkthrough fake provider CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "api.anthropic.com"},
		DNSNames: []string{"api.anthropic.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
