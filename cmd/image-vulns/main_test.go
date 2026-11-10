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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Trivy report with one finding of each kind the gate tells apart.
const trivyJSON = `{
  "ArtifactName": "example.test/grain:sha-abc",
  "Results": [
    {"Target": "example.test/grain:sha-abc (debian 12.15)", "Class": "os-pkgs", "Type": "debian",
     "Vulnerabilities": [
       {"VulnerabilityID": "CVE-2026-0001", "PkgName": "libfoo", "InstalledVersion": "1.0", "FixedVersion": "1.1", "Severity": "CRITICAL"},
       {"VulnerabilityID": "CVE-2026-0002", "PkgName": "libbar", "InstalledVersion": "2.0", "Severity": "HIGH"},
       {"VulnerabilityID": "CVE-2026-0003", "PkgName": "libbaz", "InstalledVersion": "3.0", "FixedVersion": "3.1", "Severity": "MEDIUM"}
     ]},
    {"Target": "usr/local/bin/tool", "Class": "lang-pkgs", "Type": "gobinary",
     "Vulnerabilities": [
       {"VulnerabilityID": "CVE-2026-0004", "PkgName": "stdlib", "InstalledVersion": "v1.22.0", "FixedVersion": "1.25.0", "Severity": "HIGH"},
       {"VulnerabilityID": "CVE-2026-0004", "PkgName": "stdlib", "InstalledVersion": "v1.22.0", "FixedVersion": "1.25.0", "Severity": "HIGH"}
     ]}
  ]
}`

type fixture struct {
	dir string
}

func newFixture(t *testing.T, allowlist string) fixture {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("grain.json", trivyJSON)
	write("allow.yaml", allowlist)
	return fixture{dir: dir}
}

func (f fixture) run(t *testing.T, today string, extra ...string) (int, *Report, string) {
	t.Helper()
	args := append([]string{
		"-allowlist", filepath.Join(f.dir, "allow.yaml"),
		"-json", filepath.Join(f.dir, "out.json"),
		"-markdown", filepath.Join(f.dir, "summary.md"),
		"-today", today,
	}, extra...)
	args = append(args, "grain="+filepath.Join(f.dir, "grain.json"))
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, time.Now())
	var r Report
	if b, err := os.ReadFile(filepath.Join(f.dir, "out.json")); err == nil {
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
	}
	md, _ := os.ReadFile(filepath.Join(f.dir, "summary.md"))
	return code, &r, string(md) + stderr.String()
}

func statusOf(r *Report, id string) Status {
	for _, f := range r.Findings {
		if f.ID == id {
			return f.Status
		}
	}
	return ""
}

