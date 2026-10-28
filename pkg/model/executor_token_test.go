package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestExecutorTokenGenerationAndHashing(t *testing.T) {
	raw, tok, err := model.GenerateExecutorToken("builder-1")
	if err != nil {
		t.Fatalf("GenerateExecutorToken failed: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty raw token")
	}
	if tok.Prefix != raw[:8] {
		t.Fatalf("expected prefix %q, got %q", raw[:8], tok.Prefix)
	}
	if tok.TokenHash != model.HashExecutorToken(raw) {
		t.Fatalf("expected token hash %q, got %q", model.HashExecutorToken(raw), tok.TokenHash)
	}
}

func TestExecutorTokenCRUDAndRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)

	// Initially empty
	tokens, err := st.ListExecutorTokens(ctx)
	if err != nil {
		t.Fatalf("ListExecutorTokens failed: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens, got %d", len(tokens))
	}

	// Create a token
	raw1, genTok1, err := model.GenerateExecutorToken("gpu-worker")
	if err != nil {
		t.Fatalf("GenerateExecutorToken failed: %v", err)
	}
	created1, err := st.CreateExecutorToken(ctx, genTok1)
	if err != nil {
		t.Fatalf("CreateExecutorToken failed: %v", err)
	}
	if created1.ID == "" {
		t.Fatal("expected generated token ID")
	}
	if created1.Name != "gpu-worker" {
		t.Fatalf("unexpected token fields: %+v", created1)
	}

	// Create a second token
	raw2, genTok2, err := model.GenerateExecutorToken("cpu-worker")
	if err != nil {
		t.Fatalf("GenerateExecutorToken failed: %v", err)
	}
	if _, err := st.CreateExecutorToken(ctx, genTok2); err != nil {
		t.Fatalf("CreateExecutorToken failed: %v", err)
	}

	// List tokens
	tokens, err = st.ListExecutorTokens(ctx)
	if err != nil {
		t.Fatalf("ListExecutorTokens failed: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	// Get by ID
	byID, err := st.GetExecutorToken(ctx, created1.ID)
	if err != nil {
		t.Fatalf("GetExecutorToken failed: %v", err)
	}
	if byID == nil || byID.ID != created1.ID {
		t.Fatalf("token mismatch: got %+v, want %+v", byID, created1)
	}

	// Get by Hash
	hash1 := model.HashExecutorToken(raw1)
	byHash, err := st.GetExecutorTokenByHash(ctx, hash1)
	if err != nil {
		t.Fatalf("GetExecutorTokenByHash failed: %v", err)
	}
	if byHash == nil || byHash.ID != created1.ID {
		t.Fatalf("byHash mismatch: got %+v, want %+v", byHash, created1)
	}

	// Touch last connected
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.TouchExecutorTokenConnected(ctx, created1.ID, now); err != nil {
		t.Fatalf("TouchExecutorTokenConnected failed: %v", err)
	}
	touched, err := st.GetExecutorToken(ctx, created1.ID)
	if err != nil {
		t.Fatalf("GetExecutorToken failed: %v", err)
	}
	if touched.LastConnectedAt == nil {
		t.Fatal("expected non-nil LastConnectedAt")
	}

	// Revoke token 1
	revoked, err := st.RevokeExecutorToken(ctx, created1.ID)
	if err != nil {
		t.Fatalf("RevokeExecutorToken failed: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("expected non-nil RevokedAt after revocation")
	}

	// Verify revoked state in GetExecutorToken
	fetchedRevoked, err := st.GetExecutorToken(ctx, created1.ID)
	if err != nil {
		t.Fatalf("GetExecutorToken failed: %v", err)
	}
	if fetchedRevoked.RevokedAt == nil {
		t.Fatal("expected non-nil RevokedAt in fetched token")
	}

	// Verify token 2 is still active
	activeTok2, err := st.GetExecutorTokenByHash(ctx, model.HashExecutorToken(raw2))
	if err != nil {
		t.Fatalf("GetExecutorTokenByHash failed: %v", err)
	}
	if activeTok2.RevokedAt != nil {
		t.Fatal("token 2 should not be revoked")
	}
}
