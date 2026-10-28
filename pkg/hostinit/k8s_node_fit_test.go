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

package hostinit

import (
	"context"
	"strings"
	"testing"
)

// defaultShapeSettingsJSON is GET /api/settings on a fresh deployment:
// nothing stored, so grain's own 2 cpu / 8 GiB / 30 GiB default applies.
const defaultShapeSettingsJSON = `{"sandboxCpus":0,"sandboxMemoryMb":0,"sandboxDiskGb":0,
	"sandboxCpusDefault":2,"sandboxMemoryMbDefault":8192,"sandboxDiskGbDefault":30}`

func runCheckK8sNodeFits(t *testing.T, settings, nodes string) Step {
	t.Helper()
	opts := Options{}.withDefaults()
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/settings", 200, []byte(settings), nil)
	sys.on(nodes, nil, "kubectl", "get", "nodes", "-o", "go-template="+k8sNodeAllocatableFormat)
	var r Result
	checkK8sNodeFitsSandbox(context.Background(), sys, opts, &r, true)
	if len(r.Steps) != 1 {
		t.Fatalf("Steps = %+v, want exactly one", r.Steps)
	}
	for _, c := range sys.httpRequestCalls {
		if c.method != "GET" {
			t.Errorf("checkK8sNodeFitsSandbox made a %s %s -- it must only read the settings", c.method, c.url)
		}
	}
	return r.Steps[0]
}

// The 10-04 walkthrough's kind node: 8129388Ki allocatable cannot hold
// the 8 GiB default, and init used to exit 0 with every run's pod then
// sitting Pending.
func TestCheckK8sNodeFitsReportsADefaultShapeNoNodeCanHold(t *testing.T) {
	s := runCheckK8sNodeFits(t, defaultShapeSettingsJSON, "grain-control-plane|8|8129388Ki|102626232Ki|<no value>|\n")
	if s.Status != StepManual {
		t.Fatalf("Status = %s, want %s: %+v", s.Status, StepManual, s)
	}
	for _, want := range []string{"8192 Mi memory", "7938 Mi", "sandboxMemoryMb"} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", s.Detail, want)
		}
	}
	want := `curl -s -X PUT http://127.0.0.1:4242/api/settings -H 'Content-Type: application/json' -d '{"sandboxMemoryMb":5120}'`
	if s.Command != want {
		t.Errorf("Command = %q\nwant      %q", s.Command, want)
	}
}

func TestCheckK8sNodeFitsPassesWhenOneNodeFits(t *testing.T) {
	nodes := "cp|8|64Gi|200Gi||NoSchedule,\n" + // big, but tainted
		"small|2|4Gi|50Gi|<no value>|\n" +
		"big|16|32Gi|100Gi|<no value>|\n"
	s := runCheckK8sNodeFits(t, defaultShapeSettingsJSON, nodes)
	if s.Status != StepDone || !strings.Contains(s.Detail, "node big") {
		t.Fatalf("Step = %+v, want done on node big", s)
	}
}

// Lowering the setting is what the manual step asks for; once it is
// lowered, the same node passes.
func TestCheckK8sNodeFitsReadsTheStoredShape(t *testing.T) {
	settings := `{"sandboxCpus":0,"sandboxMemoryMb":2048,"sandboxDiskGb":10,
		"sandboxCpusDefault":2,"sandboxMemoryMbDefault":8192,"sandboxDiskGbDefault":30}`
	s := runCheckK8sNodeFits(t, settings, "grain-control-plane|8|8129388Ki|102626232Ki|<no value>|\n")
	if s.Status != StepDone {
		t.Fatalf("Step = %+v, want done", s)
	}
}

func TestCheckK8sNodeFitsSkipsWhenNodesCannotBeRead(t *testing.T) {
	opts := Options{}.withDefaults()
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/settings", 200, []byte(defaultShapeSettingsJSON), nil)
	var r Result
	checkK8sNodeFitsSandbox(context.Background(), sys, opts, &r, true)
	if len(r.Steps) != 1 || r.Steps[0].Status != StepSkipped {
		t.Fatalf("Steps = %+v, want one skipped step", r.Steps)
	}
}

func TestParseK8sQuantity(t *testing.T) {
	for q, want := range map[string]float64{
		"8": 8, "7800m": 7.8, "8129388Ki": 8129388 * 1024, "16Gi": 16 << 30, "100G": 100e9, "1e3": 1000,
	} {
		got, err := parseK8sQuantity(q)
		if err != nil || got != want {
			t.Errorf("parseK8sQuantity(%q) = %v, %v; want %v", q, got, err, want)
		}
	}
	if _, err := parseK8sQuantity("lots"); err == nil {
		t.Error("parseK8sQuantity(\"lots\") succeeded")
	}
}
