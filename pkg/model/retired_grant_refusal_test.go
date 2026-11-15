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

package model

import (
	"context"
	"strings"
	"testing"
)

func TestRetiredGrantRefusalNamesWhatTheNameBecame(t *testing.T) {
	msg, became, ok := RetiredGrantRefusal(" self-debug ")
	if !ok || len(became) != 2 || !strings.Contains(msg, "self-debug is a retired capability name") ||
		!strings.Contains(msg, "https://grain.dev/capabilities/api#self-debug") || !strings.Contains(msg, GrainSourceGrant) {
		t.Errorf("self-debug: %q %q %v", msg, became, ok)
	}
	for _, name := range []string{"https://grain.dev/capabilities/api#self-debug", "local-scripts:deploy", "no-such-capability", ""} {
		if msg, _, ok := RetiredGrantRefusal(name); ok {
			t.Errorf("%q is not retired, yet refused as %q", name, msg)
		}
	}
}

// A task row written while the bare names still granted (or by hand) is
// refused at dispatch -- the live walkthrough parks such a task on the
// refusal -- and the refusal says what to grant instead.
func TestResolveGrantsRefusesARetiredBareRowNamingWhatItBecame(t *testing.T) {
	reg := NewCapabilityRegistry(grantCapability{})
	resolved, err := ResolveGrants(context.Background(), reg, capContext(capTask("bootstrap-playbooks", "test-grant")))
	if err != nil {
		t.Fatal(err)
	}
	var reason string
	for _, r := range resolved {
		if r.Grant.Capability == "bootstrap-playbooks" {
			reason = r.Resolution.Reason
			if !r.Resolution.Refused {
				t.Errorf("bootstrap-playbooks was not refused: %+v", r)
			}
		}
	}
	if !strings.Contains(reason, `no provider is registered for capability "bootstrap-playbooks"`) ||
		!strings.Contains(reason, RetiredGrantsBecame("bootstrap-playbooks")[0]) {
		t.Errorf("reason = %q, want the refusal to name the config-map grant it became", reason)
	}
}
