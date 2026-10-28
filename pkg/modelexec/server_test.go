package modelexec

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
)

func newTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := model.New(db)
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return st
}

func putTeams(t *testing.T, st *model.Store, ids ...string) {
	t.Helper()
	_ = ids
}

func TestStoreBackedTokenAuthenticationAndRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newTestStore(t)
	putTeams(t, st, "team-alpha", "team-beta")

	// Issue two tokens in different teams
	raw1, tok1, err := model.GenerateExecutorToken("executor-a")
	if err != nil {
		t.Fatalf("GenerateExecutorToken: %v", err)
	}
	created1, err := st.CreateExecutorToken(ctx, tok1)
	if err != nil {
		t.Fatalf("CreateExecutorToken: %v", err)
	}

	raw2, tok2, err := model.GenerateExecutorToken("executor-b")
	if err != nil {
		t.Fatalf("GenerateExecutorToken: %v", err)
	}
	created2, err := st.CreateExecutorToken(ctx, tok2)
	if err != nil {
		t.Fatalf("CreateExecutorToken: %v", err)
	}

	// Create Server backed by Store
	srv, err := NewServer(Config{
		Store: st,
		Wait:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy": "/bin/sh",
	}

	// 1. Connect daemon using token 1
	d1 := &Daemon{
		Addr:     ln.Addr().String(),
		Token:    raw1,
		Name:     "worker-alpha",
		Binaries: binaries,
	}
	d1Ctx, cancelD1 := context.WithCancel(ctx)
	defer cancelD1()
	go func() { _ = d1.Run(d1Ctx) }()

	// Wait for host in team-alpha
	host1, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host(team-alpha): %v", err)
	}
	if host1 == nil {
		t.Fatal("expected host1 connected")
	}

	// Verify status includes TokenID and team
	statuses := srv.Connected()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].TokenID != created1.ID {
		t.Fatalf("expected token ID %q, got %q", created1.ID, statuses[0].TokenID)
	}

	// Verify TouchExecutorTokenConnected was invoked
	fetched1, err := st.GetExecutorToken(ctx, created1.ID)
	if err != nil {
		t.Fatalf("GetExecutorToken: %v", err)
	}
	if fetched1.LastConnectedAt == nil {
		t.Fatal("expected LastConnectedAt to be updated")
	}

	// 2. Connect daemon with wrong token -> should fail
	dWrong := &Daemon{
		Addr:     ln.Addr().String(),
		Token:    "invalid-token-12345",
		Name:     "worker-invalid",
		Binaries: binaries,
	}
	dWrongCtx, cancelDWrong := context.WithTimeout(ctx, 1*time.Second)
	defer cancelDWrong()
	err = dWrong.runOnce(dWrongCtx, "worker-invalid", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "wrong token") {
		t.Fatalf("expected wrong token error, got: %v", err)
	}

	// 3. Revoke token 2 in store and attempt connection -> should fail with token revoked
	_, err = st.RevokeExecutorToken(ctx, created2.ID)
	if err != nil {
		t.Fatalf("RevokeExecutorToken: %v", err)
	}

	dRevoked := &Daemon{
		Addr:     ln.Addr().String(),
		Token:    raw2,
		Name:     "worker-revoked",
		Binaries: binaries,
	}
	dRevCtx, cancelDRev := context.WithTimeout(ctx, 1*time.Second)
	defer cancelDRev()
	err = dRevoked.runOnce(dRevCtx, "worker-revoked", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "token revoked") {
		t.Fatalf("expected 'token revoked' error, got: %v", err)
	}

	// 4. Test immediate disconnect of active session via srv.RevokeToken
	if len(srv.Connected()) != 1 {
		t.Fatalf("expected 1 connected host before revocation, got %d", len(srv.Connected()))
	}
	srv.RevokeToken(created1.ID)

	// Host should be disconnected immediately
	time.Sleep(50 * time.Millisecond)
	if len(srv.Connected()) != 0 {
		t.Fatalf("expected 0 connected hosts after srv.RevokeToken, got %d", len(srv.Connected()))
	}
}
