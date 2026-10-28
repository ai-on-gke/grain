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

package upgrade

import (
	"strings"
	"testing"
)

func TestFlagsNotDefinedReadsWhatTheBuildAnswered(t *testing.T) {
	have := []string{"-data-dir", "max-workers", "-reboot-cmd"}
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{{
		name: "the two-column answer -flag-names actually prints",
		out:  "data-dir value\nmax-workers value\nui-addr value\n",
		want: "-reboot-cmd",
	}, {
		// A dash on this side is optional: the caller hands over the
		// flags as they were written on a command line, and the build
		// prints them as it defined them.
		name: "names alone, as a build predating the second column prints them",
		out:  "data-dir\nmax-workers\nreboot-cmd\n",
		want: "",
	}, {
		name: "a bool flag is still a flag",
		out:  "data-dir value\nmax-workers value\nreboot-cmd bool\n",
		want: "",
	}, {
		name: "every flag missing is every flag reported",
		out:  "ui-addr value\n",
		want: "-data-dir -max-workers -reboot-cmd",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := flagsNotDefined("example/grain:main", tc.out, have)
			if err != nil {
				t.Fatalf("flagsNotDefined: %v", err)
			}
			if got := strings.Join(missing, " "); got != tc.want {
				t.Errorf("missing = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFlagsNotDefinedRefusesAnAnswerThatIsNotOne(t *testing.T) {
	// The case this has to get right, because getting it wrong is a
	// reassuring lie rather than a false alarm: a build too old to know
	// -flag-names prints a usage error and its own flag dump, and under
	// Kubernetes that stderr arrives in the same stream as the answer
	// would have. None of those lines is the "name bool|value" a real
	// answer is made of, so there is nothing to compare against.
	for _, tc := range []struct {
		name string
		out  string
	}{{
		name: "nothing at all",
		out:  "",
	}, {
		name: "a usage dump",
		out: "flag provided but not defined: -flag-names\n" +
			"Usage of daemon:\n" +
			"  -data-dir string\n" +
			"    \twhere this deployment keeps its state (default \"/data\")\n" +
			"  -max-workers int\n",
	}, {
		name: "a log line the build wrote before failing",
		out:  "2026/09/19 09:00:00 grain daemon: starting\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := flagsNotDefined("example/grain:main", tc.out, []string{"-data-dir"})
			if err == nil {
				t.Fatalf("flagsNotDefined accepted %q, reporting %v", tc.out, missing)
			}
			if !strings.Contains(err.Error(), "example/grain:main") {
				t.Errorf("error = %q, want the image that failed to answer named", err)
			}
		})
	}
}
