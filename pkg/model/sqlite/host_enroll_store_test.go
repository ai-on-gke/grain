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

package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openHostEnrollStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := model.New(db)
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

func TestHostEnrollmentCodeAndJoinHandshakeLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openHostEnrollStore(t)
	now := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)

	// Default code seeds once, never leaks SecretCode via json.Marshal,
	// and does not auto-resurrect after revocation.
	def, err := s.EnsureDefaultHostEnrollmentCode(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("EnsureDefaultHostEnrollmentCode: %v", err)
	}
	if def == nil || !strings.HasPrefix(def.SecretCode, model.HostEnrollmentCodePrefix) {
		t.Fatalf("unexpected default code: %+v", def)
	}
	marshaled, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json.Marshal(def): %v", err)
	}
	if strings.Contains(string(marshaled), def.SecretCode) {
		t.Fatalf("json.Marshal leaked SecretCode: %s", marshaled)
	}

	// Create a restricted code with max_uses=2 and default labels/capacity.
	exp := now.Add(48 * time.Hour)
	codeRec, rawCode, err := s.CreateHostEnrollmentCode(ctx, model.HostEnrollmentCode{
		Name:              "team-workstations",
		DefaultLabels:     map[string]string{"pool": "dev", "gpu": "none"},
		DefaultCapacity:   4,
		AllowedPrincipals: []string{"User:Alice@Example.com", "group:eng@example.com", "github:OctoCat"},
		MaxUses:           2,
		ExpiresAt:         &exp,
		CreatedBy:         "owner@example.com",
	})
	if err != nil {
		t.Fatalf("CreateHostEnrollmentCode: %v", err)
	}
	if len(codeRec.AllowedPrincipals) != 3 || codeRec.AllowedPrincipals[0] != "user:alice@example.com" {
		t.Fatalf("AllowedPrincipals = %v", codeRec.AllowedPrincipals)
	}

	// Join request #1: accepted and claimed -> 30d key-bound credential + handshake row deleted.
	jr1, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey:      "ed25519:AAAA",
		KeyFingerprint: "SHA256:fp-1",
		Hostname:       "Alice-Laptop.local",
		OS:             "linux",
		Arch:           "amd64",
		CLIs:           []string{"claude", "codex"},
		Version:        "v2.5.0",
		LocalUser:      "alice",
		EnrolledBy:     "Alice@Example.com",
	}, rawCode, now)
	if err != nil {
		t.Fatalf("CreateHostJoinRequest #1: %v", err)
	}
	if jr1.ExpiresAt.Sub(now) != 24*time.Hour {
		t.Fatalf("ExpiresAt delta = %v, want 24h", jr1.ExpiresAt.Sub(now))
	}

	// Poll before acceptance returns accepted=false.
	_, _, tok, _, accepted, err := s.ClaimAcceptedHostJoinRequest(ctx, jr1.ID, now.Add(time.Minute))
	if err != nil || accepted || tok != "" {
		t.Fatalf("pre-accept claim = accepted:%v tok:%q err:%v", accepted, tok, err)
	}

	h1, acceptedReq, err := s.AcceptHostJoinRequest(ctx, jr1.ID, model.AcceptHostJoinOptions{
		AcceptedBy: "owner@example.com",
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("AcceptHostJoinRequest: %v", err)
	}
	if h1.Name != "alice-laptop" || h1.Owner != "alice@example.com" || h1.Capacity != 4 || h1.Labels["pool"] != "dev" {
		t.Fatalf("accepted host = %+v (req=%+v)", h1, acceptedReq)
	}
	if !h1.KeyBound() || h1.PublicKey != "ed25519:AAAA" || h1.EnrollmentCode != "team-workstations" {
		t.Fatalf("key binding missing on accepted host: %+v", h1)
	}

	_, claimedHost, cred, credExp, accepted, err := s.ClaimAcceptedHostJoinRequest(ctx, jr1.ID, now.Add(3*time.Minute))
	if err != nil || !accepted || !strings.HasPrefix(cred, model.HostdTokenPrefix) {
		t.Fatalf("post-accept claim = accepted:%v cred:%q err:%v", accepted, cred, err)
	}
	if credExp.Sub(now.Add(3*time.Minute)) != 30*24*time.Hour || claimedHost.Name != "alice-laptop" {
		t.Fatalf("unexpected credential expiry %v or host %+v", credExp, claimedHost)
	}
	// Claiming twice fails because the handshake row is deleted on first claim.
	if _, _, _, _, _, err := s.ClaimAcceptedHostJoinRequest(ctx, jr1.ID, now.Add(4*time.Minute)); !errors.Is(err, model.ErrJoinRequestNotFound) {
		t.Fatalf("second claim err = %v, want ErrJoinRequestNotFound", err)
	}

	// Credential resolves inside 30d and expires after 30d unless renewed.
	if got, err := s.HostdForTokenAt(ctx, cred, now.Add(29*24*time.Hour)); err != nil || got == nil || got.Name != "alice-laptop" {
		t.Fatalf("HostdForTokenAt(29d) = %+v, %v", got, err)
	}
	if got, err := s.HostdForTokenAt(ctx, cred, now.Add(31*24*time.Hour)); err != nil || got != nil {
		t.Fatalf("HostdForTokenAt(31d) = %+v, %v; want nil", got, err)
	}

	// Silent renewal at day 20 mints a fresh 30d credential (valid to day 50).
	renewAt := now.Add(20 * 24 * time.Hour)
	cred2, cred2Exp, err := s.RenewHostdCredential(ctx, "alice-laptop", cred, renewAt)
	if err != nil {
		t.Fatalf("RenewHostdCredential: %v", err)
	}
	if cred2 == cred || cred2Exp.Sub(renewAt) != 30*24*time.Hour {
		t.Fatalf("renewed cred=%q exp=%v", cred2, cred2Exp)
	}
	if got, err := s.HostdForTokenAt(ctx, cred2, now.Add(49*24*time.Hour)); err != nil || got == nil {
		t.Fatalf("HostdForTokenAt(49d) after renew = %+v, %v", got, err)
	}
	// Old credential stops working once the 2-minute grace window passes.
	if got, err := s.HostdForTokenAt(ctx, cred, renewAt.Add(5*time.Minute)); err != nil || got != nil {
		t.Fatalf("superseded credential still valid after grace: %+v, %v", got, err)
	}

	// Join request #2 consumes the second of max_uses=2; rejecting deletes the row cleanly.
	jr2, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey:      "ed25519:BBBB",
		KeyFingerprint: "SHA256:fp-2",
		Hostname:       "bob-box",
		EnrolledBy:     "github:octocat",
	}, rawCode, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("CreateHostJoinRequest #2: %v", err)
	}
	// 3rd attempt hits ErrEnrollmentCodeExhausted.
	if _, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey:      "ed25519:CCCC",
		KeyFingerprint: "SHA256:fp-3",
		Hostname:       "charlie-box",
		EnrolledBy:     "alice@example.com",
	}, rawCode, now.Add(6*time.Minute)); !errors.Is(err, model.ErrEnrollmentCodeExhausted) {
		t.Fatalf("3rd join err = %v, want ErrEnrollmentCodeExhausted", err)
	}
	if _, err := s.RejectHostJoinRequest(ctx, jr2.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatalf("RejectHostJoinRequest: %v", err)
	}
	if rem, err := s.ListPendingHostJoinRequests(ctx, now.Add(7*time.Minute)); err != nil || len(rem) != 0 {
		t.Fatalf("pending after reject = %+v, %v", rem, err)
	}

	// Revoking the enrollment code stops new joins without touching alice-laptop.
	if _, err := s.RevokeHostEnrollmentCode(ctx, "team-workstations"); err != nil {
		t.Fatalf("RevokeHostEnrollmentCode: %v", err)
	}
	if _, err := s.LookupHostEnrollmentCode(ctx, rawCode, now.Add(10*time.Minute)); !errors.Is(err, model.ErrEnrollmentCodeRevoked) {
		t.Fatalf("LookupHostEnrollmentCode after revoke = %v, want ErrEnrollmentCodeRevoked", err)
	}
	if got, err := s.HostdForTokenAt(ctx, cred2, renewAt.Add(time.Hour)); err != nil || got == nil || got.Name != "alice-laptop" {
		t.Fatalf("revoking code touched accepted host: %+v, %v", got, err)
	}

	// Expired pending requests (after 24h) leave nothing behind.
	jr3, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey:      "ed25519:DDDD",
		KeyFingerprint: "SHA256:fp-4",
		Hostname:       "temp-host",
		EnrolledBy:     "owner@example.com",
	}, def.SecretCode, now)
	if err != nil {
		t.Fatalf("CreateHostJoinRequest #3: %v", err)
	}
	if got, err := s.GetHostJoinRequest(ctx, jr3.ID, now.Add(24*time.Hour+time.Second)); err != nil || got != nil {
		t.Fatalf("expired request still returned: %+v, %v", got, err)
	}

	// Revoking default code as well does not auto-mint a fresh default code.
	if _, err := s.RevokeHostEnrollmentCode(ctx, model.DefaultHostEnrollmentCodeName); err != nil {
		t.Fatalf("RevokeHostEnrollmentCode(default): %v", err)
	}
	afterRevoke, err := s.EnsureDefaultHostEnrollmentCode(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("EnsureDefaultHostEnrollmentCode after revoke: %v", err)
	}
	if afterRevoke == nil || afterRevoke.RevokedAt == nil || afterRevoke.SecretCode != "" {
		t.Fatalf("revoked default code unexpectedly active: %+v", afterRevoke)
	}

	// Rename, update capacity, revoke and delete host lifecycle.
	renamed, err := s.RenameHostd(ctx, "alice-laptop", "alice-ws")
	if err != nil || renamed.Name != "alice-ws" {
		t.Fatalf("RenameHostd = %+v, %v", renamed, err)
	}
	if got, err := s.HostdForTokenAt(ctx, cred2, renewAt.Add(time.Hour)); err != nil || got == nil || got.Name != "alice-ws" {
		t.Fatalf("token after RenameHostd = %+v, %v", got, err)
	}
	cap8 := 8
	updated, err := s.UpdateHostdDetails(ctx, "alice-ws", model.HostdUpdate{Capacity: &cap8})
	if err != nil || updated.Capacity != 8 {
		t.Fatalf("UpdateHostdDetails = %+v, %v", updated, err)
	}
	if err := s.DeleteHostd(ctx, "alice-ws"); !errors.Is(err, model.ErrHostdLive) {
		t.Fatalf("DeleteHostd on a live host = %v, want ErrHostdLive", err)
	}
	if _, err := s.RevokeHostd(ctx, "alice-ws"); err != nil {
		t.Fatalf("RevokeHostd: %v", err)
	}
	if err := s.DeleteHostd(ctx, "alice-ws"); err != nil {
		t.Fatalf("DeleteHostd: %v", err)
	}
	if got, err := s.GetHostd(ctx, "alice-ws"); err != nil || got != nil {
		t.Fatalf("GetHostd after DeleteHostd = %+v, %v", got, err)
	}
}

