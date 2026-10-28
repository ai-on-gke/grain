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

package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The address the UI's bootstrap command points a worker at is the one
// the executor listener really bound -- the port ":0" was given, not the
// flag's own text -- and nothing at all when -executor-addr is unset,
// even though the server half still runs for HTTP-upgraded executors
// (grain/task-2396).
func TestStartExecutorServerReportsWhereItListens(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, addr, stop, err := startExecutorServer(Config{ExecutorAddr: "127.0.0.1:0", ExecutorTokenFile: tokenFile}, nil)
	if err != nil {
		t.Fatalf("startExecutorServer: %v", err)
	}
	defer stop(context.Background())
	if srv == nil {
		t.Fatal("server = nil, want one")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("addr = %q, want 127.0.0.1 and the port the listener actually got", addr)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing the reported address %s: %v", addr, err)
	}
	conn.Close()

	// Token only: the server runs (HTTP-upgraded executors still need
	// it), but no TCP listener does, so there is no address to offer.
	srv, addr, stop2, err := startExecutorServer(Config{ExecutorTokenFile: tokenFile}, nil)
	if err != nil {
		t.Fatalf("startExecutorServer without -executor-addr: %v", err)
	}
	defer stop2(context.Background())
	if srv == nil {
		t.Fatal("server = nil without -executor-addr, want one for HTTP-upgraded executors")
	}
	if addr != "" {
		t.Fatalf("addr = %q without -executor-addr, want empty", addr)
	}

	// Nothing configured at all: no server and no address.
	srv, addr, _, err = startExecutorServer(Config{}, nil)
	if err != nil || srv != nil || addr != "" {
		t.Fatalf("startExecutorServer(Config{}) = %v, %q, %v; want nil, \"\", nil", srv, addr, err)
	}
}
