package guestverify

import (
	"strings"
	"testing"
)

func TestParseChecks(t *testing.T) {
	got, err := ParseChecks(`# the checks grain's own guest has to pass
# before a sandbox is started on it.

[docker starts and answers]
systemctl start docker
docker info >/dev/null

[the guest has a default route]
ip route show default | grep -q via   # a trailing comment is shell
`)
	if err != nil {
		t.Fatalf("ParseChecks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d checks, want 2: %+v", len(got), got)
	}
	if got[0].Name != "docker starts and answers" {
		t.Errorf("first check is named %q", got[0].Name)
	}
	if want := "systemctl start docker\ndocker info >/dev/null\n"; got[0].Script != want {
		t.Errorf("first check's script = %q, want %q", got[0].Script, want)
	}
	// A "#" inside a check is shell, where it is also a comment: taking
	// it out here would change a script's own text under it.
	if !strings.Contains(got[1].Script, "# a trailing comment is shell") {
		t.Errorf("second check's script lost its comment: %q", got[1].Script)
	}
}

func TestParseChecksRejects(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"nothing at all", "\n# just a comment\n", "no checks"},
		{"a script before any name", "docker info\n", "comes before any check"},
		{"an empty name", "[]\ntrue\n", "must not be empty"},
		// A check that asserts nothing passes whatever the guest did,
		// which is worse than not having written it: it reads like
		// coverage.
		{"a name with no script", "[docker works]\n\n[and so does the clock]\ntrue\n", "nothing to run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseChecks(tc.text)
			if err == nil {
				t.Fatalf("ParseChecks accepted %q", tc.text)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Every line of a check has to work, not just its last one: without
// "set -e" the first line failing is silent, which is the exact shape of
// failure verify exists to catch.
func TestCheckShellStopsAtTheFirstFailingLine(t *testing.T) {
	c := Check{Name: "docker starts", Script: "systemctl start docker\ndocker info\n"}
	if !strings.HasPrefix(c.shell(), "#!/bin/sh\nset -eu\n") {
		t.Errorf("shell() = %q, want /bin/sh with set -eu in front of it", c.shell())
	}
	// A check that brings its own interpreter is left alone: it may not
	// be shell at all.
	python := Check{Name: "the agent imports", Script: "#!/usr/bin/env python3\nimport json\n"}
	if python.shell() != python.Script {
		t.Errorf("shell() = %q, want a script with its own shebang left unchanged", python.shell())
	}
}

func TestParseChecks_WhitespaceAndMultiple(t *testing.T) {
	input := `
	# Leading comments and empty lines
	
	[   check with leading/trailing spaces   ]
	echo "hello"
	
	[second check]
	mkdir -p /tmp/test
	rm -rf /tmp/test
	`
	checks, err := ParseChecks(input)
	if err != nil {
		t.Fatalf("ParseChecks failed: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}
	if checks[0].Name != "check with leading/trailing spaces" {
		t.Errorf("checks[0].Name = %q, want trimmed name", checks[0].Name)
	}
	if checks[1].Name != "second check" {
		t.Errorf("checks[1].Name = %q", checks[1].Name)
	}
}
