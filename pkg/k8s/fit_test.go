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

package k8s

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUsage is a UsageReader standing in for kubectl: it answers with
// whatever reading the test has set and counts how often it was asked.
type fakeUsage struct {
	mu    sync.Mutex
	usage ClusterUsage
	err   error
	reads int
}

func (f *fakeUsage) NodeUsage(context.Context, map[string]string) (ClusterUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.usage, f.err
}

func cores(n float64) int64 { return int64(n * 1000) }

const gib = int64(1) << 30

func node(name string, allocCPU, usedCPU float64, allocMemGiB, usedMemGiB int64) NodeUsage {
	return NodeUsage{
		Name:        name,
		Allocatable: Requests{MilliCPU: cores(allocCPU), MemoryBytes: allocMemGiB * gib, DiskBytes: 500 * gib},
		Requested:   Requests{MilliCPU: cores(usedCPU), MemoryBytes: usedMemGiB * gib},
	}
}

func TestFitIsPerNodeNotAClusterWideSum(t *testing.T) {
	// 6 cores free across the cluster, but no more than 3 on any one
	// node: a 4-core pod does not fit, though the sum says it would.
	nodes := []NodeUsage{node("a", 16, 13, 64, 0), node("b", 16, 13, 64, 0)}
	want := Requests{MilliCPU: cores(4), MemoryBytes: 8 * gib, DiskBytes: 30 * gib}
	if got := fits(nodes, nil, want); got.OK || got.Never || !got.Known {
		t.Errorf("fits = %+v, want a known refusal that is not Never", got)
	}
	nodes[1].Requested.MilliCPU = cores(12)
	if got := fits(nodes, nil, want); !got.OK {
		t.Errorf("fits = %+v, want OK once node b has 4 cores free", got)
	}
}

func TestFitChecksMemoryAndEphemeralStorageToo(t *testing.T) {
	nodes := []NodeUsage{node("a", 64, 0, 16, 12)}
	if got := fits(nodes, nil, Requests{MilliCPU: 1000, MemoryBytes: 8 * gib}); got.OK {
		t.Errorf("fits = %+v, want a memory refusal: 4 GiB free, 8 wanted", got)
	}
	nodes[0].Requested.DiskBytes = 490 * gib
	if got := fits(nodes, nil, Requests{MilliCPU: 1000, MemoryBytes: gib, DiskBytes: 30 * gib}); got.OK {
		t.Errorf("fits = %+v, want an ephemeral-storage refusal: 10 GiB free, 30 wanted", got)
	}
}

func TestFitSaysNeverForAPodLargerThanAnyNode(t *testing.T) {
	nodes := []NodeUsage{node("a", 8, 0, 32, 0), node("b", 8, 0, 32, 0)}
	got := fits(nodes, nil, Requests{MilliCPU: cores(16), MemoryBytes: gib})
	if got.OK || !got.Never {
		t.Fatalf("fits = %+v, want Never: no node has 16 cores at all", got)
	}
	if got := fits(nil, nil, Requests{MilliCPU: 1}); !got.Never {
		t.Errorf("fits with no schedulable node = %+v, want Never", got)
	}
}

