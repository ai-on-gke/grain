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

// Command grain_slowverifier runs core grain (pkg/cli.Main) with a configurable
// slow IdentityVerifier ("slow-proxy") implementing ui.SessionBinder and
// ui.LocalVerifier, an optional local HTTPS identity-aware reverse proxy, and
// a local HTTP control plane for inspecting verification counts and switching
// backend modes during the docs/test-plans/2026-11-16.md §1 walkthrough.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/cli"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const slowProxyHeader = "X-Slow-Proxy-Assertion"

type slowUnavailableErr struct {
	reason     string
	startingUp bool
}

func (e slowUnavailableErr) Error() string {
	if e.startingUp {
		return "slow-proxy verifier is starting up"
	}
	return "slow-proxy remote backend unreachable"
}

func (e slowUnavailableErr) Unavailable() bool         { return true }
func (e slowUnavailableErr) UnavailableReason() string { return e.reason }

type verifierConfig struct {
	Mode            string `json:"mode"`            // "ok", "down", "refuse", "lie-grace"
	DelayMs         int    `json:"delayMs"`         // remote Verify sleep in milliseconds
	TTLMs           int    `json:"ttlMs"`           // SessionTTL in milliseconds (0 disables SessionBinder)
	StartingUp      bool   `json:"startingUp"`      // include "starting up" in unavailable error
	ProxyMode       string `json:"proxyMode"`       // "sign", "none", "forged", "raw"
	ProxyEmail      string `json:"proxyEmail"`      // email signed into X-Slow-Proxy-Assertion
	StripRetryAfter bool   `json:"stripRetryAfter"` // strip Retry-After on proxied responses
}

type verifyEvent struct {
	Seq        int64  `json:"seq"`
	At         string `json:"at"`
	Kind       string `json:"kind"` // "local_ok", "local_reject", "full_admit", "grace_admit", "unavailable_503", "refuse_401", "lie_grace"
	Method     string `json:"method"`
	Path       string `json:"path"`
	RawURI     string `json:"rawUri"`
	Host       string `json:"host"`
	Email      string `json:"email,omitempty"`
	HasCookie  bool   `json:"hasCookie"`
	HasSession bool   `json:"hasSession"`
	Mode       string `json:"mode"`
	DurationMs int64  `json:"durationMs"`
}

type proxyEvent struct {
	Seq          int64    `json:"seq"`
	At           string   `json:"at"`
	Listener     string   `json:"listener"`
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	Host         string   `json:"host"`
	ReqCookies   []string `json:"reqCookies,omitempty"`
	Status       int      `json:"status"`
	SetCookies   []string `json:"setCookies,omitempty"`
	CacheControl string   `json:"cacheControl,omitempty"`
	ETag         string   `json:"etag,omitempty"`
	RetryAfter   string   `json:"retryAfter,omitempty"`
	DurationMs   int64    `json:"durationMs"`
}

type slowVerifier struct {
	secret []byte

	mu           sync.RWMutex
	cfg          verifierConfig
	verifyEvents []verifyEvent
	proxyEvents  []proxyEvent

	seq              atomic.Int64
	proxySeq         atomic.Int64
	fullVerifies     atomic.Int64
	localVerifies    atomic.Int64
	graceAdmits      atomic.Int64
	unavailableCount atomic.Int64
	refuseCount      atomic.Int64
	admitCount       atomic.Int64
}

func newSlowVerifier() *slowVerifier {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return &slowVerifier{
		secret: secret,
		cfg: verifierConfig{
			Mode:       "ok",
			DelayMs:    1500,
			TTLMs:      60000,
			ProxyMode:  "sign",
			ProxyEmail: "alice@example.com",
		},
	}
}

func (v *slowVerifier) getConfig() verifierConfig {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cfg
}

func (v *slowVerifier) recordVerify(ev verifyEvent) {
	ev.Seq = v.seq.Add(1)
	ev.At = time.Now().UTC().Format(time.RFC3339Nano)
	v.mu.Lock()
	if len(v.verifyEvents) < 2000 {
		v.verifyEvents = append(v.verifyEvents, ev)
	}
	v.mu.Unlock()
}

