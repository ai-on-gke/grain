package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedHTTPChild(t *testing.T) {
	if os.Getenv("GRAIN_HTTP_TEST_CHILD") != "1" {
		return
	}
	registry := NewRegistry()
	registry.Register(Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}, Handler: func(_ context.Context, args map[string]any) Result { return Result{Text: args["text"].(string)} }})
	_ = Serve(context.Background(), registry, bufio.NewReader(os.Stdin), bufio.NewWriter(os.Stdout))
	os.Exit(0)
}

func TestManagedHTTPRunLifecycle(t *testing.T) {
	t.Setenv("GRAIN_HTTP_TEST_CHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := StartCommandHTTP(ctx, os.Args[0], []string{"-test.run=^TestManagedHTTPChild$"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Tools) != 1 || s.Tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", s.Tools)
	}
	request, _ := http.NewRequest("POST", s.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"through-grain"}}}`))
	request.Header.Set("Authorization", "Bearer "+s.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), "through-grain") {
		t.Fatalf("response %d: %s", response.StatusCode, body)
	}
	cancel()
	s.Close() // waits for the child and closes the listener; idempotent
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(s.URL); err == nil {
		response.Body.Close()
		t.Fatal("endpoint survived cancellation")
	}
}

func TestManagedHTTPStartupFailureAndCancellation(t *testing.T) {
	if s, err := StartCommandHTTP(context.Background(), "/nonexistent/grain", nil); err == nil {
		s.Close()
		t.Fatal("missing child accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := StartCommandHTTP(ctx, os.Args[0], nil); err == nil {
		s.Close()
		t.Fatal("cancelled startup accepted")
	}
}

func TestManagedHTTPKeepsConcurrentToolCallsApart(t *testing.T) {
	t.Setenv("GRAIN_HTTP_TEST_CHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := StartCommandHTTP(ctx, os.Args[0], []string{"-test.run=^TestManagedHTTPChild$"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("call-%02d", i)
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echo","arguments":{"text":%q}}}`, i+1, want)
			request, err := http.NewRequest("POST", s.URL, strings.NewReader(body))
			if err != nil {
				t.Errorf("%s: building the request: %v", want, err)
				return
			}
			request.Header.Set("Authorization", "Bearer "+s.Token)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Errorf("%s: %v", want, err)
				return
			}
			answer, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("%s: response %d: %s", want, response.StatusCode, answer)
				return
			}
			if !strings.Contains(string(answer), want) {
				t.Errorf("%s got another call's answer: %s", want, answer)
			}
		}()
	}
	wg.Wait()
}

func TestHTTPBoundary(t *testing.T) {
	registry := NewRegistry()
	called := false
	registry.Register(Tool{Name: "probe", Handler: func(context.Context, map[string]any) Result { called = true; return Result{Text: "ok"} }})
	handler := streamableHTTPHandler(registry, "run-a-token", "127.0.0.1:1234")
	for _, tc := range []struct {
		name, token, origin, host, method, body string
		status                                  int
	}{
		{"missing token", "", "", "127.0.0.1:1234", "POST", `{}`, 401},
		{"cross run", "run-b-token", "", "127.0.0.1:1234", "POST", `{}`, 401},
		{"origin", "run-a-token", "https://evil.test", "127.0.0.1:1234", "POST", `{}`, 403},
		{"rebind", "run-a-token", "", "evil.test", "POST", `{}`, 403},
		{"SSE optional", "run-a-token", "", "127.0.0.1:1234", "GET", `{}`, 405},
		{"invalid", "run-a-token", "", "127.0.0.1:1234", "POST", `{`, 400},
		{"notification cannot call tool", "run-a-token", "", "127.0.0.1:1234", "POST", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"probe"}}`, 202},
		{"initialize", "run-a-token", "", "127.0.0.1:1234", "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://"+tc.host+"/mcp", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+tc.token)
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if called {
				t.Fatal("boundary request executed a tool")
			}
			if tc.name == "initialize" {
				var rpc struct {
					Result map[string]any `json:"result"`
				}
				if json.Unmarshal(response.Body.Bytes(), &rpc) != nil || rpc.Result["protocolVersion"] != "2025-03-26" {
					t.Fatalf("bad handshake: %s", response.Body.String())
				}
			}
		})
	}
}

// The rest of the surface an authenticated request can still be refused
// on. This endpoint is one CLI's JSON-RPC surface rather than a server
// anything is meant to find its way around: one path, one content type,
// and a body small enough that reading it is not itself the attack.
func TestHTTPRequestsThatGetPastTheTokenAreStillBounded(t *testing.T) {
	registry := NewRegistry()
	called := false
	registry.Register(Tool{Name: "probe", Handler: func(context.Context, map[string]any) Result { called = true; return Result{Text: "ok"} }})
	handler := streamableHTTPHandler(registry, "run-a-token", "127.0.0.1:1234")
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe"}}`
	for _, tc := range []struct {
		name, path, contentType, body string
		status                        int
	}{
		{"another path", "/", "application/json", call, 404},
		{"a path below this one", "/mcp/tools", "application/json", call, 404},
		{"no content type", "/mcp", "", call, 415},
		{"some other content type", "/mcp", "text/plain", call, 415},
		// The charset a client may add to the one type this accepts is
		// not a different type.
		{"json with a charset", "/mcp", "application/json; charset=utf-8", call, 200},
		{"a body larger than the cap", "/mcp", "application/json", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe","arguments":{"text":"` + strings.Repeat("x", 5<<20) + `"}}}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			request := httptest.NewRequest("POST", "http://127.0.0.1:1234"+tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer run-a-token")
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if called != (tc.status == 200) {
				t.Errorf("the tool ran = %v for a %d", called, response.Code)
			}
		})
	}
}
