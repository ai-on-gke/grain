package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTunnelMCPServerWaitsForTheAnswerAfterStdinEnds is `echo req | grain
// mcpserver ...` on a remote executor: stdin is at EOF before grain has
// answered. The request still reaches grain and is acted on, so the answer
// must still come back on stdout rather than being dropped because the
// stdin side finished first.
func TestTunnelMCPServerWaitsForTheAnswerAfterStdinEnds(t *testing.T) {
	dir, err := os.MkdirTemp("", "mcptun")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "mcp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		var args []string
		if err := json.NewDecoder(strings.NewReader(mustReadLine(r))).Decode(&args); err != nil {
			return
		}
		req := mustReadLine(r)
		// grain answers some time after the client's stdin has ended.
		time.Sleep(200 * time.Millisecond)
		_, _ = conn.Write([]byte(`{"answer":` + strings.TrimSpace(req) + "}\n"))
	}()

	var out bytes.Buffer
	in := strings.NewReader(`{"id":1}` + "\n")
	if err := tunnelMCPServerIO(sock, []string{"-task", "1"}, in, &out); err != nil {
		t.Fatalf("tunnelMCPServerIO: %v", err)
	}
	if got, want := out.String(), `{"answer":{"id":1}}`+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func mustReadLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return line
}
