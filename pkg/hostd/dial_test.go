//go:build unix

package hostd

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A request made while hostd restarts -- its socket gone for a moment --
// waits for the new one rather than failing.
func TestDialWaitsOutARestartingHostd(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hostd.sock")
	ready := make(chan net.Listener, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
		}
		ready <- l
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Client{Socket: sock}.dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	(<-ready).Close()
}

// One that stays down is said plainly, with where to look.
func TestDialSaysHostdIsDown(t *testing.T) {
	old := dialRetry
	dialRetry = 200 * time.Millisecond
	defer func() { dialRetry = old }()
	_, err := Client{Socket: filepath.Join(t.TempDir(), "hostd.sock")}.dial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grain hostd is not running") || !strings.Contains(err.Error(), "systemctl status grain-hostd") {
		t.Fatalf("err = %v", err)
	}
}
