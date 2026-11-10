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

// Command image-vulns is the policy half of the release images'
// vulnerability gate (docs/image-vulnerability-scanning.md). Trivy does the
// scanning, run by scripts/scan-images.sh; this reads the JSON reports
// Trivy wrote, one per image, together with the allowlist in
// .grain/baselines/image-vulns-allowlist.yaml, and decides:
//
//	blocking     a CRITICAL or HIGH finding with a fixed version
//	             available that no unexpired allowlist entry covers
//	allowlisted  the same, covered by an unexpired entry
//	unfixable    CRITICAL or HIGH with no fixed version yet
//	reported     everything below HIGH
//
// Only blocking findings fail it (exit 1). An allowlist entry stops
// applying the day after its expiry date, so an expired waiver fails the
// gate again rather than lasting forever. An allowlist that does not
// parse, or an entry missing its reason, owner or expiry, fails it too
// (exit 2).
//
// Usage:
//
//	image-vulns [flags] <image>=<trivy.json> ...
//
// <image> is the image's leaf name (grain, sandbox, sandbox-k8s, hub),
// which is what an allowlist entry's `image` names.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const dateLayout = "2006-01-02"

// maxWaiver is how far ahead an allowlist entry may expire: a waiver is
// a date to come back to, not a way to stop looking.
const maxWaiver = 180 * 24 * time.Hour

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now().UTC()))
}

type options struct {
	allowlist  string
	jsonOut    string
	mdOut      string
	previous   string
	reportOnly bool
	warnDays   int
	today      string
	title      string
}

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("image-vulns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.allowlist, "allowlist", ".grain/baselines/image-vulns-allowlist.yaml", "allowlist file (empty for none)")
	fs.StringVar(&o.jsonOut, "json", "", "write the evaluated findings here as JSON")
	fs.StringVar(&o.mdOut, "markdown", "", "write a Markdown summary here (default: stdout)")
	fs.StringVar(&o.previous, "previous", "", "an earlier -json report; findings not in it are listed as new")
	fs.BoolVar(&o.reportOnly, "report-only", false, "report blocking findings but exit 0")
	fs.IntVar(&o.warnDays, "warn-days", 14, "list allowlist entries expiring within this many days")
	fs.StringVar(&o.today, "today", "", "evaluate as of this date (YYYY-MM-DD; default today, UTC)")
	fs.StringVar(&o.title, "title", "Image vulnerability scan", "the summary's heading")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "image-vulns: name at least one <image>=<trivy.json>")
		return 2
	}
	today := now.Truncate(24 * time.Hour)
	if o.today != "" {
		t, err := time.Parse(dateLayout, o.today)
		if err != nil {
			fmt.Fprintf(stderr, "image-vulns: -today: %v\n", err)
			return 2
		}
		today = t
	}

	var entries []Entry
	if o.allowlist != "" {
		var err error
		entries, err = loadAllowlist(o.allowlist, today)
		if err != nil {
			fmt.Fprintf(stderr, "image-vulns: %v\n", err)
			return 2
		}
	}

	var findings []Finding
	var images, scanned []string
	for _, arg := range fs.Args() {
		name, path, ok := strings.Cut(arg, "=")
		if !ok || name == "" || path == "" {
			fmt.Fprintf(stderr, "image-vulns: %q is not <image>=<trivy.json>\n", arg)
			return 2
		}
		f, ref, err := loadTrivy(name, path)
		if err != nil {
			fmt.Fprintf(stderr, "image-vulns: %v\n", err)
			return 2
		}
		images = append(images, name+" ("+ref+")")
		scanned = append(scanned, name)
		findings = append(findings, f...)
	}

	r := evaluate(findings, entries, scanned, today, o.warnDays)
	r.Images = images
	r.Date = today.Format(dateLayout)
	if o.previous != "" {
		prev, err := loadReport(o.previous)
		if err != nil {
			// A missing or unreadable previous report only loses the
			// "new since" list; it is not a reason to lose the report.
			fmt.Fprintf(stderr, "image-vulns: previous report: %v\n", err)
		} else {
			r.markNew(prev)
		}
	}

	if o.jsonOut != "" {
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(o.jsonOut, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "image-vulns: %v\n", err)
			return 2
		}
	}
	md := r.markdown(o.title)
	if o.mdOut != "" {
		if err := os.WriteFile(o.mdOut, []byte(md), 0o644); err != nil {
			fmt.Fprintf(stderr, "image-vulns: %v\n", err)
			return 2
		}
	} else {
		fmt.Fprint(stdout, md)
	}
	for _, f := range r.Findings {
		if f.Status == StatusBlocking {
			fmt.Fprintf(stderr, "image-vulns: BLOCKING %s %s %s %s in %s (%s, fixed in %s)%s\n",
				f.Image, f.Severity, f.ID, f.Package, f.Target, f.Installed, f.Fixed, f.Note)
		}
	}
	if n := r.Counts.Blocking; n > 0 {
		fmt.Fprintf(stderr, "image-vulns: %d fixable CRITICAL/HIGH finding(s) are not allowlisted; fix them or add an entry to %s (docs/image-vulnerability-scanning.md)\n", n, o.allowlist)
		if !o.reportOnly {
			return 1
		}
	}
	return 0
}

