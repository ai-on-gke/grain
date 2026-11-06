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

package plcseams_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
)

func TestIdentityDelegationAndGroupSeams(t *testing.T) {
	defer plcseams.ResetForTest()

	plcseams.RegisterGroupResolver(&plcseams.StaticGroupResolver{
		Members: map[string][]string{
			"alice@example.com": {"eng", "plc-admins"},
		},
	})
	plcseams.RegisterDelegationCredentials(&plcseams.HeaderDelegationCredentials{
		StaticSubjectToken: "tok-default-123",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/studio/directory", nil)
	req.Header.Set(plcseams.HeaderOnBehalfOf, "alice@example.com")
	req.Header.Set(plcseams.HeaderSubjectToken, "tok-req-456")

	p, err := plcseams.CurrentIdentityVerifier().VerifyRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("VerifyRequest: %v", err)
	}
	if p.Email != "alice@example.com" || len(p.Groups) != 2 {
		t.Fatalf("Principal = %+v, want alice@example.com with 2 groups", p)
	}

	ctx := plcseams.WithPrincipal(context.Background(), p)
	hdrs, err := plcseams.CurrentDelegationCredentials().HeadersForUser(ctx, "")
	if err != nil {
		t.Fatalf("HeadersForUser: %v", err)
	}
	if got := hdrs.Get(plcseams.HeaderOnBehalfOf); got != "alice@example.com" {
		t.Fatalf("X-Grain-On-Behalf-Of = %q, want alice@example.com", got)
	}
	if got := hdrs.Get(plcseams.HeaderSubjectToken); got != "tok-default-123" {
		t.Fatalf("X-Grain-Subject-Token = %q, want tok-default-123", got)
	}
}

func TestHMACChatAdapterVerificationAndOutbox(t *testing.T) {
	defer plcseams.ResetForTest()

	adapter := plcseams.NewHMACChatAdapter("super-secret")
	plcseams.RegisterChatAdapter(adapter)

	payload, _ := json.Marshal(plcseams.ChatInvocation{
		UserEmail:   "bob@example.com",
		SpaceID:     "spaces/AAA",
		ThreadID:    "threads/111",
		RawText:     "/grain status bug:98765",
		ExternalRef: "bug:98765",
	})

	// Unsigned request must fail when Secret is set.
	badReq := httptest.NewRequest(http.MethodPost, "/api/chat/events", bytes.NewReader(payload))
	if _, err := plcseams.CurrentChatAdapter().VerifyAndParse(context.Background(), badReq); !errors.Is(err, plcseams.ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature on unsigned request, got %v", err)
	}

	// Properly signed request succeeds and parses command + args.
	goodReq := httptest.NewRequest(http.MethodPost, "/api/chat/events", bytes.NewReader(payload))
	goodReq.Header.Set(plcseams.HeaderChatSignature, plcseams.SignPayload("super-secret", payload))
	inv, err := plcseams.CurrentChatAdapter().VerifyAndParse(context.Background(), goodReq)
	if err != nil {
		t.Fatalf("VerifyAndParse: %v", err)
	}
	if inv.Command != "status" || len(inv.Args) != 1 || inv.Args[0] != "bug:98765" {
		t.Fatalf("parsed invocation = %+v, want command=status args=[bug:98765]", inv)
	}

	if err := plcseams.CurrentChatAdapter().PostMessage(context.Background(), plcseams.ChatMessage{
		SpaceID: "spaces/AAA",
		Text:    "Task #42 is merged",
	}); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if got := len(adapter.Outbox()); got != 1 {
		t.Fatalf("outbox len = %d, want 1", got)
	}
}

func TestMemorySpecFetcher(t *testing.T) {
	defer plcseams.ResetForTest()

	fetcher := plcseams.NewMemorySpecFetcher(map[string][]byte{
		"spec:launch-v1": []byte("title: Launch v1\n"),
	})
	plcseams.RegisterSpecFetcher(fetcher)

	b, err := plcseams.CurrentSpecFetcher().FetchSpec(context.Background(), "spec:launch-v1")
	if err != nil || string(b) != "title: Launch v1\n" {
		t.Fatalf("FetchSpec = %q, err=%v", string(b), err)
	}
	if _, err := plcseams.CurrentSpecFetcher().FetchSpec(context.Background(), "spec:missing"); !errors.Is(err, plcseams.ErrSpecNotFound) {
		t.Fatalf("expected ErrSpecNotFound, got %v", err)
	}
}
