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

package ui_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestHostEnrollmentOwnerOnlyAndGroupRestrictions(t *testing.T) {
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:     "proxy",
		headers:  []string{"X-Test-Proxy-User"},
		verifyFn: parseSimpleSignedHeader,
	}))
	t.Cleanup(ui.RegisterGroupMemberChecker(groupCheckerFunc(func(_ context.Context, groupEmail, memberEmail string) (bool, error) {
		return groupEmail == "eng-hosts@example.com" && memberEmail == "rarcea@example.com", nil
	})))

	srv, client := testServer(t)
	ctx := context.Background()
	client.Now = nil
	client.Config.Actor = ui.DefaultActor("owner@example.com")
	client.Config.LocalAPIToken = testAccessCode

	// Seed owner@example.com and colleague@example.com (both at grain/operator)
	// plus viewer@example.com and a service account.
	for _, p := range []ui.AllowPrincipalRequest{
		{Entry: "user:owner@example.com", Role: "grain/operator", Reason: "deployment owner"},
		{Entry: "user:colleague@example.com", Role: "grain/operator", Reason: "fellow operator"},
		{Entry: "user:viewer@example.com", Role: "grain/viewer"},
		{Entry: "serviceAccount:ci@my-proj.iam.gserviceaccount.com", Role: "grain/operator", Reason: "ci bot"},
	} {
		if _, err := client.AllowPrincipal(ctx, p); err != nil {
			t.Fatalf("AllowPrincipal(%s): %v", p.Entry, err)
		}
	}

	callAs := func(who, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return serveFrom(srv, "203.0.113.50:443", method, path, body, map[string]string{
			"Content-Type":      "application/json",
			"X-Test-Proxy-User": "ok:" + who,
		})
	}

	// Non-owner principals (even colleague@example.com at grain/operator, viewer, and service account)
	// are refused 403 on enrollment codes and pending approvals.
	for _, denied := range []string{
		"colleague@example.com",
		"viewer@example.com",
		"serviceAccount:ci@my-proj.iam.gserviceaccount.com",
	} {
		for _, path := range []string{"/api/hostds/enrollment-codes", "/api/hostds/pending"} {
			if rec := callAs(denied, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
				t.Errorf("%s GET %s = %d %s, want 403", denied, path, rec.Code, rec.Body.String())
			}
		}
		if rec := callAs(denied, http.MethodPost, "/api/hostds/enrollment-codes", `{"name":"unauthorized"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s POST /api/hostds/enrollment-codes = %d %s, want 403", denied, rec.Code, rec.Body.String())
		}
	}

	// Deployment owner lists codes and gets the default "Invite hosts" snippet.
	rec := callAs("owner@example.com", http.MethodGet, "/api/hostds/enrollment-codes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET /api/hostds/enrollment-codes = %d %s", rec.Code, rec.Body.String())
	}
	var codes []ui.HostEnrollmentCodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &codes); err != nil {
		t.Fatal(err)
	}
	if len(codes) != 1 || codes[0].Name != "default" || !strings.HasPrefix(codes[0].Code, model.HostEnrollmentCodePrefix) {
		t.Fatalf("default enrollment codes = %+v", codes)
	}
	if codes[0].Install == nil || len(codes[0].Install.Lines) != 1 ||
		!strings.Contains(codes[0].Install.Lines[0], "/hostd/install | sh -s -- --join "+codes[0].Code) {
		t.Fatalf("default invite snippet = %+v", codes[0].Install)
	}

	// Create a restricted enrollment code allowing group:eng-hosts@example.com with max_uses=1.
	rec = callAs("owner@example.com", http.MethodPost, "/api/hostds/enrollment-codes",
		`{"name":"eng-team","max_uses":1,"default_capacity":4,"default_labels":{"pool":"eng"},"allowed_principals":["group:eng-hosts@example.com"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner POST /api/hostds/enrollment-codes = %d %s", rec.Code, rec.Body.String())
	}
	var engCode ui.HostEnrollmentCodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &engCode); err != nil {
		t.Fatal(err)
	}

	// Enrollment code is NOT a credential for anything else (cannot list tasks or connect as hostd).
	if r := serveFrom(srv, "203.0.113.55:443", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + engCode.Code,
	}); r.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/tasks with enrollment code = %d, want 401", r.Code)
	}

	// outsider@example.com is not in group:eng-hosts@example.com -> 403.
	_, privOut, _ := ed25519.GenerateKey(rand.Reader)
	subOut := hostd.JoinSubmitRequest{
		EnrollmentCode: engCode.Code,
		Hostname:       "outsider-box",
		OS:             "linux",
		Arch:           "amd64",
	}
	if err := hostd.SignJoinSubmit(&subOut, privOut, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rawOut, _ := json.Marshal(subOut)
	if r := serveFrom(srv, "203.0.113.60:443", http.MethodPost, "/api/hostd/join", string(rawOut), map[string]string{
		"Content-Type":      "application/json",
		"X-Test-Proxy-User": "ok:outsider@example.com",
	}); r.Code != http.StatusForbidden {
		t.Fatalf("outsider join = %d %s, want 403", r.Code, r.Body.String())
	}

	// rarcea@example.com IS in group:eng-hosts@example.com (even though not on Settings -> Access!) -> 201 Created.
	_, privMember, _ := ed25519.GenerateKey(rand.Reader)
	subMember := hostd.JoinSubmitRequest{
		EnrollmentCode: engCode.Code,
		Hostname:       "rarcea-workstation",
		OS:             "linux",
		Arch:           "amd64",
		CLIs:           []string{"claude", "codex"},
		Version:        "v1.2.3",
		LocalUser:      "rarcea",
	}
	if err := hostd.SignJoinSubmit(&subMember, privMember, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rawMember, _ := json.Marshal(subMember)
	joinRec := serveFrom(srv, "203.0.113.61:443", http.MethodPost, "/api/hostd/join", string(rawMember), map[string]string{
		"Content-Type":      "application/json",
		"X-Test-Proxy-User": "ok:rarcea@example.com",
	})
	if joinRec.Code != http.StatusCreated {
		t.Fatalf("group member join = %d %s, want 201", joinRec.Code, joinRec.Body.String())
	}
	var joinResp hostd.JoinSubmitResponse
	if err := json.Unmarshal(joinRec.Body.Bytes(), &joinResp); err != nil {
		t.Fatal(err)
	}

	// max_uses=1 is now exhausted -> a second attempt gets 403.
	if err := hostd.SignJoinSubmit(&subMember, privMember, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rawMember2, _ := json.Marshal(subMember)
	if r := serveFrom(srv, "203.0.113.61:443", http.MethodPost, "/api/hostd/join", string(rawMember2), map[string]string{
		"Content-Type":      "application/json",
		"X-Test-Proxy-User": "ok:rarcea@example.com",
	}); r.Code != http.StatusForbidden {
		t.Fatalf("exhausted code join = %d %s, want 403", r.Code, r.Body.String())
	}

	// Pending list shows exact matching SAS pairing code; colleague@example.com cannot accept or reject.
	pendRec := callAs("owner@example.com", http.MethodGet, "/api/hostds/pending", "")
	if pendRec.Code != http.StatusOK {
		t.Fatalf("owner GET /api/hostds/pending = %d %s", pendRec.Code, pendRec.Body.String())
	}
	var pending []ui.PendingHostJoinView
	if err := json.Unmarshal(pendRec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].PairingCode != joinResp.PairingCode || pending[0].EnrolledBy != "rarcea@example.com" {
		t.Fatalf("pending = %+v, joinResp = %+v", pending, joinResp)
	}
	if r := callAs("colleague@example.com", http.MethodPost, "/api/hostds/pending/"+joinResp.ID+"/accept", `{}`); r.Code != http.StatusForbidden {
		t.Fatalf("colleague accept = %d %s, want 403", r.Code, r.Body.String())
	}
	if r := callAs("colleague@example.com", http.MethodPost, "/api/hostds/pending/"+joinResp.ID+"/reject", `{}`); r.Code != http.StatusForbidden {
		t.Fatalf("colleague reject = %d %s, want 403", r.Code, r.Body.String())
	}

	// Owner rejects -> leaves nothing behind in pending or hostds.
	if r := callAs("owner@example.com", http.MethodPost, "/api/hostds/pending/"+joinResp.ID+"/reject", `{}`); r.Code != http.StatusOK {
		t.Fatalf("owner reject = %d %s", r.Code, r.Body.String())
	}
	pendAfter, err := client.ListPendingHostJoins(ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}))
	if err != nil || len(pendAfter) != 0 {
		t.Fatalf("pending after reject = %+v, %v", pendAfter, err)
	}
	hostsAfter, err := client.ListHostds(ctx)
	if err != nil || len(hostsAfter) != 0 {
		t.Fatalf("hosts after reject = %+v, %v", hostsAfter, err)
	}
}