// Entry is one allowlist waiver.
type Entry struct {
	// ID is the vulnerability (CVE-..., GHSA-..., GO-...). IDs lists
	// several that share everything else -- one vulnerable toolchain is
	// often dozens of stdlib CVEs.
	ID  string   `yaml:"id" json:"id,omitempty"`
	IDs []string `yaml:"ids" json:"ids,omitempty"`
	// Package is the package name as Trivy reports it (PkgName).
	Package string `yaml:"package" json:"package"`
	// Image is the image's leaf name, or "*" for every image.
	Image string `yaml:"image" json:"image"`
	// Target, optional, narrows the entry to one Trivy target: a binary's
	// path in the image, for a language package.
	Target  string `yaml:"target" json:"target,omitempty"`
	Reason  string `yaml:"reason" json:"reason"`
	Owner   string `yaml:"owner" json:"owner"`
	Expires string `yaml:"expires" json:"expires"`
	// Followup, optional, names the task or issue fixing it.
	Followup string `yaml:"followup" json:"followup,omitempty"`

	expires time.Time
	used    bool
}

type allowlistFile struct {
	Entries []Entry `yaml:"entries"`
}

func loadAllowlist(path string, today time.Time) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f allowlistFile
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	var problems []string
	for i := range f.Entries {
		e := &f.Entries[i]
		where := fmt.Sprintf("%s: entry %d (%s %s)", path, i+1, e.Image, e.Package)
		if e.ID != "" {
			e.IDs = append([]string{e.ID}, e.IDs...)
		}
		missing := []string{}
		for _, field := range []struct{ name, v string }{
			{"id or ids", strings.Join(e.IDs, "")}, {"package", e.Package}, {"image", e.Image},
			{"reason", e.Reason}, {"owner", e.Owner}, {"expires", e.Expires},
		} {
			if strings.TrimSpace(field.v) == "" {
				missing = append(missing, field.name)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s has no %s", where, strings.Join(missing, ", ")))
			continue
		}
		t, err := time.Parse(dateLayout, e.Expires)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: expires %q is not YYYY-MM-DD", where, e.Expires))
			continue
		}
		if t.Sub(today) > maxWaiver {
			problems = append(problems, fmt.Sprintf("%s: expires %s, more than %d days away; waive it for less and look again", where, e.Expires, int(maxWaiver.Hours()/24)))
			continue
		}
		e.expires = t
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return f.Entries, nil
}

func (e *Entry) matches(f Finding) bool {
	if e.Image != "*" && e.Image != f.Image {
		return false
	}
	if e.Package != f.Package {
		return false
	}
	if e.Target != "" && e.Target != f.Target {
		return false
	}
	for _, id := range e.IDs {
		if id == f.ID {
			return true
		}
	}
	return false
}

// expired reports whether the entry has stopped applying: it covers its
// expiry date itself, and not the day after.
func (e *Entry) expired(today time.Time) bool { return today.After(e.expires) }

// Status is what the gate made of one finding.
type Status string

const (
	StatusBlocking    Status = "blocking"
	StatusAllowlisted Status = "allowlisted"
	StatusUnfixable   Status = "unfixable"
	StatusReported    Status = "reported"
)