func TestFitCheckerCountsPodsCreatedSinceTheReading(t *testing.T) {
	reader := &fakeUsage{usage: ClusterUsage{Nodes: []NodeUsage{node("a", 16, 4, 64, 0)}}}
	now := time.Unix(1000, 0)
	f := &FitChecker{Reader: reader, Now: func() time.Time { return now }}
	pod := Requests{MilliCPU: cores(4), MemoryBytes: 8 * gib, DiskBytes: 30 * gib}
	ctx := context.Background()

	// 12 cores free: three 4-core pods fit, the fourth does not, all on
	// the one reading -- a burst inside the TTL cannot overshoot.
	for i := 0; i < 3; i++ {
		if got := f.Check(ctx, pod); !got.OK {
			t.Fatalf("pod %d: Check = %+v, want OK", i, got)
		}
		f.Reserve("p"+string(rune('0'+i)), pod)
	}
	if got := f.Check(ctx, pod); got.OK {
		t.Fatalf("fourth pod: Check = %+v, want a refusal: the first three already took the room", got)
	}
	if reader.reads != 1 {
		t.Errorf("read the cluster %d times inside one TTL, want 1", reader.reads)
	}

	// A released pod gives its room back.
	f.Release("p0")
	if got := f.Check(ctx, pod); !got.OK {
		t.Errorf("after a release: Check = %+v, want OK", got)
	}

	// A reading that sees a reserved pod bound stops counting it twice.
	reader.usage.Nodes[0].Requested.MilliCPU = cores(12)
	reader.usage.Bound = map[string]bool{"p1": true, "p2": true}
	now = now.Add(DefaultFitTTL)
	if got := f.Check(ctx, pod); !got.OK {
		t.Errorf("after a re-read with p1/p2 bound: Check = %+v, want OK (12 of 16 used, 4 free)", got)
	}
	if reader.reads != 2 {
		t.Errorf("reads = %d, want a second read once the TTL passed", reader.reads)
	}
}

func TestFitCheckerAdmitsWhenTheClusterCannotBeRead(t *testing.T) {
	f := &FitChecker{Reader: &fakeUsage{err: errors.New("forbidden")}}
	if got := f.Check(context.Background(), Requests{MilliCPU: cores(1000)}); !got.OK || got.Known {
		t.Errorf("Check = %+v, want an unknown OK: an unreadable cluster holds nothing back", got)
	}
	var nilChecker *FitChecker
	if got := nilChecker.Check(context.Background(), Requests{}); !got.OK {
		t.Errorf("nil checker: %+v, want OK", got)
	}
}

func TestPodShapeRequestsMatchTheManifest(t *testing.T) {
	got := PodShape{CPUs: 8, MemoryMB: 16384, DiskGB: 30, CPUOvercommit: 2, MemoryOvercommit: 2, DiskOvercommit: 2}.Requests()
	want := Requests{MilliCPU: 4000, MemoryBytes: 8192 << 20, DiskBytes: 15 * gib}
	if got != want {
		t.Errorf("Requests = %+v, want %+v", got, want)
	}
}

const fitNodesJSON = `{"items":[
 {"metadata":{"name":"n1","labels":{"pool":"sandbox"}},"spec":{},
  "status":{"allocatable":{"cpu":"15890m","memory":"60Gi","ephemeral-storage":"100Gi"},
            "conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"cordoned","labels":{"pool":"sandbox"}},"spec":{"unschedulable":true},
  "status":{"allocatable":{"cpu":"16","memory":"64Gi"},"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"tainted","labels":{"pool":"sandbox"}},"spec":{"taints":[{"effect":"NoSchedule"}]},
  "status":{"allocatable":{"cpu":"16","memory":"64Gi"},"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"other","labels":{"pool":"system"}},"spec":{},
  "status":{"allocatable":{"cpu":"16","memory":"64Gi"},"conditions":[{"type":"Ready","status":"True"}]}}
]}`

const fitPodsJSON = `{"items":[
 {"metadata":{"name":"kube-proxy","namespace":"kube-system"},"spec":{"nodeName":"n1",
  "containers":[{"resources":{"requests":{"cpu":"250m","memory":"256Mi"}}}]},"status":{"phase":"Running"}},
 {"metadata":{"name":"grain-granule-7-1","namespace":"grain"},"spec":{"nodeName":"n1",
  "initContainers":[{"resources":{"requests":{"cpu":"8"}}}],
  "containers":[{"resources":{"requests":{"cpu":"4","memory":"8Gi","ephemeral-storage":"30Gi"}}}]},
  "status":{"phase":"Running"}},
 {"metadata":{"name":"grain-granule-8-1","namespace":"grain"},"spec":{
  "containers":[{"resources":{"requests":{"cpu":"4"}}}]},"status":{"phase":"Pending"}}
]}`

