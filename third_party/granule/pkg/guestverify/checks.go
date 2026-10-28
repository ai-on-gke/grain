package guestverify

import (
	"fmt"
	"strings"
)

// Check is one assertion about a booted guest: a claim in Name, and the
// shell that either holds it up or does not.
type Check struct {
	// Name is what is being claimed, in a reader's words. It is what a
	// verdict is printed as, so "docker starts and answers" beats
	// "check3": the report is read by somebody who did not write the
	// check and is deciding whether an image may go into service.
	Name string

	// Script runs as root in the booted guest. It is a script, not a
	// command: multi-line, with its own shebang if it wants one, and
	// otherwise run under /bin/sh with "set -eu" in front of it, so that
	// the first line that fails fails the check rather than being
	// stepped over.
	Script string
}

// shell is Script as it is staged in the guest.
//
// A check that brings its own shebang is left exactly as written -- it
// may not be shell at all. Everything else is /bin/sh with "set -eu",
// because the shape a check comes in is a handful of lines each of which
// has to work:
//
//	systemctl start docker
//	docker info >/dev/null
//
// Without "set -e" the second line's verdict is the whole check's and
// the first line failing is silent, which is the exact failure mode
// verify exists to catch.
func (c Check) shell() string {
	if strings.HasPrefix(c.Script, "#!") {
		return c.Script
	}
	return "#!/bin/sh\nset -eu\n" + c.Script
}

// ParseChecks reads the check file format: a named check, then the shell
// that tests it, repeated.
//
//	# a comment, and blank lines, are ignored between checks
//	[docker starts and answers]
//	systemctl start docker
//	docker info >/dev/null
//
//	[the guest has a default route]
//	ip route show default | grep -q via
//
// The format is deliberately the smallest thing that can report which
// check failed. The alternatives considered were a single script, which
// this repo already has in `guest build -setup` and which can only ever
// report "the script failed" plus whatever the author remembered to echo;
// and a list of one-line commands, which cannot express the two-line case
// above without an && chain that hides which half of it broke. A real
// declarative format (YAML with a type per check) was rejected for a
// different reason: every check worth writing here is "run this in the
// guest and see", so a schema over it would be a schema with one member
// and a dependency to go with it.
//
// Naming a check is required rather than optional on purpose. The name is
// the sentence the verdict is reported as, and a report that says an
// image failed without saying what it failed to do is barely better than
// no report -- which is the failure this whole verb is designed against.
//
// Comments are only recognized outside a check's body: a "#" inside one
// is shell, where it is also a comment, and stripping it here would
// change a script's own text under it.
func ParseChecks(text string) ([]Check, error) {
	var (
		checks []Check
		cur    *Check
		body   strings.Builder
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Script = strings.TrimSpace(body.String()) + "\n"
		checks = append(checks, *cur)
		cur, body = nil, strings.Builder{}
	}

	for n, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if cur == nil && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if name, ok := header(trimmed); ok {
			flush()
			if name == "" {
				return nil, fmt.Errorf("line %d: a check's name must not be empty", n+1)
			}
			cur = &Check{Name: name}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: %q comes before any check; a check file opens with a name in brackets, like [docker starts and answers]", n+1, trimmed)
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()

	if len(checks) == 0 {
		return nil, fmt.Errorf("no checks: a check file is one or more [name] headers, each followed by the shell that tests it")
	}
	for _, c := range checks {
		if strings.TrimSpace(c.Script) == "" {
			return nil, fmt.Errorf("check %q has nothing to run; a check with no script asserts nothing and would pass whatever the guest did", c.Name)
		}
	}
	return checks, nil
}

// header recognizes a "[name]" line.
func header(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return "", false
	}
	return strings.TrimSpace(line[1 : len(line)-1]), true
}