func TestStoreRedactsHostEnrollmentCodesInTasksAndComments(t *testing.T) {
	ctx := context.Background()
	s := openHostEnrollStore(t)
	now := time.Now().UTC()
	secretCode := "ghe_0123456789abcdef0123456789abcdef0123456789abcdef"

	task := model.Task{
		ID:        "1",
		Title:     "Enroll with " + secretCode,
		Body:      "Run curl -fsSL https://grain.example/hostd/install | sh -s -- --join " + secretCode,
		CreatedAt: &now,
		Owner:     "owner@example.com",
		Origin: model.Origin{
			Reason:      model.ReasonDirect,
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}},
		},
	}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	stored, err := s.GetTask(ctx, "1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if strings.Contains(stored.Title, secretCode) || strings.Contains(stored.Body, secretCode) || strings.Contains(stored.Branch, "0123456789abcdef") {
		t.Fatalf("enrollment code leaked into stored task: title=%q body=%q branch=%q", stored.Title, stored.Body, stored.Branch)
	}

	if _, err := s.AddComment(ctx, model.Comment{
		TaskID:  "1",
		Author:  model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}},
		Summary: "Used " + secretCode,
		Body:    "Pasted " + secretCode + " here",
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	comments, err := s.Comments(ctx, "1")
	if err != nil || len(comments) != 1 {
		t.Fatalf("ListComments = %+v, %v", comments, err)
	}
	if strings.Contains(comments[0].Body, secretCode) || strings.Contains(comments[0].Summary, secretCode) {
		t.Fatalf("enrollment code leaked into stored comment: %+v", comments[0])
	}
}