func TestNodeUsageReadsNodesAndEveryNamespacesPodsThroughKubectl(t *testing.T) {
	kubectl, calls := fakeKubectlByArgs(t, map[string]string{
		"get nodes -l pool=sandbox -o json": fitNodesJSON,
		"get pods --all-namespaces --field-selector=status.phase!=Succeeded,status.phase!=Failed -o json": fitPodsJSON,
	})
	usage, err := Client{Kubectl: kubectl, Namespace: "grain"}.NodeUsage(context.Background(), map[string]string{"pool": "sandbox"})
	if err != nil {
		t.Fatalf("NodeUsage: %v (calls %q)", err, calls())
	}
	if len(usage.Nodes) != 1 || usage.Nodes[0].Name != "n1" {
		t.Fatalf("nodes = %+v, want only n1: cordoned, tainted and unselected nodes take no sandbox pod", usage.Nodes)
	}
	n := usage.Nodes[0]
	// kube-proxy's 250m plus the sandbox's max(4 cores of containers, an
	// 8-core init container); the Pending pod is bound nowhere yet.
	if n.Requested.MilliCPU != 8250 {
		t.Errorf("requested cpu = %dm, want 8250m", n.Requested.MilliCPU)
	}
	if n.Requested.MemoryBytes != 8*gib+256<<20 || n.Requested.DiskBytes != 30*gib {
		t.Errorf("requested = %+v, want 8Gi+256Mi memory and 30Gi storage", n.Requested)
	}
	if n.Allocatable.MilliCPU != 15890 {
		t.Errorf("allocatable cpu = %dm, want 15890m", n.Allocatable.MilliCPU)
	}
	if !usage.Bound["grain-granule-7-1"] || usage.Bound["grain-granule-8-1"] || usage.Partial {
		t.Errorf("Bound = %v Partial = %v, want only the bound grain pod and a full reading", usage.Bound, usage.Partial)
	}
}

func TestNodeUsageFallsBackToItsOwnNamespaceWithoutClusterWidePods(t *testing.T) {
	kubectl, _ := fakeKubectlByArgs(t, map[string]string{
		"get nodes -o json": fitNodesJSON,
		"get pods --field-selector=status.phase!=Succeeded,status.phase!=Failed -o json": `{"items":[]}`,
	})
	usage, err := Client{Kubectl: kubectl, Namespace: "grain"}.NodeUsage(context.Background(), nil)
	if err != nil {
		t.Fatalf("NodeUsage: %v", err)
	}
	if !usage.Partial || len(usage.Nodes) != 2 {
		t.Errorf("usage = %+v, want a Partial reading of n1 and other", usage)
	}
	if !strings.Contains(FreeSummary{Known: true, Partial: true}.String(), "own namespace") {
		t.Errorf("a partial summary should say so")
	}
}

// fakeKubectlByArgs is fakeKubectl keyed by the whole argument list after
// the "-n <namespace>" this package prepends, answering each from a file
// so a JSON body needs no shell quoting. Anything it was not given fails
// the way an RBAC refusal does.
func fakeKubectlByArgs(t *testing.T, responses map[string]string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nshift 2\nprintf '%s\\036' \"$*\" >> " + log + "\n")
	i := 0
	for args, body := range responses {
		file := filepath.Join(dir, "r"+strconv.Itoa(i))
		i++
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		script.WriteString("if [ \"$*\" = '" + args + "' ]; then cat " + file + "; exit 0; fi\n")
	}
	script.WriteString("echo 'Error from server (Forbidden): pods is forbidden' >&2\nexit 1\n")
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, func() []string {
		data, _ := os.ReadFile(log)
		return strings.Split(strings.Trim(string(data), "\x1e"), "\x1e")
	}
}
