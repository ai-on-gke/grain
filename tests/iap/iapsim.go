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

// iapsim stands in for Google Cloud IAP in tests/iap/walkthrough.sh, so a
// real `grain demo -ui-iap-*` can be put behind something that behaves the
// way IAP does. One process, two listeners:
//
//	go run iapsim.go -issuer 127.0.0.1:18080 -proxy 127.0.0.1:18443 -upstream http://127.0.0.1:4300 \
//	    -aud /projects/123/global/backendServices/456 -iss https://iap.test.local \
//	    -group ops@example.com=carol@example.com
//
// The issuer listener serves:
//
//	GET /jwks       the published signing key (kid k1) as a JWK set, as IAP's jwk endpoint does
//	GET /sign       a fresh ES256 assertion: ?email=&aud=&iss=&exp=1h&iat=0&kid=k1&key=k1&badsig=1;
//	                key=k2 signs with a key that is never published
//	POST /token     an OAuth token endpoint for a service-account ADC file whose token_uri points here
//	GET /v1/groups:lookup and /v1/groups/G/memberships:checkTransitiveMembership
//	                a fake Cloud Identity groups API answering from -group
//	GET /stats      how many times each of those was called; POST /stats/reset zeroes them
//
// The proxy listener is IAP's data path: it drops any X-Goog-IAP-JWT-Assertion
// and X-Goog-Authenticated-User-* the client sent, signs an assertion for the
// signed-in person, adds it with the plaintext X-Goog-Authenticated-User-Email
// IAP also sends, sets X-Forwarded-For/-Proto, and forwards with the client's
// Host. "Signed in" is the iapsim_email cookie, set by /_iapsim/login (a stand-in
// for Google's sign-in page, and forwarded like IAP's own GCP_IAP_UID cookie),
// or an X-Iapsim-Email header for curl, which is stripped. A CLI signs in
// with a bearer "ID token" iapsim-idtoken.<email> (see the proxy handler).
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type groupsFlag map[string]map[string]bool

func (g groupsFlag) String() string { return "" }
func (g groupsFlag) Set(v string) error {
	name, members, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want group=member,member")
	}
	set := map[string]bool{}
	for _, m := range strings.Split(members, ",") {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			set[m] = true
		}
	}
	g[strings.ToLower(name)] = set
	return nil
}

var (
	keys    = map[string]*ecdsa.PrivateKey{}
	statsMu sync.Mutex
	stats   = map[string]int{}
)

func count(name string) {
	statsMu.Lock()
	stats[name]++
	statsMu.Unlock()
}

func main() {
	issuerAddr := flag.String("issuer", "127.0.0.1:18080", "issuer listener (JWKS, signer, token, Cloud Identity)")
	proxyAddr := flag.String("proxy", "127.0.0.1:18443", "proxy listener (IAP's data path)")
	upstream := flag.String("upstream", "http://127.0.0.1:4300", "the grain server behind the proxy")
	aud := flag.String("aud", "/projects/123/global/backendServices/456", "audience the proxy signs for")
	iss := flag.String("iss", "https://iap.test.local", "issuer the proxy signs as")
	defaultEmail := flag.String("email", "", "who the proxy signs in when the browser has not signed in (empty: send it to /_iapsim/login)")
	groups := groupsFlag{}
	flag.Var(groups, "group", "group=member,member for the fake Cloud Identity (repeatable)")
	flag.Parse()

	for _, k := range []string{"k1", "k2"} {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		keys[k] = priv
	}

	go serveIssuer(*issuerAddr, groups)
	serveProxy(*proxyAddr, *upstream, *aud, *iss, *defaultEmail)
}

// sign mints an assertion with the claims IAP sets: iss, aud, exp, iat,
// email, sub and (for a Workspace account) hd.
func sign(email, aud, iss, kid, key string, exp, iat time.Duration, badsig bool) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": iss, "aud": aud,
		"exp": now.Add(exp).Unix(), "iat": now.Add(iat).Unix(),
		"email": email, "sub": "accounts.google.com:" + strconv.Itoa(len(email)*7919),
	}
	if _, domain, ok := strings.Cut(email, "@"); ok && !strings.HasSuffix(domain, "gserviceaccount.com") && domain != "gmail.com" {
		claims["hd"] = domain
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	t.Header["kid"] = kid
	k, ok := keys[key]
	if !ok {
		return "", fmt.Errorf("unknown key %q", key)
	}
	s, err := t.SignedString(k)
	if err != nil {
		return "", err
	}
	if badsig {
		i := strings.LastIndexByte(s, '.') + 5
		c := byte('A')
		if s[i] == 'A' {
			c = 'B'
		}
		s = s[:i] + string(c) + s[i+1:]
	}
	return s, nil
}