// A legacy per-host token migrates to a key-bound credential through
// rekey, but once a host is key-bound a copied credential alone must not
// move it to another key: only a request carrying its bound key rekeys it,
// and a second rekey filed before the first was accepted cannot be accepted
// afterwards. Legacy tokens never renew, and renewal always names the
// current credential.
func TestHostRekeyIsBoundToTheHostsKeyAndOnlyKeyBoundHostsRenew(t *testing.T) {
	ctx := context.Background()
	s := openHostEnrollStore(t)
	now := time.Now().UTC()

	legacy, legacyTok, err := s.CreateHostd(ctx, model.Hostd{Name: "old-box", Owner: "owner@example.com"})
	if err != nil {
		t.Fatalf("CreateHostd: %v", err)
	}
	if legacy.KeyBound() {
		t.Fatalf("legacy host is key-bound: %+v", legacy)
	}

	// Legacy per-host tokens do not renew (with or without a token).
	if _, _, err := s.RenewHostdCredential(ctx, "old-box", legacyTok, now); !errors.Is(err, model.ErrHostdNotKeyBound) {
		t.Fatalf("RenewHostdCredential(legacy) err = %v, want ErrHostdNotKeyBound", err)
	}
	if got, err := s.HostdForTokenAt(ctx, legacyTok, now.Add(365*24*time.Hour)); err != nil || got == nil {
		t.Fatalf("legacy token stopped resolving after a refused renew: %+v, %v", got, err)
	}

	// Two rekey requests race for the legacy host with different keys.
	mine, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		RekeyHostd: "old-box", PublicKey: "ed25519:MINE", KeyFingerprint: "SHA256:mine",
		Hostname: "old-box", EnrolledBy: "owner@example.com",
	}, "", now)
	if err != nil {
		t.Fatalf("CreateHostJoinRequest(rekey mine): %v", err)
	}
	theirs, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		RekeyHostd: "old-box", PublicKey: "ed25519:THEIRS", KeyFingerprint: "SHA256:theirs",
		Hostname: "old-box", EnrolledBy: "mallory@example.com",
	}, "", now)
	if err != nil {
		t.Fatalf("CreateHostJoinRequest(rekey theirs): %v", err)
	}
	if _, _, err := s.AcceptHostJoinRequest(ctx, mine.ID, model.AcceptHostJoinOptions{AcceptedBy: "owner@example.com"}, now); err != nil {
		t.Fatalf("AcceptHostJoinRequest(mine): %v", err)
	}
	_, _, cred, _, accepted, err := s.ClaimAcceptedHostJoinRequest(ctx, mine.ID, now)
	if err != nil || !accepted || cred == "" {
		t.Fatalf("ClaimAcceptedHostJoinRequest(mine) = %v %q %v", accepted, cred, err)
	}

	// The host is key-bound now: the stale request with another key cannot
	// be accepted, and no new one can be filed with another key.
	if _, _, err := s.AcceptHostJoinRequest(ctx, theirs.ID, model.AcceptHostJoinOptions{AcceptedBy: "owner@example.com"}, now); !errors.Is(err, model.ErrRekeyKeyMismatch) {
		t.Fatalf("AcceptHostJoinRequest(theirs) err = %v, want ErrRekeyKeyMismatch", err)
	}
	if h, err := s.GetHostd(ctx, "old-box"); err != nil || h == nil || h.PublicKey != "ed25519:MINE" {
		t.Fatalf("host after refused accept = %+v, %v", h, err)
	}
	if _, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		RekeyHostd: "old-box", PublicKey: "ed25519:OTHER", KeyFingerprint: "SHA256:other",
		Hostname: "old-box", EnrolledBy: "mallory@example.com",
	}, "", now); !errors.Is(err, model.ErrRekeyKeyMismatch) {
		t.Fatalf("CreateHostJoinRequest(rekey other key) err = %v, want ErrRekeyKeyMismatch", err)
	}
	// Its own key (padding aside) may still file one.
	if _, err := s.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		RekeyHostd: "old-box", PublicKey: "ed25519:MINE==", KeyFingerprint: "SHA256:mine",
		Hostname: "old-box", EnrolledBy: "owner@example.com",
	}, "", now); err != nil {
		t.Fatalf("CreateHostJoinRequest(rekey same key): %v", err)
	}

	// Key-bound renewal needs the current credential.
	if _, _, err := s.RenewHostdCredential(ctx, "old-box", "", now); !errors.Is(err, model.ErrHostdRevoked) {
		t.Fatalf("RenewHostdCredential(empty token) err = %v, want ErrHostdRevoked", err)
	}
	if _, _, err := s.RenewHostdCredential(ctx, "old-box", cred, now); err != nil {
		t.Fatalf("RenewHostdCredential(key-bound): %v", err)
	}
}
