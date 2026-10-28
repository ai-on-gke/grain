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

package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `grain capability <name>` serves an in-tree server; the task command
// `grain capability <id> <capability-id> attach|detach` keeps its meaning.
func TestCapabilityServesOnlyInTreeNames(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"gcp", "-project", "p"}, true},
		{[]string{"skill"}, true},
		{[]string{"git", "-listen", "127.0.0.1:0"}, true},
		{[]string{"123", "gcp-key", "attach"}, false},
		{[]string{"skill", "gcp-key", "detach"}, false},
		{[]string{"nonesuch"}, false},
		{nil, false},
	} {
		if got := servesCapability(c.args); got != c.want {
			t.Errorf("servesCapability(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestServeCapabilityAnswersTheControlPlaneOnStdio(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review", "SKILL.md"), []byte("# Reviewing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, toServer := io.Pipe()
	fromServer, out := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveCapability(context.Background(), []string{"skill", "-dir", dir}, func(string) string { return "" }, in, out, io.Discard)
		out.Close()
	}()
	r := bufio.NewReader(fromServer)
	ask := func(line string) string {
		t.Helper()
		if _, err := io.WriteString(toServer, line+"\n"); err != nil {
			t.Fatal(err)
		}
		got, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ask(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); !strings.Contains(got, `"name":"skill"`) {
		t.Errorf("initialize = %s", got)
	}
	if got := ask(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); !strings.Contains(got, `"mint_skill"`) || strings.Contains(got, "release_skill") {
		t.Errorf("tools/list = %s", got)
	}
	toServer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeCapabilityRefusesUnknownNames(t *testing.T) {
	err := serveCapability(context.Background(), []string{"nonesuch"}, func(string) string { return "" }, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "gcp") {
		t.Errorf("err = %v, want one listing the in-tree servers", err)
	}
}