func (v *slowVerifier) recordProxy(ev proxyEvent) {
	ev.Seq = v.proxySeq.Add(1)
	ev.At = time.Now().UTC().Format(time.RFC3339Nano)
	v.mu.Lock()
	if len(v.proxyEvents) < 2000 {
		v.proxyEvents = append(v.proxyEvents, ev)
	}
	v.mu.Unlock()
}

func (v *slowVerifier) mintAssertion(email, host string, ts int64) string {
	email = strings.ToLower(strings.TrimSpace(email))
	host = strings.ToLower(strings.TrimSpace(host))
	payload := fmt.Sprintf("v1|%s|%s|%d", email, host, ts)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

func (v *slowVerifier) parseLocal(r *http.Request, raw string) (email, sigPart, payload string, err error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 5 || parts[0] != "v1" {
		return "", "", "", errors.New("malformed slow-proxy assertion")
	}
	email = strings.ToLower(strings.TrimSpace(parts[1]))
	host := strings.ToLower(strings.TrimSpace(parts[2]))
	ts, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || email == "" || host == "" || parts[4] == "" {
		return "", "", "", errors.New("invalid slow-proxy assertion fields")
	}
	reqHost := strings.ToLower(strings.TrimSpace(r.Host))
	if reqHost == "" || host != reqHost {
		return "", "", "", fmt.Errorf("assertion host %q does not match request host %q", host, reqHost)
	}
	age := time.Since(time.Unix(ts, 0))
	if age < -30*time.Second || age > 5*time.Minute {
		return "", "", "", errors.New("assertion timestamp outside 5m window")
	}
	return email, parts[4], strings.Join(parts[:4], "|"), nil
}

func (v *slowVerifier) Name() string      { return "slow-proxy" }
func (v *slowVerifier) Headers() []string { return []string{slowProxyHeader} }

func (v *slowVerifier) SessionTTL() time.Duration {
	cfg := v.getConfig()
	if cfg.TTLMs <= 0 {
		return 0
	}
	return time.Duration(cfg.TTLMs) * time.Millisecond
}

func (v *slowVerifier) VerifyLocal(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	if r == nil {
		return ui.VerifiedIdentity{}, false, nil
	}
	raw := strings.TrimSpace(r.Header.Get(slowProxyHeader))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	v.localVerifies.Add(1)
	_, hasCookieErr := r.Cookie(ui.VerifierSessionCookieName)
	email, _, _, err := v.parseLocal(r, raw)
	if err != nil {
		v.recordVerify(verifyEvent{
			Kind:      "local_reject",
			Method:    r.Method,
			Path:      r.URL.Path,
			RawURI:    r.RequestURI,
			Host:      r.Host,
			HasCookie: hasCookieErr == nil,
			Mode:      v.getConfig().Mode,
		})
		return ui.VerifiedIdentity{}, false, err
	}
	v.recordVerify(verifyEvent{
		Kind:      "local_ok",
		Method:    r.Method,
		Path:      r.URL.Path,
		RawURI:    r.RequestURI,
		Host:      r.Host,
		Email:     email,
		HasCookie: hasCookieErr == nil,
		Mode:      v.getConfig().Mode,
	})
	return ui.VerifiedIdentity{Email: email, Kind: model.PrincipalHuman}, true, nil
}

func (v *slowVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	if r == nil {
		return ui.VerifiedIdentity{}, false, nil
	}
	raw := strings.TrimSpace(r.Header.Get(slowProxyHeader))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	started := time.Now()
	v.fullVerifies.Add(1)
	cfg := v.getConfig()
	if cfg.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(cfg.DelayMs) * time.Millisecond):
		case <-r.Context().Done():
			return ui.VerifiedIdentity{}, false, r.Context().Err()
		}
	}
	_, cookieErr := r.Cookie(ui.VerifierSessionCookieName)
	hasCookie := cookieErr == nil
	sess, hasSess := ui.VerifierSessionFromRequest(r)

	switch cfg.Mode {
	case "refuse":
		v.refuseCount.Add(1)
		v.recordVerify(verifyEvent{
			Kind:       "refuse_401",
			Method:     r.Method,
			Path:       r.URL.Path,
			RawURI:     r.RequestURI,
			Host:       r.Host,
			HasCookie:  hasCookie,
			HasSession: hasSess,
			Mode:       cfg.Mode,
			DurationMs: time.Since(started).Milliseconds(),
		})
		return ui.VerifiedIdentity{}, false, errors.New("slow-proxy refused assertion")

	case "lie-grace":
		email, _, _, _ := v.parseLocal(r, raw)
		if email == "" {
			email = "alice@example.com"
		}
		v.recordVerify(verifyEvent{
			Kind:       "lie_grace",
			Method:     r.Method,
			Path:       r.URL.Path,
			RawURI:     r.RequestURI,
			Host:       r.Host,
			Email:      email,
			HasCookie:  hasCookie,
			HasSession: hasSess,
			Mode:       cfg.Mode,
			DurationMs: time.Since(started).Milliseconds(),
		})
		return ui.VerifiedIdentity{Email: email, Kind: model.PrincipalHuman, SessionGrace: true}, true, nil

	case "down":
		email, _, _, localErr := v.parseLocal(r, raw)
		if hasSess && localErr == nil && strings.EqualFold(sess.Identity.Email, email) {
			v.graceAdmits.Add(1)
			v.recordVerify(verifyEvent{
				Kind:       "grace_admit",
				Method:     r.Method,
				Path:       r.URL.Path,
				RawURI:     r.RequestURI,
				Host:       r.Host,
				Email:      email,
				HasCookie:  hasCookie,
				HasSession: true,
				Mode:       cfg.Mode,
				DurationMs: time.Since(started).Milliseconds(),
			})
			return ui.VerifiedIdentity{Email: email, Kind: model.PrincipalHuman, SessionGrace: true}, true, nil
		}
		v.unavailableCount.Add(1)
		v.recordVerify(verifyEvent{
			Kind:       "unavailable_503",
			Method:     r.Method,
			Path:       r.URL.Path,
			RawURI:     r.RequestURI,
			Host:       r.Host,
			Email:      email,
			HasCookie:  hasCookie,
			HasSession: hasSess,
			Mode:       cfg.Mode,
			DurationMs: time.Since(started).Milliseconds(),
		})
		return ui.VerifiedIdentity{}, false, slowUnavailableErr{reason: "slow_backend_unreachable", startingUp: cfg.StartingUp}
	}

	email, sigHex, payload, err := v.parseLocal(r, raw)
	if err != nil {
		v.refuseCount.Add(1)
		v.recordVerify(verifyEvent{
			Kind:       "refuse_401",
			Method:     r.Method,
			Path:       r.URL.Path,
			RawURI:     r.RequestURI,
			Host:       r.Host,
			HasCookie:  hasCookie,
			HasSession: hasSess,
			Mode:       cfg.Mode,
			DurationMs: time.Since(started).Milliseconds(),
		})
		return ui.VerifiedIdentity{}, false, err
	}
	wantMAC := hmac.New(sha256.New, v.secret)
	wantMAC.Write([]byte(payload))
	gotSig, decErr := hex.DecodeString(sigHex)
	if decErr != nil || !hmac.Equal(gotSig, wantMAC.Sum(nil)) {
		v.refuseCount.Add(1)
		v.recordVerify(verifyEvent{
			Kind:       "refuse_401",
			Method:     r.Method,
			Path:       r.URL.Path,
			RawURI:     r.RequestURI,
			Host:       r.Host,
			Email:      email,
			HasCookie:  hasCookie,
			HasSession: hasSess,
			Mode:       cfg.Mode,
			DurationMs: time.Since(started).Milliseconds(),
		})
		return ui.VerifiedIdentity{}, false, errors.New("slow-proxy HMAC verification failed")
	}
	v.admitCount.Add(1)
	v.recordVerify(verifyEvent{
		Kind:       "full_admit",
		Method:     r.Method,
		Path:       r.URL.Path,
		RawURI:     r.RequestURI,
		Host:       r.Host,
		Email:      email,
		HasCookie:  hasCookie,
		HasSession: hasSess,
		Mode:       cfg.Mode,
		DurationMs: time.Since(started).Milliseconds(),
	})
	return ui.VerifiedIdentity{Email: email, Kind: model.PrincipalHuman}, true, nil
}