func serveIssuer(addr string, groups groupsFlag) {
	mux := http.NewServeMux()
	pub := keys["k1"].PublicKey
	b64 := base64.RawURLEncoding.EncodeToString
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "k1",
		"x": b64(pub.X.FillBytes(make([]byte, 32))), "y": b64(pub.Y.FillBytes(make([]byte, 32))),
	}}})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		count("jwks")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	})
	mux.HandleFunc("GET /sign", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		get := func(k, def string) string {
			if v := q.Get(k); v != "" {
				return v
			}
			return def
		}
		exp, err1 := time.ParseDuration(get("exp", "1h"))
		iat, err2 := time.ParseDuration(get("iat", "0s"))
		if err1 != nil || err2 != nil {
			http.Error(w, "bad exp/iat", 400)
			return
		}
		kid := get("kid", "k1")
		s, err := sign(q.Get("email"), q.Get("aud"), q.Get("iss"), kid, get("key", kid), exp, iat, q.Get("badsig") != "")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fmt.Fprint(w, s)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		count("token")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"iapsim-access-token","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("GET /v1/groups:lookup", func(w http.ResponseWriter, r *http.Request) {
		count("groups:lookup")
		g := strings.ToLower(r.URL.Query().Get("groupKey.id"))
		if _, ok := groups[g]; !ok || r.Header.Get("Authorization") != "Bearer iapsim-access-token" {
			http.Error(w, `{"error":{"code":404}}`, 404)
			return
		}
		fmt.Fprintf(w, `{"name":"groups/%s"}`, url.PathEscape(g))
	})
	mux.HandleFunc("GET /v1/groups/{group}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("rest") != "memberships:checkTransitiveMembership" {
			http.NotFound(w, r)
			return
		}
		count("checkTransitiveMembership")
		members, ok := groups[strings.ToLower(r.PathValue("group"))]
		q := r.URL.Query().Get("query") // member_key_id == 'x@y'
		m := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(q, "member_key_id == '"), "'"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"hasMembership":%t}`, members[m])
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		statsMu.Lock()
		defer statsMu.Unlock()
		_ = json.NewEncoder(w).Encode(stats)
	})
	mux.HandleFunc("POST /stats/reset", func(w http.ResponseWriter, r *http.Request) {
		statsMu.Lock()
		stats = map[string]int{}
		statsMu.Unlock()
	})
	log.Printf("iapsim issuer on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

const loginPage = `<!doctype html><html><head><title>Sign in - Google Accounts (simulated)</title></head>
<body style="font-family:sans-serif;max-width:28rem;margin:4rem auto">
<h1>Sign in (simulated IAP)</h1>
<p>This stands in for Google's sign-in page in front of an IAP-protected app.</p>
<form method="get" action="/_iapsim/login">
<label>Email <input name="email" type="email" required></label>
<input type="hidden" name="next" value="%s">
<button type="submit">Continue</button>
</form></body></html>`

func serveProxy(addr, upstream, aud, iss, defaultEmail string) {
	up, err := url.Parse(upstream)
	if err != nil {
		log.Fatal(err)
	}
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(up)
		pr.Out.Host = pr.In.Host // IAP forwards the Host the browser asked for
		pr.SetXForwarded()
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/_iapsim/login", func(w http.ResponseWriter, r *http.Request) {
		next := r.URL.Query().Get("next")
		if next == "" || !strings.HasPrefix(next, "/") {
			next = "/"
		}
		email := strings.TrimSpace(r.URL.Query().Get("email"))
		if email == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, loginPage, html.EscapeString(next))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "iapsim_email", Value: url.QueryEscape(email), Path: "/", HttpOnly: true})
		http.Redirect(w, r, next, http.StatusFound)
	})
	// IAP's own sign-out endpoint, which grain's Log out sends the browser to.
	mux.HandleFunc("/_gcp_iap/clear_login_cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "iapsim_email", Value: "", Path: "/", MaxAge: -1})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Signed out</title><h1>Signed out of IAP (simulated)</h1>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		email := r.Header.Get("X-Iapsim-Email")
		// Programmatic access: IAP takes a Google-signed ID token in
		// Proxy-Authorization (consumed) or Authorization (left for the
		// app), as `grain -token`/$GRAIN_ID_TOKEN sends one. The stand-in
		// for such a token is "iapsim-idtoken.<email>".
		if email == "" {
			for _, h := range []string{"Proxy-Authorization", "Authorization"} {
				if v, ok := strings.CutPrefix(r.Header.Get(h), "Bearer iapsim-idtoken."); ok {
					email = v
					if h == "Proxy-Authorization" {
						r.Header.Del(h)
					}
					break
				}
			}
		}
		if email == "" {
			if c, err := r.Cookie("iapsim_email"); err == nil {
				email, _ = url.QueryUnescape(c.Value)
			}
		}
		if email == "" {
			email = defaultEmail
		}
		if email == "" {
			if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
				http.Redirect(w, r, "/_iapsim/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Error(w, "IAP: sign-in required", http.StatusUnauthorized)
			return
		}
		tok, err := sign(email, aud, iss, "k1", "k1", 10*time.Minute, 0, false)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-goog-") || strings.EqualFold(name, "X-Iapsim-Email") {
				r.Header.Del(name)
			}
		}
		r.Header.Set("X-Goog-IAP-JWT-Assertion", tok)
		r.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:"+email)
		count("proxied")
		rp.ServeHTTP(w, r)
	})
	log.Printf("iapsim proxy on http://%s -> %s", addr, upstream)
	log.Fatal(http.ListenAndServe(addr, mux))
}