func TestHostEnrollHandshakeKeyBoundConnectRenewalAndOSSDeviceFlow(t *testing.T) {
	// Start a mock GitHub server handling Device Flow + /api/v3/user + /api/v3/user/orgs.
	ghMux := http.NewServeMux()
	ghMux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dev-123","user_code":"ABCD-1234","verification_uri":"https://github.example/login/device","expires_in":600,"interval":1}`))
	})
	ghMux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_valid_octocat_token","token_type":"bearer"}`))
	})
	ghMux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_valid_octocat_token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"OctoCat"}`))
	})
	ghMux.HandleFunc("/api/v3/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"login":"Acme-Org"}]`))
	})
	ghSrv := httptest.NewServer(ghMux)
	t.Cleanup(ghSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, store, _ := testClient(t)
	client.Now = nil
	client.Config.LocalAPIToken = testAccessCode

	// Wire a real hostd.Remote hub backed by store so we test full key-bound WebSocket connect + disconnect + renew.
	remote := &hostd.Remote{
		ResolveCredential: func(rctx context.Context, token string) (hostd.ResolvedCredential, bool, error) {
			h, err := store.HostdForTokenAt(rctx, token, time.Now().UTC())
			if err != nil || h == nil {
				return hostd.ResolvedCredential{}, false, err
			}
			return hostd.ResolvedCredential{
				Name:      h.Name,
				Owner:     h.Owner,
				PublicKey: h.PublicKey,
			}, true, nil
		},
		Renew: func(rctx context.Context, name, curTok string) (string, time.Time, error) {
			return store.RenewHostdCredential(rctx, name, curTok, time.Now().UTC())
		},
	}
	client.Config.HostdRemote = remote

	// Configure GitHubHost to point at our mock GitHub server over HTTP.
	storeSettingsRow(t, client)
	mc, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mc == nil {
		mc = &model.Config{}
	}
	mc.GitHubHost = strings.TrimPrefix(ghSrv.URL, "http://")
	mc.GitHubInsecureHTTP = true
	if err := store.PutConfig(ctx, *mc); err != nil {
		t.Fatal(err)
	}

	srv := ui.NewServerWithClient(client)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	// Short /hostd/install URL serves the bootstrap script openly with no secrets.
	instResp, err := http.Get(ts.URL + "/hostd/install")
	if err != nil {
		t.Fatal(err)
	}
	instResp.Body.Close()
	if instResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /hostd/install = %d, want 200", instResp.StatusCode)
	}

	// Owner creates an enrollment code restricted to org:acme-org via HTTPClient.
	ownerCLI := ui.NewHTTPClient(ts.URL)
	ownerCLI.Token = testAccessCode
	codeView, err := ownerCLI.CreateHostEnrollmentCode(ctx, ui.CreateHostEnrollmentCodeRequest{
		Name:              "oss-contributors",
		ExpiresIn:         "7d",
		DefaultLabels:     map[string]string{"gpu": "l4"},
		DefaultCapacity:   3,
		AllowedPrincipals: []string{"org:acme-org"},
	})
	if err != nil {
		t.Fatalf("CreateHostEnrollmentCode: %v", err)
	}

	// Run hostd.Enroll in a goroutine (simulating the remote host terminal).
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "hostd-key")
	tokPath := filepath.Join(tmpDir, "hostd-token")
	var termOut bytes.Buffer
	enrollErrCh := make(chan error, 1)
	var enrollRes *hostd.EnrollResult
	go func() {
		var err error
		enrollRes, err = hostd.Enroll(ctx, hostd.EnrollOptions{
			ServerURL:        ts.URL,
			JoinCode:         codeView.Code,
			KeyPath:          keyPath,
			TokenPath:        tokPath,
			Hostname:         "Dev-Workstation.local",
			OS:               "linux",
			Arch:             "amd64",
			CLIs:             []string{"claude"},
			Version:          "v2.0.0",
			LocalUser:        "octocat",
			PollInterval:     40 * time.Millisecond,
			DeviceFlowClient: ghSrv.Client(),
			Out:              &termOut,
		})
		enrollErrCh <- err
	}()

	// Wait until the pending join request appears in Settings -> Hosts, and Accept.
	var pending []ui.PendingHostJoinView
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err = ownerCLI.ListPendingHostJoins(ctx)
		if err == nil && len(pending) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending join requests = %+v (err=%v, term=%s)", pending, err, termOut.String())
	}
	if pending[0].EnrolledBy != "github:octocat" || pending[0].SuggestedName != "dev-workstation" || !strings.HasPrefix(pending[0].KeyFingerprint, "SHA256:") {
		t.Fatalf("pending[0] = %+v", pending[0])
	}

	acceptedHost, err := ownerCLI.AcceptPendingHostJoin(ctx, pending[0].ID, ui.AcceptHostJoinRequest{})
	if err != nil {
		t.Fatalf("AcceptPendingHostJoin: %v", err)
	}
	if acceptedHost.Name != "dev-workstation" || acceptedHost.Owner != "github:octocat" || acceptedHost.Capacity != 3 || !strings.HasPrefix(acceptedHost.KeyFingerprint, "SHA256:") {
		t.Fatalf("acceptedHost = %+v", acceptedHost)
	}

	if err := <-enrollErrCh; err != nil {
		t.Fatalf("hostd.Enroll failed: %v\nterm:\n%s", err, termOut.String())
	}
	// The pairing code is skipped for now (the enrolling person is
	// verified): the terminal names the host and says it waits for the
	// owner, and never shows a code to compare.
	if out := termOut.String(); strings.Contains(out, pending[0].PairingCode) || !strings.Contains(out, "Waiting for the deployment owner to accept it") {
		t.Errorf("terminal output should wait for acceptance without a pairing code:\n%s", out)
	}
	for _, secretFile := range []string{keyPath, tokPath} {
		st, err := os.Stat(secretFile)
		if err != nil {
			t.Fatalf("stat %s: %v", secretFile, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", secretFile, st.Mode().Perm())
		}
	}

	// Revoking the enrollment code leaves the already-accepted host untouched.
	if _, err := ownerCLI.RevokeHostEnrollmentCode(ctx, "oss-contributors"); err != nil {
		t.Fatalf("RevokeHostEnrollmentCode: %v", err)
	}

	// A copied credential WITHOUT the host's Ed25519 private key cannot connect to /api/hostd/connect!
	copiedReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+hostd.RemotePath, nil)
	copiedReq.Header.Set(hostd.TokenHeader, enrollRes.Token)
	copiedResp, err := ts.Client().Do(copiedReq)
	if err != nil {
		t.Fatal(err)
	}
	copiedResp.Body.Close()
	if copiedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("copied token without Ed25519 signature got %d, want 401", copiedResp.StatusCode)
	}

	// Before connecting, POST /api/hostd/renew is refused (renewal requires an active connection).
	priv, err := hostd.LoadPrivateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	proof1, err := hostd.SignRenewProof(acceptedHost.Name, enrollRes.Token, priv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	proof1Raw, _ := json.Marshal(proof1)
	disconnRenewReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/hostd/renew", bytes.NewReader(proof1Raw))
	disconnRenewReq.Header.Set("Content-Type", "application/json")
	disconnRenewReq.Header.Set(hostd.TokenHeader, enrollRes.Token)
	disconnRenewResp, err := ts.Client().Do(disconnRenewReq)
	if err != nil {
		t.Fatal(err)
	}
	disconnRenewResp.Body.Close()
	if disconnRenewResp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /api/hostd/renew while offline = %d, want 403", disconnRenewResp.StatusCode)
	}

	// Dial in with the host's Ed25519 private key + issued credential!
	conn, err := (hostd.Dialer{
		Server:     ts.URL,
		Token:      enrollRes.Token,
		PrivateKey: priv,
		Host:       "dev-workstation",
	}).Dial(ctx)
	if err != nil {
		t.Fatalf("Dial with Ed25519 key + credential: %v", err)
	}
	defer conn.Close()

	connDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(connDeadline) && !remote.IsConnected(acceptedHost.Name) {
		time.Sleep(20 * time.Millisecond)
	}
	if !remote.IsConnected(acceptedHost.Name) {
		t.Fatal("key-bound host did not connect")
	}

	// While connected, POST /api/hostd/renew with a valid Ed25519 signature succeeds!
	proof2, err := hostd.SignRenewProof(acceptedHost.Name, enrollRes.Token, priv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	proof2Raw, _ := json.Marshal(proof2)
	renewReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/hostd/renew", bytes.NewReader(proof2Raw))
	renewReq.Header.Set("Content-Type", "application/json")
	renewReq.Header.Set(hostd.TokenHeader, enrollRes.Token)
	renewResp, err := ts.Client().Do(renewReq)
	if err != nil {
		t.Fatal(err)
	}
	var renewed hostd.RenewReply
	_ = json.NewDecoder(renewResp.Body).Decode(&renewed)
	renewResp.Body.Close()
	if renewResp.StatusCode != http.StatusOK || !strings.HasPrefix(renewed.Token, model.HostdTokenPrefix) {
		t.Fatalf("POST /api/hostd/renew while connected = %d (%+v)", renewResp.StatusCode, renewed)
	}
	// The HTTP renewal recorded its nonce in the hub's replay cache, the one
	// the in-band OpRenew checks, so the same proof cannot renew twice.
	if remote.ReplayCache().CheckAndRecord("renew:"+hostd.PublicKeyString(priv.Public().(ed25519.PublicKey)), proof2.Nonce, time.Now().UTC(), 2*hostd.ConnectProofSkew) {
		t.Errorf("POST /api/hostd/renew did not record its nonce in the hub's replay cache")
	}

	// Rename the connected host -> live session renames cleanly.
	renamed, err := ownerCLI.RenameHostd(ctx, acceptedHost.Name, "octocat-box")
	if err != nil || renamed.Name != "octocat-box" || !remote.IsConnected("octocat-box") {
		t.Fatalf("RenameHostd = %+v, %v (connected=%v)", renamed, err, remote.IsConnected("octocat-box"))
	}

	// Deleting while connected is refused; revoking drops the live connection immediately; deleting then succeeds!
	if _, err := ownerCLI.DeleteHostd(ctx, "octocat-box"); err == nil {
		t.Fatal("DeleteHostd while connected succeeded, want refusal")
	}
	if _, err := ownerCLI.RevokeHostd(ctx, "octocat-box"); err != nil {
		t.Fatalf("RevokeHostd: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && remote.IsConnected("octocat-box") {
		time.Sleep(20 * time.Millisecond)
	}
	if remote.IsConnected("octocat-box") {
		t.Fatal("revoked host remained connected")
	}
	if _, err := ownerCLI.DeleteHostd(ctx, "octocat-box"); err != nil {
		t.Fatalf("DeleteHostd after revoke: %v", err)
	}
}

// A key-bound host's credential is refused on /api/hostd/connect without
// its private key, so a copied one must not be able to file a rekey that
// moves the host to the copier's key either; and a legacy per-host token,
// which has no key behind it, never renews over POST /api/hostd/renew.
func TestHostdRekeyNeedsTheBoundKeyAndLegacyTokensDoNotRenew(t *testing.T) {
	client, store, ctx := testClient(t)
	client.Now = nil
	srv := ui.NewServerWithClient(client)
	now := time.Now().UTC()

	boundPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boundKey := hostd.PublicKeyString(boundPub)
	jr, err := store.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey: boundKey, KeyFingerprint: hostd.KeyFingerprint(boundPub),
		Hostname: "bound-box", EnrolledBy: "owner@example.com",
	}, mustDefaultEnrollmentCode(t, store), now)
	if err != nil {
		t.Fatalf("CreateHostJoinRequest: %v", err)
	}
	if _, _, err := store.AcceptHostJoinRequest(ctx, jr.ID, model.AcceptHostJoinOptions{AcceptedBy: "owner@example.com"}, now); err != nil {
		t.Fatalf("AcceptHostJoinRequest: %v", err)
	}
	_, _, cred, _, accepted, err := store.ClaimAcceptedHostJoinRequest(ctx, jr.ID, now)
	if err != nil || !accepted {
		t.Fatalf("ClaimAcceptedHostJoinRequest = %v, %v", accepted, err)
	}

	rekey := func(priv ed25519.PrivateKey) *httptest.ResponseRecorder {
		t.Helper()
		req := hostd.JoinSubmitRequest{RekeyHostd: "bound-box", Hostname: "bound-box", OS: "linux", Arch: "amd64"}
		if err := hostd.SignJoinSubmit(&req, priv, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(req)
		return serveFrom(srv, "203.0.113.70:443", http.MethodPost, "/api/hostd/join", string(body), map[string]string{
			"Content-Type":    "application/json",
			hostd.TokenHeader: cred,
		})
	}

	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if rec := rekey(otherPriv); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "key-bound") {
		t.Fatalf("rekey of a key-bound host with a copied credential and another key = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if pending, err := store.ListPendingHostJoinRequests(ctx, time.Now().UTC()); err != nil || len(pending) != 0 {
		t.Fatalf("pending after refused rekey = %+v, %v", pending, err)
	}
	if h, err := store.GetHostd(ctx, "bound-box"); err != nil || h == nil || h.PublicKey != boundKey {
		t.Fatalf("host after refused rekey = %+v, %v", h, err)
	}

	// A legacy per-host token does not renew, even with a well-formed proof.
	_, legacyTok, err := store.CreateHostd(ctx, model.Hostd{Name: "legacy-box"})
	if err != nil {
		t.Fatalf("CreateHostd: %v", err)
	}
	proof, err := hostd.SignRenewProof("legacy-box", legacyTok, otherPriv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, _ := json.Marshal(proof)
	rec := serveFrom(srv, "203.0.113.71:443", http.MethodPost, "/api/hostd/renew", string(proofRaw), map[string]string{
		"Content-Type":    "application/json",
		hostd.TokenHeader: legacyTok,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/hostd/renew with a legacy token = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if got, err := store.HostdForTokenAt(ctx, legacyTok, time.Now().UTC().Add(365*24*time.Hour)); err != nil || got == nil {
		t.Fatalf("legacy token no longer resolves after a refused renew: %+v, %v", got, err)
	}
}

func mustDefaultEnrollmentCode(t *testing.T, store *model.Store) string {
	t.Helper()
	c, err := store.EnsureDefaultHostEnrollmentCode(context.Background(), "owner@example.com")
	if err != nil || c == nil || c.SecretCode == "" {
		t.Fatalf("EnsureDefaultHostEnrollmentCode = %+v, %v", c, err)
	}
	return c.SecretCode
}

// TestHostdJoinRefusesMisleadingMetadata pins that what a joining host says
// about itself -- shown verbatim to the owner deciding on it, in Settings ->
// Hosts and `grain host pending` -- carries no control or formatting
// characters and no field longer than a real host's, and that a refused
// request leaves nothing pending.
func TestHostdJoinRefusesMisleadingMetadata(t *testing.T) {
	client, store, _ := testClient(t)
	client.Now = nil
	client.Config.LocalAPIToken = testAccessCode
	srv := ui.NewServerWithClient(client)
	code := mustDefaultEnrollmentCode(t, store)

	join := func(mut func(*hostd.JoinSubmitRequest)) *httptest.ResponseRecorder {
		t.Helper()
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		req := hostd.JoinSubmitRequest{EnrollmentCode: code, Hostname: "build-01", OS: "linux", Arch: "amd64", Version: "v1.2.3", LocalUser: "alice", CLIs: []string{"claude"}}
		mut(&req)
		if err := hostd.SignJoinSubmit(&req, priv, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(req)
		return serveFrom(srv, "203.0.113.80:443", http.MethodPost, "/api/hostd/join", string(body), map[string]string{
			"Content-Type":      "application/json",
			ui.AccessCodeHeader: testAccessCode,
		})
	}
	for name, mut := range map[string]func(*hostd.JoinSubmitRequest){
		"ansi escape in hostname": func(r *hostd.JoinSubmitRequest) { r.Hostname = "build-01\x1b[2K\renrolled by owner@example.com" },
		"newline in hostname":     func(r *hostd.JoinSubmitRequest) { r.Hostname = "build-01\nenrolled-by-owner" },
		"space in hostname":       func(r *hostd.JoinSubmitRequest) { r.Hostname = "build-01 (owner@example.com)" },
		"bidi override in os":     func(r *hostd.JoinSubmitRequest) { r.OS = "linux\u202e" },
		"long hostname":           func(r *hostd.JoinSubmitRequest) { r.Hostname = strings.Repeat("a", 254) },
		"control in version":      func(r *hostd.JoinSubmitRequest) { r.Version = "v1\x07" },
		"control in local user":   func(r *hostd.JoinSubmitRequest) { r.LocalUser = "alice\x1b]0;x\x07" },
		"comma in a cli":          func(r *hostd.JoinSubmitRequest) { r.CLIs = []string{"claude,codex"} },
		"too many clis":           func(r *hostd.JoinSubmitRequest) { r.CLIs = make([]string, 33) },
		"invalid utf-8 in arch":   func(r *hostd.JoinSubmitRequest) { r.Arch = "amd64\xff" },
	} {
		if rec := join(mut); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid host join request") {
			t.Errorf("%s: POST /api/hostd/join = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if pending, err := store.ListPendingHostJoinRequests(context.Background(), time.Now().UTC()); err != nil || len(pending) != 0 {
		t.Fatalf("pending after refused joins = %+v, %v", pending, err)
	}
	// An ordinary host still joins, with a version that carries a space.
	if rec := join(func(r *hostd.JoinSubmitRequest) { r.Version = "dev (abc123)" }); rec.Code != http.StatusCreated {
		t.Fatalf("ordinary join = %d %s, want 201", rec.Code, rec.Body.String())
	}
}
