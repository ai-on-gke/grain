package modelexec

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/tunnel/mux"
)

// rawExecutor is a client that holds an executor token and speaks the
// executor protocol by hand, choosing its own hello -- what anybody with a
// token can do without running `grain executor`.
type rawExecutor struct {
	m *mux.Mux
}

func dialRawExecutor(t *testing.T, ctx context.Context, addr, token, instanceID string) *rawExecutor {
	t.Helper()
	r, refusal := helloRawExecutor(t, ctx, addr, token, instanceID)
	if refusal != "" {
		t.Fatalf("welcome refused: %s", refusal)
	}
	return r
}

// helloRawExecutor sends a hand-written hello and returns the connection
// and the welcome frame's error, empty when grain accepted it.
func helloRawExecutor(t *testing.T, ctx context.Context, addr, token, instanceID string) (*rawExecutor, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	m := mux.NewMux(conn, mux.Config{PingInterval: time.Hour, PingTimeout: time.Hour})
	t.Cleanup(func() { _ = m.Close() })
	ctrl, err := m.OpenStream(ctx)
	if err != nil {
		t.Fatalf("control stream: %v", err)
	}
	c := newCodec(ctrl)
	if err := c.write(frame{
		Type:            frameHello,
		ProtocolVersion: ProtocolVersion,
		Token:           token,
		Host:            "raw",
		InstanceID:      instanceID,
		Binaries:        map[string]string{"agy": "/bin/sh", "grain": "/bin/sh"},
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	welcome, err := c.read(5 * time.Second)
	if err != nil || welcome.Type != frameWelcome {
		t.Fatalf("welcome = %+v, %v", welcome, err)
	}
	return &rawExecutor{m: m}, welcome.Error
}

// openMCP opens an MCP tunnel stream with args and reports whether grain
// served it: a served stream answers with what ServeMCP writes, a refused
// one is closed without a byte.
func (r *rawExecutor) openMCP(t *testing.T, ctx context.Context, args []string) bool {
	t.Helper()
	s, err := r.m.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open MCP stream: %v", err)
	}
	defer s.Close()
	if err := json.NewEncoder(s).Encode(StreamHeader{Type: StreamTypeMCP, Args: args}); err != nil {
		t.Fatalf("MCP header: %v", err)
	}
	_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	n, _ := s.Read(b[:])
	return n > 0
}

// TestMCPGrantsDoNotPassToAnotherConnectionClaimingTheSameID is a client
// holding a different team's executor token that says, in its hello, that
// it is the instance a run was dispatched to. The instance id is the
// executor's own word, and visible to anyone who can list executors, so
// the grants issued to the run's connection must not follow the id to it:
// otherwise replaying the run's argv gets an mcpserver for another team's
// run, with that run's -task, -data-dir and credentials.
func TestMCPGrantsDoNotPassToAnotherConnectionClaimingTheSameID(t *testing.T) {
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
	rawAlpha, tokAlpha, err := model.GenerateExecutorToken("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateExecutorToken(ctx, tokAlpha); err != nil {
		t.Fatal(err)
	}
	rawBeta, tokBeta, err := model.GenerateExecutorToken("beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateExecutorToken(ctx, tokBeta); err != nil {
		t.Fatal(err)
	}

	served := make(chan []string, 4)
	srv, err := NewServer(Config{
		Store: st,
		Wait:  5 * time.Second,
		ServeMCP: func(rwc io.ReadWriteCloser, args []string) error {
			defer rwc.Close()
			served <- args
			_, err := rwc.Write([]byte("served\n"))
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, ln) }()

	const victimID = "alpha-node-1"
	victim := dialRawExecutor(t, ctx, ln.Addr().String(), rawAlpha, victimID)
	host, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host: %v", err)
	}
	if host.ID() != victimID {
		t.Fatalf("host id = %q, want %q", host.ID(), victimID)
	}
	issued := []string{"mcpserver", "-task", "41", "-data-dir", "/var/lib/grain/data"}
	host.AuthorizeMCPArgs(issued)

	// The run's own connection is served: the grant works at all.
	if !victim.openMCP(t, ctx, issued[1:]) {
		t.Fatal("the run's own executor was refused the argv grain issued it")
	}
	<-served

	// The claim itself is refused now (TestExecutorTokenCannotEvictAnotherTokensInstance);
	// whether or not it is, the grant must not follow the id.
	attacker, refusal := helloRawExecutor(t, ctx, ln.Addr().String(), rawBeta, victimID)
	if refusal == "" && attacker.openMCP(t, ctx, issued[1:]) {
		t.Fatalf("a team-beta token claiming instance %q was served team-alpha's run argv %q", victimID, <-served)
	}
}
