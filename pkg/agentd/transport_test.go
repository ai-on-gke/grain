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

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// pipeTransport is a transport for *.pipe.test URLs whose connections are
// one end of a net.Pipe with an echo on the other, so a test can see a
// dial went through it. It records what it was told.
type pipeTransport struct {
	mu       sync.Mutex
	via      string
	dialCfg  DialConfig
	prepared InstallConfig
	dials    int
	closed   int
}

func (p *pipeTransport) transport() Transport {
	return Transport{
		Name:  "pipe",
		Match: func(u *url.URL) bool { return strings.HasSuffix(u.Hostname(), ".pipe.test") },
		Flags: func(fs *flag.FlagSet) { fs.StringVar(&p.via, "pipe-via", "", "the pipe helper") },
		Dial: func(ctx context.Context, cfg DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
			p.mu.Lock()
			p.dialCfg = cfg
			p.mu.Unlock()
			dial := func(context.Context) (net.Conn, error) {
				p.mu.Lock()
				p.dials++
				p.mu.Unlock()
				a, b := net.Pipe()
				go func() {
					defer b.Close()
					line, _ := bufio.NewReader(b).ReadString('\n')
					io.WriteString(b, "echo "+line)
				}()
				return a, nil
			}
			return dial, func() { p.mu.Lock(); p.closed++; p.mu.Unlock() }, nil
		},
		Prepare: func(ctx context.Context, cfg InstallConfig) ([]string, error) {
			p.prepared = cfg
			return []string{"-pipe-via", "/opt/pipe-helper"}, nil
		},
	}
}

func registerPipe(t *testing.T) *pipeTransport {
	t.Helper()
	p := &pipeTransport{}
	t.Cleanup(RegisterTransport(p.transport()))
	return p
}

func TestTransportIsUsedForTheURLsItMatches(t *testing.T) {
	p := registerPipe(t)
	cfg := DialConfig{Server: mustURL(t, "https://daemon.pipe.test"), Token: "ghd_t", Name: "n", Host: "h", Build: "b"}
	dial, closeFn, err := dialerFor(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "ping\n")
	if got, _ := bufio.NewReader(conn).ReadString('\n'); got != "echo ping\n" {
		t.Fatalf("over the transport's connection got %q", got)
	}
	closeFn()
	if p.dials != 1 || p.closed != 1 || p.dialCfg.Token != "ghd_t" || p.dialCfg.Server.Host != "daemon.pipe.test" {
		t.Errorf("transport saw dials=%d closed=%d cfg=%+v", p.dials, p.closed, p.dialCfg)
	}
	if transportFor(mustURL(t, "https://grain.example.com")) != nil {
		t.Error("a URL the transport does not match was given to it")
	}
}

// TestUnmatchedURLsAreDialedDirectly: with a transport registered, a URL
// it does not match still goes straight to the daemon, carrying the hostd
// token as hostd.Dialer sends it.
func TestUnmatchedURLsAreDialedDirectly(t *testing.T) {
	registerPipe(t)
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get(hostd.TokenHeader):
		default:
		}
		http.Error(w, "grain hostd: this controller does not take that hostd token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	dial, closeFn, err := dialerFor(context.Background(), DialConfig{Server: mustURL(t, srv.URL), Token: "ghd_direct", Host: "h", Build: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if conn, err := dial(ctx); err == nil {
		conn.Close()
		t.Fatal("dial against a refusing daemon succeeded")
	}
	select {
	case token := <-got:
		if token != "ghd_direct" {
			t.Errorf("the daemon was sent token %q, want ghd_direct", token)
		}
	default:
		t.Error("the daemon was never dialed")
	}
}

func TestTransportDialErrorNamesTheTransport(t *testing.T) {
	t.Cleanup(RegisterTransport(Transport{
		Name:  "broken",
		Match: func(u *url.URL) bool { return u.Hostname() == "broken.test" },
		Dial: func(context.Context, DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
			return nil, nil, errors.New("helper missing")
		},
	}))
	_, _, err := dialerFor(context.Background(), DialConfig{Server: mustURL(t, "https://broken.test")})
	if err == nil || err.Error() != "broken: helper missing" {
		t.Fatalf("dialerFor = %v, want the transport's error under its name", err)
	}
}

func TestTransportsAreTriedInOrderAndUnregister(t *testing.T) {
	first := RegisterTransport(Transport{Name: "first", Match: func(*url.URL) bool { return true },
		Dial: func(context.Context, DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
			return nil, nil, nil
		}})
	second := RegisterTransport(Transport{Name: "second", Match: func(*url.URL) bool { return true },
		Dial: func(context.Context, DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
			return nil, nil, nil
		}})
	defer second()
	u := mustURL(t, "https://any.test")
	if got := transportFor(u); got == nil || got.Name != "first" {
		t.Fatalf("transportFor = %v, want first", got)
	}
	first()
	if got := transportFor(u); got == nil || got.Name != "second" {
		t.Fatalf("after unregistering first, transportFor = %v, want second", got)
	}
}

func TestRegisterTransportRefusesAnIncompleteOne(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterTransport with no Dial did not panic")
		}
	}()
	RegisterTransport(Transport{Name: "x", Match: func(*url.URL) bool { return true }})
}

// TestTransportFlagsAndPrepareReachTheUnit: a transport's flags parse on
// grain-agentd's own command line, and what its Prepare returns is on the
// installed unit's.
func TestTransportFlagsAndPrepareReachTheUnit(t *testing.T) {
	p := registerPipe(t)
	h := newFakeHome(t)
	var out, errOut bytes.Buffer
	if code := install([]string{"-server", "https://daemon.pipe.test", "-no-start"}, &out, &errOut); code != 0 {
		t.Fatalf("install = %d: %s", code, errOut.String())
	}
	if p.prepared.Server == nil || p.prepared.Server.Host != "daemon.pipe.test" || p.prepared.Home != h.home {
		t.Errorf("Prepare was told %+v", p.prepared)
	}
	unit, err := os.ReadFile(filepath.Join(pathsFor(h.home).UnitDir, UnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), " -pipe-via /opt/pipe-helper") {
		t.Errorf("unit lacks the transport's flags:\n%s", unit)
	}
	if !strings.Contains(out.String(), "prepared pipe for daemon.pipe.test") {
		t.Errorf("install output lacks the transport's step:\n%s", out.String())
	}

	// The same flag parses on grain-agentd's own command line.
	dir := t.TempDir()
	allow := filepath.Join(dir, "agentd.yaml")
	writeFile(t, allow, renderTestAllowlist(t, dir, "https://daemon.pipe.test", "/bin/true"), 0o644)
	out.Reset()
	errOut.Reset()
	if code := run([]string{"-allowlist", allow, "-check", "-pipe-via", "/opt/other"}, &out, &errOut); code != 0 {
		t.Fatalf("run with the transport's flag = %d: %s", code, errOut.String())
	}
	if p.via != "/opt/other" {
		t.Errorf("-pipe-via = %q, want /opt/other", p.via)
	}
}
