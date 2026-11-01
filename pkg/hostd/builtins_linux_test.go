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

package hostd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gchatBuiltin is a built-in entry an upgrade adds: one a host installed
// before it existed has no allowlist entry for.
var gchatBuiltin = BuiltinEntry{Name: "grain-gchat", Args: []string{"capability", "gchat"}, ExtraArgs: true}

// fakeGrain writes a stand-in for hostd's own binary, mode mode, that
// prints its arguments.
func fakeGrain(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "versions", "new", "grain")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"new grain $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasCapability(offers []Offer, name string) (Offer, bool) {
	for _, o := range offers {
		if o.Kind == KindCapability && o.Name == name {
			return o, true
		}
	}
	return Offer{}, false
}

// An upgrade that adds a built-in host server: hostd on the new build
// offers its entry, run as hostd's own binary, before anything has written
// it into the allowlist -- and runs it.
func TestHostdOffersItsBuildsBuiltinsTheAllowlistLacks(t *testing.T) {
	var self string
	ts := startServer(t, "", func(s *Server) {
		self = fakeGrain(t, filepath.Dir(s.Allowlist), 0o755)
		s.Builtins, s.BuiltinCommand = []BuiltinEntry{gchatBuiltin}, self
	})
	offers, err := ts.client.Offers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o, ok := hasCapability(offers, "grain-gchat")
	if !ok || o.Path != self {
		t.Fatalf("offers = %+v, want grain-gchat as %s", offers, self)
	}
	var out bytes.Buffer
	exit, err := ts.client.Run(context.Background(), Request{Kind: KindCapability, Name: "grain-gchat", Args: []string{"-listen", "127.0.0.1:0"}},
		strings.NewReader(""), &out, &bytes.Buffer{})
	if err != nil || exit.Code != 0 {
		t.Fatalf("Run = %+v, %v", exit, err)
	}
	if got := strings.TrimSpace(out.String()); got != "new grain capability gchat -listen 127.0.0.1:0" {
		t.Errorf("ran %q", got)
	}
	if !strings.Contains(ts.logs.String(), "offering grain's own entries grain-gchat") {
		t.Errorf("logs do not say so:\n%s", ts.logs.String())
	}
}

// The operator's entry of that name is the one hostd runs.
func TestHostdNeverReplacesTheAllowlistsOwnEntryWithABuiltin(t *testing.T) {
	ts := startServer(t, "capabilities:\n  grain-gchat:\n    command: /bin/true\n", func(s *Server) {
		s.Builtins, s.BuiltinCommand = []BuiltinEntry{gchatBuiltin}, fakeGrain(t, filepath.Dir(s.Allowlist), 0o755)
	})
	offers, err := ts.client.Offers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := hasCapability(offers, "grain-gchat"); !ok || o.Path != "/bin/true" {
		t.Errorf("offers = %+v, want the allowlist's own grain-gchat", offers)
	}
}

// A binary someone else could have rewritten is never run for an entry
// the allowlist does not name itself.
func TestHostdOffersNoBuiltinsWhenItsBinaryIsWritableByOthers(t *testing.T) {
	ts := startServer(t, "", func(s *Server) {
		s.Builtins, s.BuiltinCommand = []BuiltinEntry{gchatBuiltin}, fakeGrain(t, filepath.Dir(s.Allowlist), 0o777)
	})
	offers, err := ts.client.Offers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hasCapability(offers, "grain-gchat"); ok {
		t.Errorf("offers = %+v, want no grain-gchat", offers)
	}
	_, err = ts.client.Run(context.Background(), Request{Kind: KindCapability, Name: "grain-gchat"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	var r *RefusedError
	if !errors.As(err, &r) {
		t.Errorf("Run err = %v, want refused", err)
	}
	if !strings.Contains(ts.logs.String(), "not grain's built-in ones") {
		t.Errorf("logs do not say why:\n%s", ts.logs.String())
	}
	// The remedy keeps the binary executable: "make it 0644" did not.
	if logs := ts.logs.String(); !strings.Contains(logs, "chmod go-w") || strings.Contains(logs, "0644") {
		t.Errorf("logs do not say to remove group and other write, and only that:\n%s", logs)
	}
}
