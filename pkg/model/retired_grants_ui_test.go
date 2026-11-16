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
	"errors"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// uiRetiredGrantTable is the /next/ UI's copy of the retired in-process
// names: Settings → Capabilities names each one a deployment's
// settings/capabilities.yml still configures, with what replaced it.
const uiRetiredGrantTable = "../../ui-ng/src/app/settings/capability-retired.ts"

var (
	uiPresetCallRe  = regexp.MustCompile(`^preset\("([^"]+)"\)$`)
	uiReleaseCallRe = regexp.MustCompile(`^RELEASE\("([^"]+)"\)$`)
)

func splitTopLevelTS(s string, sep byte) []string {
	var parts []string
	var quote byte
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		default:
			if c == sep && depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func stripTSLineComments(s string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}

func evalUIGrantExpr(expr string) (string, bool) {
	expr = stripTSLineComments(expr)
	if m := uiPresetCallRe.FindStringSubmatch(expr); m != nil {
		return APIRef(m[1]), true
	}
	if m := uiReleaseCallRe.FindStringSubmatch(expr); m != nil {
		return "https://grain.dev/capabilities/release#" + m[1] + "={}", true
	}
	if expr == `gcpKeyGrant("default")` {
		return GCPKeySandboxAgentGrant, true
	}
	if strings.HasPrefix(expr, "onServer(") && strings.HasSuffix(expr, ")") {
		args := splitTopLevelTS(expr[len("onServer("):len(expr)-1], ',')
		if len(args) < 1 {
			return "", false
		}
		return evalUIGrantExpr(args[0])
	}
	if strings.HasPrefix(expr, "{") && strings.HasSuffix(expr, "}") {
		for _, field := range splitTopLevelTS(expr[1:len(expr)-1], ',') {
			kv := splitTopLevelTS(field, ':')
			if len(kv) == 2 && strings.TrimSpace(kv[0]) == "grant" {
				return evalUIGrantExpr(kv[1])
			}
		}
		return "", false
	}
	if len(expr) >= 2 && (expr[0] == '`' || expr[0] == '"' || expr[0] == '\'') && expr[len(expr)-1] == expr[0] {
		s := expr[1 : len(expr)-1]
		s = strings.ReplaceAll(s, "${SERVER}", "https://grain.dev/capabilities")
		s = strings.ReplaceAll(s, "${MODEL_PROXY}", ModelProxyRef)
		return s, true
	}
	return "", false
}

// TestUIRetiredGrantTableNamesEveryRetiredCapability keeps the UI's
// RETIRED_IN_PROCESS equal to the union of retiredGrantReplacements and
// retiredGrantMultiReplacements -- both the set of retired capability names
// and the canonical grant spelling shown for each.
func TestUIRetiredGrantTableNamesEveryRetiredCapability(t *testing.T) {
	src, err := os.ReadFile(uiRetiredGrantTable)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is not in this tree", uiRetiredGrantTable)
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "export const RETIRED_IN_PROCESS")
	if start < 0 {
		t.Fatalf("%s declares no RETIRED_IN_PROCESS", uiRetiredGrantTable)
	}
	body := text[start:]
	open := strings.Index(body, "{\n")
	end := strings.Index(body, "\n};")
	if open < 0 || end < 0 || end <= open {
		t.Fatalf("%s: malformed RETIRED_IN_PROCESS object literal", uiRetiredGrantTable)
	}
	body = body[open+2 : end]

	want := map[string]string{}
	for name := range retiredGrantReplacements {
		want[name] = strings.Join(RetiredGrantsBecame(name), " + ")
	}
	for name := range retiredGrantMultiReplacements {
		want[name] = strings.Join(RetiredGrantsBecame(name), " + ")
	}

	got := map[string]string{}
	for _, entry := range splitTopLevelTS(body, ',') {
		entry = stripTSLineComments(entry)
		if entry == "" {
			continue
		}
		kv := splitTopLevelTS(entry, ':')
		if len(kv) != 2 {
			t.Fatalf("%s: unexpected entry %q", uiRetiredGrantTable, entry)
		}
		name := strings.Trim(strings.TrimSpace(kv[0]), `"'`)
		grant, ok := evalUIGrantExpr(kv[1])
		if !ok {
			t.Errorf("%s: could not evaluate RETIRED_IN_PROCESS[%q] = %s", uiRetiredGrantTable, name, strings.TrimSpace(kv[1]))
			got[name] = ""
			continue
		}
		got[name] = grant
	}

	var missing, extra []string
	for name, wantGrant := range want {
		gotGrant, ok := got[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		if gotGrant != "" && gotGrant != wantGrant {
			t.Errorf("%s: RETIRED_IN_PROCESS[%q].grant = %q, want %q", uiRetiredGrantTable, name, gotGrant, wantGrant)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("%s's RETIRED_IN_PROCESS leaves out retired capabilities %v: add each with the grant it was retired onto", uiRetiredGrantTable, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%s's RETIRED_IN_PROCESS names %v, which pkg/model does not retire", uiRetiredGrantTable, extra)
	}
}
