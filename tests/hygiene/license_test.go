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

// Package hygiene holds repository-wide checks: every tracked source file
// carries the Apache license header, and nothing outside the historical
// records names Google-internal tools, hosts or projects.
package hygiene

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fix, set with `go test ./tests/hygiene -run TestLicenseHeaders -args
// -fix`, adds the header to every file the test would fail on instead of
// failing.
var fix = flag.Bool("fix", false, "add the license header to source files that lack it")

// licenseLines is the header every source file starts with, without its
// comment marker. The year is the one the file was added in.
var licenseLines = []string{
	"Copyright 2026 Google LLC",
	"",
	`Licensed under the Apache License, Version 2.0 (the "License");`,
	"you may not use this file except in compliance with the License.",
	"You may obtain a copy of the License at",
	"",
	"    http://www.apache.org/licenses/LICENSE-2.0",
	"",
	"Unless required by applicable law or agreed to in writing, software",
	`distributed under the License is distributed on an "AS IS" BASIS,`,
	"WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.",
	"See the License for the specific language governing permissions and",
	"limitations under the License.",
}

// licenseMarker is what says a file already has a header, whatever its
// year or comment style.
const licenseMarker = "Licensed under the Apache License, Version 2.0"

// licenseSkipped are tracked paths that are not grain's own source, or
// whose bytes something compares: vendored code and test fixtures.
var licenseSkipped = regexp.MustCompile(`^third_party/|(^|/)testdata/|(^|/)node_modules/|\.min\.js$`)

// commentStyle says how a tracked file's header is written: "//" or "#",
// or "" for a file that needs none.
func commentStyle(rel string) string {
	if licenseSkipped.MatchString(rel) {
		return ""
	}
	base := filepath.Base(rel)
	switch filepath.Ext(base) {
	case ".go", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return "//"
	case ".sh", ".bash", ".py", ".tf":
		return "#"
	case ".yml", ".yaml":
		if strings.HasPrefix(rel, ".grain/workflows/") || strings.HasPrefix(rel, ".github/workflows/") {
			return "#"
		}
		return ""
	}
	if strings.HasSuffix(base, ".dockerignore") {
		return ""
	}
	if base == "Makefile" || strings.HasPrefix(base, "Dockerfile") || strings.HasPrefix(base, "Granulefile") {
		return "#"
	}
	return ""
}

// generated reports a Go file go generate wrote, which says so in its
// first comment by the standard convention.
var generated = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

func hasLicense(data []byte) bool {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(head, []byte(licenseMarker))
}

// withLicense returns data with the header put in front of it, after any
// line that has to stay first: a #! line, or a Dockerfile parser
// directive.
func withLicense(data []byte, style string) []byte {
	var header bytes.Buffer
	for _, l := range licenseLines {
		if l == "" {
			header.WriteString(style + "\n")
		} else {
			header.WriteString(style + " " + l + "\n")
		}
	}
	header.WriteString("\n")
	var keep []byte
	rest := data
	for {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i+1]
		} else {
			break
		}
		if bytes.HasPrefix(line, []byte("#!")) || dockerDirective.Match(line) {
			keep = append(keep, line...)
			rest = rest[len(line):]
			continue
		}
		break
	}
	out := append([]byte{}, keep...)
	out = append(out, header.Bytes()...)
	return append(out, rest...)
}

var dockerDirective = regexp.MustCompile(`^#\s*(syntax|escape|check)=`)

func TestLicenseHeaders(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range trackedFiles(t, root) {
		style := commentStyle(rel)
		if style == "" {
			continue
		}
		path := filepath.Join(root, rel)
		if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
			continue // gone from the work tree, or a symlink
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if hasLicense(data) || generated.Match(data) {
			continue
		}
		if *fix {
			if err := os.WriteFile(path, withLicense(data, style), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Errorf("%s has no license header: run `go test ./tests/hygiene -run TestLicenseHeaders -args -fix`", rel)
	}
}

func TestWithLicenseKeepsTheLinesThatMustStayFirst(t *testing.T) {
	got := string(withLicense([]byte("#!/bin/bash\nset -e\n"), "#"))
	if !strings.HasPrefix(got, "#!/bin/bash\n# Copyright 2026 Google LLC\n#\n# Licensed") || !strings.HasSuffix(got, "# limitations under the License.\n\nset -e\n") {
		t.Errorf("withLicense on a script = %q", got)
	}
	got = string(withLicense([]byte("# syntax=docker/dockerfile:1\nFROM x\n"), "#"))
	if !strings.HasPrefix(got, "# syntax=docker/dockerfile:1\n# Copyright") {
		t.Errorf("withLicense on a Dockerfile = %q", got)
	}
	got = string(withLicense([]byte("package x\n"), "//"))
	if !strings.HasPrefix(got, "// Copyright 2026 Google LLC\n//\n// Licensed") || !strings.HasSuffix(got, "// limitations under the License.\n\npackage x\n") || !hasLicense([]byte(got)) {
		t.Errorf("withLicense on Go = %q", got)
	}
}

// repoRoot is the top of the work tree this package sits two levels in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// trackedFiles lists the files git tracks under root, relative to it.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git ls-files: %v (not a git checkout)", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}