func TestOnlyAFixableCriticalOrHighFindingBlocks(t *testing.T) {
	code, r, out := newFixture(t, "").run(t, "2026-11-10")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for id, want := range map[string]Status{
		"CVE-2026-0001": StatusBlocking,  // critical, fixed version available
		"CVE-2026-0002": StatusUnfixable, // high, no fix yet: reported only
		"CVE-2026-0003": StatusReported,  // medium: reported only
		"CVE-2026-0004": StatusBlocking,  // a language package counts the same
	} {
		if got := statusOf(r, id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	if r.Counts.Blocking != 2 {
		t.Errorf("blocking = %d, want 2 (the duplicate stdlib finding counted once)", r.Counts.Blocking)
	}
	if !strings.Contains(out, "FAIL: 2 fixable") {
		t.Errorf("summary does not say it failed:\n%s", out)
	}
}

const allowBoth = `entries:
  - id: CVE-2026-0001
    package: libfoo
    image: grain
    target: os
    reason: no fixed build for our base yet
    owner: someone
    expires: "2026-11-30"
  - ids: [CVE-2026-0004]
    package: stdlib
    image: "*"
    target: usr/local/bin/tool
    reason: upstream rebuild pending
    owner: someone-else
    expires: "2026-11-15"
`

func TestAnUnexpiredAllowlistEntryWaivesAFinding(t *testing.T) {
	code, r, out := newFixture(t, allowBoth).run(t, "2026-11-10")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if got := statusOf(r, "CVE-2026-0001"); got != StatusAllowlisted {
		t.Errorf("CVE-2026-0001: %s, want allowlisted", got)
	}
	// The second entry expires within -warn-days.
	if len(r.Expiring) != 1 || r.Expiring[0].Package != "stdlib" {
		t.Errorf("expiring = %+v, want the stdlib entry", r.Expiring)
	}
}

func TestAnEntryAppliesOnItsExpiryDateAndFailsTheDayAfter(t *testing.T) {
	f := newFixture(t, allowBoth)
	if code, _, out := f.run(t, "2026-11-15"); code != 0 {
		t.Fatalf("on its expiry date: exit %d, want 0:\n%s", code, out)
	}
	code, r, out := f.run(t, "2026-11-16")
	if code != 1 {
		t.Fatalf("the day after: exit %d, want 1:\n%s", code, out)
	}
	if got := statusOf(r, "CVE-2026-0004"); got != StatusBlocking {
		t.Errorf("CVE-2026-0004: %s, want blocking once its entry expired", got)
	}
	if len(r.Expired) != 1 || !strings.Contains(out, "expired 2026-11-15") {
		t.Errorf("the expired entry is not reported: %+v\n%s", r.Expired, out)
	}
}

func TestAnEntryMatchesOnlyItsImagePackageAndTarget(t *testing.T) {
	code, r, _ := newFixture(t, `entries:
  - id: CVE-2026-0001
    package: libfoo
    image: hub
    reason: r
    owner: o
    expires: "2026-12-01"
  - id: CVE-2026-0004
    package: stdlib
    image: grain
    target: usr/local/bin/other
    reason: r
    owner: o
    expires: "2026-12-01"
`).run(t, "2026-11-10")
	if code != 1 || r.Counts.Blocking != 2 {
		t.Fatalf("exit %d with %d blocking, want 1 with 2: neither entry names what was found", code, r.Counts.Blocking)
	}
	// The hub entry is for an image this run never scanned, so it is
	// not called stale; the grain one is.
	if len(r.Stale) != 1 || r.Stale[0].Image != "grain" {
		t.Errorf("stale = %+v, want only the grain entry", r.Stale)
	}
}

func TestAnAllowlistEntryNeedsAReasonAnOwnerAndANearExpiry(t *testing.T) {
	for name, body := range map[string]string{
		"no reason":     "entries:\n  - {id: CVE-1, package: p, image: grain, owner: o, expires: \"2026-12-01\"}\n",
		"no owner":      "entries:\n  - {id: CVE-1, package: p, image: grain, reason: r, expires: \"2026-12-01\"}\n",
		"no expiry":     "entries:\n  - {id: CVE-1, package: p, image: grain, reason: r, owner: o}\n",
		"bad date":      "entries:\n  - {id: CVE-1, package: p, image: grain, reason: r, owner: o, expires: soon}\n",
		"too far":       "entries:\n  - {id: CVE-1, package: p, image: grain, reason: r, owner: o, expires: \"2028-01-01\"}\n",
		"unknown field": "entries:\n  - {id: CVE-1, package: p, image: grain, reason: r, owner: o, expires: \"2026-12-01\", expiry: x}\n",
		"no id":         "entries:\n  - {package: p, image: grain, reason: r, owner: o, expires: \"2026-12-01\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, out := newFixture(t, body).run(t, "2026-11-10"); code != 2 {
				t.Errorf("exit %d, want 2 for an invalid allowlist:\n%s", code, out)
			}
		})
	}
}

func TestReportOnlyNeverFailsAndMarksNewFindings(t *testing.T) {
	f := newFixture(t, "")
	prev := filepath.Join(f.dir, "prev.json")
	if err := os.WriteFile(prev, []byte(`{"findings":[{"image":"grain","id":"CVE-2026-0001","package":"libfoo","target":"os"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r, out := f.run(t, "2026-11-10", "-report-only", "-previous", prev)
	if code != 0 {
		t.Fatalf("exit %d, want 0 with -report-only:\n%s", code, out)
	}
	if r.NewCount != 3 {
		t.Errorf("new = %d, want 3 (everything but CVE-2026-0001)", r.NewCount)
	}
	if !strings.Contains(out, "New CRITICAL/HIGH since the previous report (2)") {
		t.Errorf("summary does not list the new findings:\n%s", out)
	}
}
