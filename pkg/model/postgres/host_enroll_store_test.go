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

package postgres_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPostgresHostEnrollmentCodeJoinHandshakeAndMigration117(t *testing.T) {
	t.Parallel()
	s, db, ctx := openStore(t)

	// Rewind from 117 -> 116, drop the two tables migration 117 creates, and
	// verify migrating 116 -> 117 succeeds cleanly on PostgreSQL.
	for _, stmt := range []string{"DROP TABLE `host_join_request`", "DROP TABLE `host_enrollment_code`"} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	rewindSchemaTo(t, ctx, db, 116)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-116 store to 117: %v", err)
	}

	now := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)

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

	h1, _, err := s.AcceptHostJoinRequest(ctx, jr1.ID, model.AcceptHostJoinOptions{
		AcceptedBy: "owner@example.com",
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("AcceptHostJoinRequest: %v", err)
	}
	if h1.Name != "alice-laptop" || h1.Owner != "alice@example.com" || h1.Capacity != 4 {
		t.Fatalf("accepted host = %+v", h1)
	}

	claimedHost, _, cred, credExp, accepted, err := s.ClaimAcceptedHostJoinRequest(ctx, jr1.ID, now.Add(3*time.Minute))
	if err != nil || !accepted || claimedHost == nil || cred == "" {
		t.Fatalf("ClaimAcceptedHostJoinRequest = host:%+v cred:%q exp:%v accepted:%v err:%v", claimedHost, cred, credExp, accepted, err)
	}

	if got, err := s.HostdForTokenAt(ctx, cred, now.Add(29*24*time.Hour)); err != nil || got == nil || got.Name != "alice-laptop" {
		t.Fatalf("HostdForTokenAt(29d) = %+v, %v", got, err)
	}
	if got, err := s.HostdForTokenAt(ctx, cred, now.Add(31*24*time.Hour)); err != nil || got != nil {
		t.Fatalf("HostdForTokenAt(31d) = %+v, %v; want nil", got, err)
	}

	renewAt := now.Add(20 * 24 * time.Hour)
	cred2, _, err := s.RenewHostdCredential(ctx, "alice-laptop", cred, renewAt)
	if err != nil {
		t.Fatalf("RenewHostdCredential: %v", err)
	}

	renamed, err := s.RenameHostd(ctx, "alice-laptop", "alice-ws")
	if err != nil || renamed.Name != "alice-ws" {
		t.Fatalf("RenameHostd = %+v, %v", renamed, err)
	}
	if got, err := s.HostdForTokenAt(ctx, cred2, renewAt.Add(time.Hour)); err != nil || got == nil || got.Name != "alice-ws" {
		t.Fatalf("token after RenameHostd = %+v, %v", got, err)
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
	if _, err := s.RevokeHostEnrollmentCode(ctx, "team-workstations"); err != nil {
		t.Fatalf("RevokeHostEnrollmentCode: %v", err)
	}
	if _, err := s.LookupHostEnrollmentCode(ctx, rawCode, now.Add(10*time.Minute)); !errors.Is(err, model.ErrEnrollmentCodeRevoked) {
		t.Fatalf("LookupHostEnrollmentCode after revoke = %v, want ErrEnrollmentCodeRevoked", err)
	}
}
