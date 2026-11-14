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

package protocol

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

// A configure handler refusing with Waiting reaches grain, over the
// wire, as a ConfigureError wrapping a WaitingError that carries the
// server's phrase -- what has the registry retry it rather than wait for
// an edit (§4.5.5).
func TestAWaitingConfigureRefusalCrossesTheWire(t *testing.T) {
	srv, err := NewCapabilityServer("waits")
	if err != nil {
		t.Fatal(err)
	}
	srv.Token = strings.Repeat("t", MinHTTPTokenBytes)
	waiting := "a renewed\n  host credential"
	if err := srv.HandleConfigure(map[string]any{"type": "object", "properties": map[string]any{}}, func(context.Context, map[string]any) error {
		return Waiting(waiting, errors.New("the host credential expired at 09:12"))
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	cfg := ServerConfig{ServerURL: ts.URL, Headers: map[string]string{"Authorization": "Bearer " + srv.Token}}
	c, err := Dial(ctx, cfg, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := NewServer(ctx, "capabilities.example.com/waits", clientCaller{c})
	if err != nil {
		t.Fatal(err)
	}
	err = s.ApplyConfig(ctx, nil, nil)
	var ce *ConfigureError
	var we *WaitingError
	if !errors.As(err, &ce) || !errors.As(err, &we) {
		t.Fatalf("err = %v, want a ConfigureError wrapping a WaitingError", err)
	}
	if we.Waiting != "a renewed host credential" {
		t.Errorf("waiting = %q, want the phrase on one line", we.Waiting)
	}
	if !strings.Contains(err.Error(), "expired at 09:12") {
		t.Errorf("err = %v, want the server's own message kept", err)
	}

	// A plain refusal is not a wait.
	srv2, _ := NewCapabilityServer("refuses")
	srv2.Token = srv.Token
	_ = srv2.HandleConfigure(map[string]any{"type": "object", "properties": map[string]any{}}, func(context.Context, map[string]any) error {
		return errors.New("no such project")
	})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	cfg.ServerURL = ts2.URL
	c2, err := Dial(ctx, cfg, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	s2, err := NewServer(ctx, "capabilities.example.com/refuses", clientCaller{c2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.ApplyConfig(ctx, nil, nil); err == nil || errors.As(err, &we) {
		t.Errorf("plain refusal: err = %v, want an error that is not a WaitingError", err)
	}
}

// A configure handler failing to start its runtime (StartFailure or a
// net.Listen OpError) reaches grain over the wire as a ConfigureError for
// which IsStartError is true, while a plain refusal is not.
func TestAStartFailureConfigureErrorCrossesTheWire(t *testing.T) {
	if StartFailure(nil) != nil {
		t.Fatal("StartFailure(nil) != nil")
	}
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ctx := context.Background()
	check := func(name string, handler func(context.Context, map[string]any) error, wantStart bool) {
		t.Helper()
		srv, err := NewCapabilityServer(name)
		if err != nil {
			t.Fatal(err)
		}
		srv.Token = strings.Repeat("t", MinHTTPTokenBytes)
		if err := srv.HandleConfigure(map[string]any{"type": "object", "properties": map[string]any{}}, handler); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv)
		defer ts.Close()
		cfg := ServerConfig{ServerURL: ts.URL, Headers: map[string]string{"Authorization": "Bearer " + srv.Token}}
		c, err := Dial(ctx, cfg, DialOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		s, err := NewServer(ctx, "capabilities.example.com/"+name, clientCaller{c})
		if err != nil {
			t.Fatal(err)
		}
		err = s.ApplyConfig(ctx, nil, nil)
		var ce *ConfigureError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: err = %v, want a ConfigureError", name, err)
		}
		if got := IsStartError(err); got != wantStart {
			t.Errorf("%s: IsStartError(%v) = %v, want %v", name, err, got, wantStart)
		}
	}

	check("explicit-start-failure", func(context.Context, map[string]any) error {
		return StartFailure(errors.New("mkdir /readonly/data: read-only file system"))
	}, true)
	check("raw-listen-error", func(context.Context, map[string]any) error {
		_, err := net.Listen("tcp", held.Addr().String())
		return err
	}, true)
	check("plain-config-refusal", func(context.Context, map[string]any) error {
		return errors.New("no such project")
	}, false)
}