// Finding is one vulnerability in one package of one image.
type Finding struct {
	Image     string `json:"image"`
	ID        string `json:"id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed,omitempty"`
	Severity  string `json:"severity"`
	Target    string `json:"target"`
	Class     string `json:"class,omitempty"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	Status    Status `json:"status"`
	Note      string `json:"note,omitempty"`
	New       bool   `json:"new,omitempty"`
}

func (f Finding) key() string {
	return strings.Join([]string{f.Image, f.ID, f.Package, f.Target}, "|")
}

type trivyReport struct {
	ArtifactName string `json:"ArtifactName"`
	Results      []struct {
		Target          string `json:"Target"`
		Class           string `json:"Class"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
			Title            string `json:"Title"`
			PrimaryURL       string `json:"PrimaryURL"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

func loadTrivy(image, path string) ([]Finding, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var r trivyReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, "", fmt.Errorf("%s: %v", path, err)
	}
	// An OS package's target names the image reference it was scanned
	// as ("docker.io/grainai/hub:sha-abc (debian 12.15)"), which changes
	// every build and every point release; an allowlist entry, and the
	// weekly comparison, should not have to. It is just "os".
	var out []Finding
	for _, res := range r.Results {
		target := res.Target
		if res.Class == "os-pkgs" {
			target = "os"
		}
		for _, v := range res.Vulnerabilities {
			out = append(out, Finding{
				Image:     image,
				ID:        v.VulnerabilityID,
				Package:   v.PkgName,
				Installed: v.InstalledVersion,
				Fixed:     v.FixedVersion,
				Severity:  strings.ToUpper(v.Severity),
				Target:    target,
				Class:     res.Class,
				Title:     v.Title,
				URL:       v.PrimaryURL,
			})
		}
	}
	return out, r.ArtifactName, nil
}

// Counts sums a report's findings by status.
type Counts struct {
	Blocking    int `json:"blocking"`
	Allowlisted int `json:"allowlisted"`
	Unfixable   int `json:"unfixable"`
	Reported    int `json:"reported"`
}

// Report is the gate's verdict, as -json writes it.
type Report struct {
	Date     string    `json:"date"`
	Images   []string  `json:"images"`
	Counts   Counts    `json:"counts"`
	Findings []Finding `json:"findings"`
	// Expiring are entries that still apply but expire within -warn-days.
	Expiring []Entry `json:"expiring,omitempty"`
	// Expired are entries past their date that a finding still needs.
	Expired []Entry `json:"expired,omitempty"`
	// Stale are entries no finding matched: fixed, or never right.
	Stale []Entry `json:"stale,omitempty"`
	// NewCount is how many findings the -previous report did not have,
	// or -1 with no previous report to compare with.
	NewCount int `json:"new_count"`
}

func severityRank(s string) int {
	switch s {
	case "CRITICAL":
		return 0
	case "HIGH":
		return 1
	case "MEDIUM":
		return 2
	case "LOW":
		return 3
	}
	return 4
}

func evaluate(findings []Finding, entries []Entry, scanned []string, today time.Time, warnDays int) *Report {
	seen := map[string]bool{}
	r := &Report{NewCount: -1}
	expiredUsed := map[int]bool{}
	for _, f := range findings {
		// The same package can be reported twice for one binary (a
		// module listed under two paths); one is enough.
		k := f.key() + "|" + f.Installed
		if seen[k] {
			continue
		}
		seen[k] = true
		serious := f.Severity == "CRITICAL" || f.Severity == "HIGH"
		switch {
		case !serious:
			f.Status = StatusReported
		case f.Fixed == "":
			f.Status = StatusUnfixable
		default:
			f.Status = StatusBlocking
			for i := range entries {
				e := &entries[i]
				if !e.matches(f) {
					continue
				}
				if e.expired(today) {
					expiredUsed[i] = true
					f.Note = fmt.Sprintf("; allowlist entry (owner %s) expired %s", e.Owner, e.Expires)
					continue
				}
				e.used = true
				f.Status = StatusAllowlisted
				f.Note = ""
				break
			}
		}
		switch f.Status {
		case StatusBlocking:
			r.Counts.Blocking++
		case StatusAllowlisted:
			r.Counts.Allowlisted++
		case StatusUnfixable:
			r.Counts.Unfixable++
		default:
			r.Counts.Reported++
		}
		r.Findings = append(r.Findings, f)
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Image != b.Image {
			return a.Image < b.Image
		}
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		return a.key() < b.key()
	})
	warn := today.Add(time.Duration(warnDays) * 24 * time.Hour)
	for i, e := range entries {
		switch {
		case expiredUsed[i] && !e.used:
			r.Expired = append(r.Expired, e)
		case !e.used && !expiredUsed[i]:
			// An entry for an image this run did not scan says nothing
			// either way.
			if e.Image == "*" || slices.Contains(scanned, e.Image) {
				r.Stale = append(r.Stale, e)
			}
		case !e.expired(today) && !e.expires.After(warn):
			r.Expiring = append(r.Expiring, e)
		}
	}
	return r
}

func loadReport(path string) (*Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return &r, nil
}

func (r *Report) markNew(prev *Report) {
	had := map[string]bool{}
	for _, f := range prev.Findings {
		had[f.key()] = true
	}
	r.NewCount = 0
	for i := range r.Findings {
		if !had[r.Findings[i].key()] {
			r.Findings[i].New = true
			r.NewCount++
		}
	}
}

func entryIDs(e Entry) string {
	if len(e.IDs) <= 3 {
		return strings.Join(e.IDs, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(e.IDs[:2], ", "), len(e.IDs)-2)
}

// maxRows bounds each Markdown table: a step summary is capped at 1 MiB,
// and the JSON report has every finding anyway.
const maxRows = 200

func (r *Report) markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s (%s)\n\n", title, r.Date)
	verdict := "PASS: no fixable CRITICAL or HIGH finding is outside the allowlist."
	if r.Counts.Blocking > 0 {
		verdict = fmt.Sprintf("FAIL: %d fixable CRITICAL/HIGH finding(s) are not allowlisted.", r.Counts.Blocking)
	}
	fmt.Fprintf(&b, "**%s**\n\n", verdict)

	type tally struct{ blocking, allowlisted, unfixable, crit, high, medium, low, isNew int }
	per := map[string]*tally{}
	var names []string
	for _, f := range r.Findings {
		t := per[f.Image]
		if t == nil {
			t = &tally{}
			per[f.Image] = t
			names = append(names, f.Image)
		}
		switch f.Status {
		case StatusBlocking:
			t.blocking++
		case StatusAllowlisted:
			t.allowlisted++
		case StatusUnfixable:
			t.unfixable++
		}
		switch f.Severity {
		case "CRITICAL":
			t.crit++
		case "HIGH":
			t.high++
		case "MEDIUM":
			t.medium++
		case "LOW":
			t.low++
		}
		if f.New {
			t.isNew++
		}
	}
	sort.Strings(names)
	b.WriteString("Scanned: " + strings.Join(r.Images, ", ") + "\n\n")
	newCol := r.NewCount >= 0
	b.WriteString("| image | blocking | allowlisted | unfixable C/H | critical | high | medium | low |")
	if newCol {
		b.WriteString(" new |")
	}
	b.WriteString("\n|---|---|---|---|---|---|---|---|")
	if newCol {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, n := range names {
		t := per[n]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d |", n, t.blocking, t.allowlisted, t.unfixable, t.crit, t.high, t.medium, t.low)
		if newCol {
			fmt.Fprintf(&b, " %d |", t.isNew)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	table := func(heading string, keep func(Finding) bool) {
		var rows []Finding
		for _, f := range r.Findings {
			if keep(f) {
				rows = append(rows, f)
			}
		}
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&b, "### %s (%d)\n\n| image | severity | id | package | installed | fixed | target |\n|---|---|---|---|---|---|---|\n", heading, len(rows))
		for i, f := range rows {
			if i == maxRows {
				fmt.Fprintf(&b, "\n...and %d more; the JSON report has them all.\n", len(rows)-maxRows)
				break
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s%s |\n", f.Image, f.Severity, f.ID, f.Package, f.Installed, f.Fixed, f.Target, f.Note)
		}
		b.WriteString("\n")
	}
	table("Blocking: fixable CRITICAL/HIGH, not allowlisted", func(f Finding) bool { return f.Status == StatusBlocking })
	if newCol {
		table("New CRITICAL/HIGH since the previous report", func(f Finding) bool {
			return f.New && f.Status != StatusReported
		})
	}

	entries := func(heading string, es []Entry) {
		if len(es) == 0 {
			return
		}
		fmt.Fprintf(&b, "### %s (%d)\n\n| image | package | ids | owner | expires | reason |\n|---|---|---|---|---|---|\n", heading, len(es))
		for _, e := range es {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", e.Image, e.Package, entryIDs(e), e.Owner, e.Expires, strings.ReplaceAll(e.Reason, "\n", " "))
		}
		b.WriteString("\n")
	}
	entries("Allowlist entries that have expired", r.Expired)
	entries("Allowlist entries expiring soon", r.Expiring)
	entries("Allowlist entries no finding needs any more (remove them)", r.Stale)
	return b.String()
}
