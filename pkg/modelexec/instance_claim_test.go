package modelexec

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestExecutorTokenCannotEvictAnotherTokensInstance is a client holding a
// valid executor token -- another team's, or another token of the same
// team -- that says in its hello it is an instance that is already
// connected. The instance id is the executor's own word and visible to
// anyone who can list executors, so the claim must be refused in the
// welcome frame and leave the live connection, and its slot, alone. The
// token that holds the id may still reconnect over it (an executor whose
// old connection is half-dead and not yet reaped by the ping timeout), and
// once the holder is gone the id is free for anyone.
func TestExecutorTokenCannotEvictAnotherTokensInstance(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := model.New(db)
	if err := st.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	newToken := func(name, team string) string {
		raw, tok, err := model.GenerateExecutorToken(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateExecutorToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	rawAlpha := newToken("alpha", "team-alpha")
	rawAlphaRotated := newToken("alpha-rotated", "team-alpha")
	rawBeta := newToken("beta", "team-beta")

	srv, err := NewServer(Config{Store: st, Wait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, ln) }()
	addr := ln.Addr().String()

	const victimID = "alpha-node-1"
	dialRawExecutor(t, ctx, addr, rawAlpha, victimID)
	host, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host: %v", err)
	}
	victimConn := host.conn

	for _, claim := range []struct{ name, token string }{
		{"another team's token", rawBeta},
		{"another team's token claiming the victim's team", rawBeta},
		{"another token of the same team", rawAlphaRotated},
	} {
		_, refusal := helloRawExecutor(t, ctx, addr, claim.token, victimID)
		if !strings.Contains(refusal, "instance id in use") {
			t.Errorf("%s claiming live instance %q: welcome error = %q, want a refusal", claim.name, victimID, refusal)
		}
		if victimConn.closed.Load() {
			t.Fatalf("%s claiming instance %q closed the victim's connection", claim.name, victimID)
		}
		if got := srv.hostByID(victimID); got != victimConn {
			t.Fatalf("%s claiming instance %q took its slot", claim.name, victimID)
		}
	}

	// The holder's own token reconnecting replaces the old connection.
	reconnected := dialRawExecutor(t, ctx, addr, rawAlpha, victimID)
	waitFor(t, "the old connection to be replaced", func() bool {
		cur := srv.hostByID(victimID)
		return victimConn.closed.Load() && cur != nil && cur != victimConn
	})

	// Once the holder has gone, the id is free.
	_ = reconnected.m.Close()
	waitFor(t, "the reconnected executor to unregister", func() bool { return srv.hostByID(victimID) == nil })
	if _, refusal := helloRawExecutor(t, ctx, addr, rawBeta, victimID); refusal != "" {
		t.Fatalf("claim on a free instance id refused: %s", refusal)
	}
}

func (s *Server) hostByID(id string) *hostConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[id]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
