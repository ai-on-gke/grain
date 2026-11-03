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

package intree

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// uptick stands in for an edition's non-capability entry.
var uptick = hostd.BuiltinEntry{Name: "grain-uptick", Args: []string{"uptick-verify"}, MaxDuration: time.Minute}

func TestARegisteredHostdEntryIsAmongTheBuiltins(t *testing.T) {
	t.Cleanup(SwapHostdEntriesForTest(nil))
	before := HostdBuiltins()
	RegisterHostdEntry(uptick)
	after := HostdBuiltins()
	if len(after) != len(before)+1 || !reflect.DeepEqual(after[len(after)-1], uptick) {
		t.Fatalf("HostdBuiltins = %+v, want %+v then %+v", after, before, uptick)
	}
	b, ok := hostd.Builtin("grain-uptick")
	if !ok || b.MaxDuration != time.Minute {
		t.Errorf("hostd.Builtin(grain-uptick) = %+v, %v", b, ok)
	}
	if help := hostd.MissingEntryHelp("grain-uptick"); !strings.Contains(help, "args: [uptick-verify]") || !strings.Contains(help, "max_duration: 1m\n") {
		t.Errorf("MissingEntryHelp lacks the entry's text:\n%s", help)
	}
}

func panics(f func()) (p bool) {
	defer func() { p = recover() != nil }()
	f()
	return false
}

func TestRegisterHostdEntryRejectsDuplicatesAndClashes(t *testing.T) {
	t.Cleanup(SwapHostdEntriesForTest(nil))
	RegisterHostdEntry(uptick)
	if !panics(func() { RegisterHostdEntry(hostd.BuiltinEntry{Name: "grain-uptick", Args: []string{"other"}}) }) {
		t.Error("a second grain-uptick was registered")
	}
	for _, bad := range []hostd.BuiltinEntry{{Args: []string{"x"}}, {Name: "no-args"}, {Name: "neg", Args: []string{"x"}, MaxDuration: -time.Second}} {
		if !panics(func() { RegisterHostdEntry(bad) }) {
			t.Errorf("%+v was registered", bad)
		}
	}
	// A name a capability server's entry has, whenever that server
	// registered.
	for _, name := range []string{"host-git", HostEntry("gcp")} {
		restore := SwapHostdEntriesForTest([]hostd.BuiltinEntry{{Name: name, Args: []string{"x"}}})
		if !panics(func() { HostdBuiltins() }) {
			t.Errorf("HostdBuiltins accepted a registered %s", name)
		}
		restore()
	}
}