func startControlServer(addr string, v *slowVerifier) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		v.mu.RLock()
		cfg := v.cfg
		ve := append([]verifyEvent(nil), v.verifyEvents...)
		pe := append([]proxyEvent(nil), v.proxyEvents...)
		v.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":            cfg,
			"fullVerifies":      v.fullVerifies.Load(),
			"localVerifies":     v.localVerifies.Load(),
			"graceAdmits":       v.graceAdmits.Load(),
			"unavailableErrors": v.unavailableCount.Load(),
			"refuses":           v.refuseCount.Load(),
			"admits":            v.admitCount.Load(),
			"verifyEvents":      ve,
			"proxyEvents":       pe,
		})
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		v.fullVerifies.Store(0)
		v.localVerifies.Store(0)
		v.graceAdmits.Store(0)
		v.unavailableCount.Store(0)
		v.refuseCount.Store(0)
		v.admitCount.Store(0)
		v.mu.Lock()
		v.verifyEvents = nil
		v.proxyEvents = nil
		v.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reset":true}` + "\n"))
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var patch struct {
			Mode            *string `json:"mode"`
			DelayMs         *int    `json:"delayMs"`
			TTLMs           *int    `json:"ttlMs"`
			StartingUp      *bool   `json:"startingUp"`
			ProxyMode       *string `json:"proxyMode"`
			ProxyEmail      *string `json:"proxyEmail"`
			StripRetryAfter *bool   `json:"stripRetryAfter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		v.mu.Lock()
		if patch.Mode != nil {
			v.cfg.Mode = *patch.Mode
		}
		if patch.DelayMs != nil {
			v.cfg.DelayMs = *patch.DelayMs
		}
		if patch.TTLMs != nil {
			v.cfg.TTLMs = *patch.TTLMs
		}
		if patch.StartingUp != nil {
			v.cfg.StartingUp = *patch.StartingUp
		}
		if patch.ProxyMode != nil {
			v.cfg.ProxyMode = *patch.ProxyMode
		}
		if patch.ProxyEmail != nil {
			v.cfg.ProxyEmail = *patch.ProxyEmail
		}
		if patch.StripRetryAfter != nil {
			v.cfg.StripRetryAfter = *patch.StripRetryAfter
		}
		cur := v.cfg
		v.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cur)
	})
	mux.HandleFunc("/mint", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		if email == "" {
			email = "alice@example.com"
		}
		host := r.URL.Query().Get("host")
		if host == "" {
			host = "127.0.0.1:4291"
		}
		tok := v.mintAssertion(email, host, time.Now().Unix())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"header": slowProxyHeader, "token": tok})
	})
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("slowverifier control server %s: %v", addr, err)
		}
	}()
}

func makeSelfSignedTLS() tls.Certificate {
	certFile := os.Getenv("GRAIN_SLOW_TLS_CERT")
	keyFile := os.Getenv("GRAIN_SLOW_TLS_KEY")
	if certFile != "" && keyFile != "" {
		if pair, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
			return pair
		}
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("generate tls key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Grain Walkthrough Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		log.Fatalf("create tls cert: %v", err)
	}
	if certFile != "" && keyFile != "" {
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			log.Fatalf("marshal tls key: %v", err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		_ = os.WriteFile(certFile, certPEM, 0o644)
		_ = os.WriteFile(keyFile, keyPEM, 0o600)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

func startHTTPSProxy(listenAddr, upstreamURL string, cert tls.Certificate, v *slowVerifier) {
	target, err := url.Parse(upstreamURL)
	if err != nil {
		log.Fatalf("parse upstream %q: %v", upstreamURL, err)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Forwarded-For", "198.51.100.42")
			cfg := v.getConfig()
			switch cfg.ProxyMode {
			case "none":
				pr.Out.Header.Del(slowProxyHeader)
			case "forged":
				pr.Out.Header.Set(slowProxyHeader, fmt.Sprintf("v1|%s|%s|%d|deadbeef00", cfg.ProxyEmail, strings.ToLower(pr.In.Host), time.Now().Unix()))
			case "raw":
				// Leave caller's header untouched.
			default:
				pr.Out.Header.Set(slowProxyHeader, v.mintAssertion(cfg.ProxyEmail, pr.In.Host, time.Now().Unix()))
			}
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if v.getConfig().StripRetryAfter {
				resp.Header.Del("Retry-After")
			}
			var reqCookies []string
			if resp.Request != nil {
				for _, c := range resp.Request.Cookies() {
					reqCookies = append(reqCookies, c.Name)
				}
			}
			var setCookies []string
			for _, line := range resp.Header.Values("Set-Cookie") {
				if semi := strings.IndexByte(line, ';'); semi > 0 {
					if eq := strings.IndexByte(line[:semi], '='); eq > 0 {
						setCookies = append(setCookies, line[:eq]+" "+line[semi:])
						continue
					}
				}
				setCookies = append(setCookies, line)
			}
			reqHost, reqMethod, reqPath := "", "", ""
			if resp.Request != nil {
				reqHost = resp.Request.Host
				reqMethod = resp.Request.Method
				reqPath = resp.Request.URL.RequestURI()
			}
			v.recordProxy(proxyEvent{
				Listener:     listenAddr,
				Method:       reqMethod,
				Path:         reqPath,
				Host:         reqHost,
				ReqCookies:   reqCookies,
				Status:       resp.StatusCode,
				SetCookies:   setCookies,
				CacheControl: resp.Header.Get("Cache-Control"),
				ETag:         resp.Header.Get("ETag"),
				RetryAfter:   resp.Header.Get("Retry-After"),
			})
			return nil
		},
	}

	srv := &http.Server{
		Addr:      listenAddr,
		Handler:   rp,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("https proxy listen %s: %v", listenAddr, err)
	}
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("https proxy serve %s: %v", listenAddr, err)
		}
	}()
}

func main() {
	v := newSlowVerifier()
	if delayStr := os.Getenv("GRAIN_SLOW_DELAY_MS"); delayStr != "" {
		if ms, err := strconv.Atoi(delayStr); err == nil {
			v.cfg.DelayMs = ms
		}
	}
	if ttlStr := os.Getenv("GRAIN_SLOW_TTL_MS"); ttlStr != "" {
		if ms, err := strconv.Atoi(ttlStr); err == nil {
			v.cfg.TTLMs = ms
		}
	}
	if mode := os.Getenv("GRAIN_SLOW_MODE"); mode != "" {
		v.cfg.Mode = mode
	}
	ui.RegisterIdentityVerifier(v)

	if ctlAddr := os.Getenv("GRAIN_SLOW_CONTROL_ADDR"); ctlAddr != "" {
		startControlServer(ctlAddr, v)
	}
	upstream := os.Getenv("GRAIN_SLOW_UPSTREAM")
	if upstream == "" {
		upstream = "http://127.0.0.1:4291"
	}
	if proxyAddrs := os.Getenv("GRAIN_SLOW_PROXY_ADDRS"); proxyAddrs != "" {
		cert := makeSelfSignedTLS()
		for _, addr := range strings.Split(proxyAddrs, ",") {
			addr = strings.TrimSpace(addr)
			if addr != "" {
				startHTTPSProxy(addr, upstream, cert, v)
			}
		}
	}

	cli.Main()
}
