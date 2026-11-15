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

package infrasig

import (
	"strings"
	"testing"
)

func TestOutputReadsTheRunnersNetworkAndOnlyTheTail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out, want string
	}{
		{"apt on the resolver", "E: Failed to fetch x  Temporary failure resolving 'security.ubuntu.com'", "temporary failure resolving"},
		{"curl", "curl: (6) Could not resolve host: registry.example", "could not resolve host"},
		{"getaddrinfo", "wget: unable to resolve host address: Name or service not known", "name or service not known"},
		{"a 404 from the mirror", "E: Failed to fetch x  404  Not Found\nE: Unable to fetch some archives", ""},
		{"a test failure", "--- FAIL: TestX: got 503", ""},
		{"a signature outside the tail", "Temporary failure resolving 'x'\n" + strings.Repeat("y", 2*StepTail), ""},
	} {
		if got := Output(false, tc.out); got != tc.want {
			t.Errorf("Output(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Output(false, "The requested URL returned error: 502"); got != "" {
		t.Errorf("git's 5xx read out of a non-checkout step: %q", got)
	}
	if got := Output(true, "The requested URL returned error: 502"); got == "" {
		t.Error("git's 5xx not read out of a checkout step")
	}
}

// Only the failing step of a job log is evidence.
func TestJobLogReadsOnlyTheFailingStep(t *testing.T) {
	t.Parallel()
	log := "##[group]Run sudo apt-get install -y expect\n##[endgroup]\n" +
		"W: Temporary failure resolving 'security.ubuntu.com'\n" +
		"##[group]Run go test ./...\n##[endgroup]\n--- FAIL: TestX\n" +
		"##[error]Process completed with exit code 1.\n"
	if got := JobLog(log); got != "" {
		t.Fatalf("JobLog = %q for a resolver warning in a step that passed", got)
	}
	log = "##[group]Run go test ./...\n##[endgroup]\nok\n" +
		"##[group]Run sudo apt-get install -y expect\n##[endgroup]\n" +
		"E: Temporary failure resolving 'security.ubuntu.com'\n" +
		"##[error]Process completed with exit code 100.\nPost job cleanup.\n"
	if got := JobLog(log); got != "temporary failure resolving" {
		t.Fatalf("JobLog = %q, want the failing step's resolver failure", got)
	}
}
