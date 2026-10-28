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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Requests is what a pod asks the scheduler for: the three resources a
// sandbox pod's manifest names (Manifest's "requests"), in the units the
// scheduler itself adds them up in.
type Requests struct {
	MilliCPU    int64
	MemoryBytes int64
	DiskBytes   int64
}

// Plus is both requests held at once.
func (r Requests) Plus(o Requests) Requests {
	return Requests{MilliCPU: r.MilliCPU + o.MilliCPU, MemoryBytes: r.MemoryBytes + o.MemoryBytes, DiskBytes: r.DiskBytes + o.DiskBytes}
}

// Minus is r with o taken out of it, floored at nothing in each dimension.
func (r Requests) Minus(o Requests) Requests {
	return Requests{
		MilliCPU:    max(r.MilliCPU-o.MilliCPU, 0),
		MemoryBytes: max(r.MemoryBytes-o.MemoryBytes, 0),
		DiskBytes:   max(r.DiskBytes-o.DiskBytes, 0),
	}
}

// Covers reports whether r has room for want in every dimension.
func (r Requests) Covers(want Requests) bool {
	return r.MilliCPU >= want.MilliCPU && r.MemoryBytes >= want.MemoryBytes && r.DiskBytes >= want.DiskBytes
}

// String is r as an operator would read it off `kubectl describe node`.
func (r Requests) String() string {
	return fmt.Sprintf("%s cpu, %d Mi memory, %d Gi ephemeral storage",
		strconv.FormatFloat(float64(r.MilliCPU)/1000, 'f', -1, 64), r.MemoryBytes>>20, r.DiskBytes>>30)
}

// Requests is what a pod of this shape asks the scheduler for -- the
// same numbers Manifest writes under "requests", read back as quantities.
func (s PodShape) Requests() Requests {
	cpu, _ := parseMilliCPU(cpuRequestQuantity(s.CPUs, s.CPUOvercommit))
	mem, _ := parseMemoryBytes(memoryRequestQuantity(s.MemoryMB, s.MemoryOvercommit))
	disk, _ := parseMemoryBytes(diskRequestQuantity(s.DiskGB, s.DiskOvercommit))
	return Requests{MilliCPU: cpu, MemoryBytes: mem, DiskBytes: disk}
}

// NodeUsage is one node a sandbox pod could be scheduled onto: what it
// has to give (allocatable) and what the non-terminated pods already
// bound to it have asked for (requested).
type NodeUsage struct {
	Name        string
	Allocatable Requests
	Requested   Requests
}

// Free is what the scheduler would still give a new pod on this node.
func (n NodeUsage) Free() Requests { return n.Allocatable.Minus(n.Requested) }

// ClusterUsage is one reading of the cluster: every node a sandbox pod
// may land on, and the names of this namespace's pods that the reading
// already counts against a node (Bound), which is how FitChecker knows a
// pod it created has stopped being its own guess.
type ClusterUsage struct {
	Nodes []NodeUsage
	Bound map[string]bool
	// Partial is set when the pods in other namespaces could not be
	// listed (a Role, not a ClusterRole, on pods): the requested figures
	// count only this namespace's own pods and are a floor, not a total.
	Partial bool
}

// UsageReader is what FitChecker reads the cluster through: Client in a
// deployment, a fake in a test.
type UsageReader interface {
	NodeUsage(ctx context.Context, nodeSelector map[string]string) (ClusterUsage, error)
}

// NodeUsage reads every node a sandbox pod may be scheduled onto -- Ready,
// not cordoned, carrying no NoSchedule or NoExecute taint (sandbox pods
// carry no tolerations) and matching nodeSelector -- with the sum of the
// requests of the non-terminated pods bound to each.
//
// The pods are read across all namespaces, because system pods and other
// workloads take their share of a node's allocatable exactly as grain's
// own do. Where grain's account may only list pods in its own namespace,
// the reading falls back to those and says so (ClusterUsage.Partial).
func (c Client) NodeUsage(ctx context.Context, nodeSelector map[string]string) (ClusterUsage, error) {
	args := []string{"get", "nodes"}
	if selector := formatLabelSelector(nodeSelector); selector != "" {
		args = append(args, "-l", selector)
	}
	nodesOut, err := c.run(ctx, append(args, "-o", "json")...)
	if err != nil {
		return ClusterUsage{}, fmt.Errorf("k8s: reading nodes: %w", err)
	}
	const notTerminated = "--field-selector=status.phase!=Succeeded,status.phase!=Failed"
	partial := false
	podsOut, err := c.run(ctx, "get", "pods", "--all-namespaces", notTerminated, "-o", "json")
	if err != nil {
		partial = true
		if podsOut, err = c.run(ctx, "get", "pods", notTerminated, "-o", "json"); err != nil {
			return ClusterUsage{}, fmt.Errorf("k8s: reading pods' resource requests: %w", err)
		}
	}
	usage, err := parseClusterUsage([]byte(nodesOut), []byte(podsOut), nodeSelector, c.NamespaceOrDefault())
	if err != nil {
		return ClusterUsage{}, err
	}
	usage.Partial = partial
	return usage, nil
}

type nodeUsageItem struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
		Taints        []struct {
			Effect string `json:"effect"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Allocatable map[string]string `json:"allocatable"`
		Conditions  []nodeCondition   `json:"conditions"`
	} `json:"status"`
}

type podContainerResources struct {
	Resources struct {
		Requests map[string]string `json:"requests"`
	} `json:"resources"`
}

type podUsageItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName       string                  `json:"nodeName"`
		Containers     []podContainerResources `json:"containers"`
		InitContainers []podContainerResources `json:"initContainers"`
		Overhead       map[string]string       `json:"overhead"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

func parseClusterUsage(nodesJSON, podsJSON []byte, nodeSelector map[string]string, namespace string) (ClusterUsage, error) {
	var nodes struct {
		Items []nodeUsageItem `json:"items"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return ClusterUsage{}, fmt.Errorf("k8s: reading node list: %w", err)
	}
	var pods struct {
		Items []podUsageItem `json:"items"`
	}
	if err := json.Unmarshal(podsJSON, &pods); err != nil {
		return ClusterUsage{}, fmt.Errorf("k8s: reading pod list: %w", err)
	}
	usage := ClusterUsage{Bound: map[string]bool{}}
	index := map[string]int{}
	for _, it := range nodes.Items {
		if it.Spec.Unschedulable || !nodeReady(it.Status.Conditions) {
			continue
		}
		if len(nodeSelector) > 0 && !matchesNodeSelector(it.Metadata.Labels, nodeSelector) {
			continue
		}
		tainted := false
		for _, taint := range it.Spec.Taints {
			if taint.Effect == "NoSchedule" || taint.Effect == "NoExecute" {
				tainted = true
			}
		}
		if tainted {
			continue
		}
		alloc, err := readRequests(it.Status.Allocatable)
		if err != nil {
			return ClusterUsage{}, fmt.Errorf("k8s: node %q allocatable: %w", it.Metadata.Name, err)
		}
		index[it.Metadata.Name] = len(usage.Nodes)
		usage.Nodes = append(usage.Nodes, NodeUsage{Name: it.Metadata.Name, Allocatable: alloc})
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == "" || p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" {
			continue
		}
		if p.Metadata.Namespace == namespace {
			usage.Bound[p.Metadata.Name] = true
		}
		i, ok := index[p.Spec.NodeName]
		if !ok {
			continue
		}
		usage.Nodes[i].Requested = usage.Nodes[i].Requested.Plus(podRequests(p))
	}
	return usage, nil
}

// podRequests is what the scheduler charges a node for p: the larger of
// its containers' requests summed and its largest init container's, per
// resource, plus its runtime overhead. A quantity that does not parse is
// counted as nothing rather than failing the whole reading over one pod.
func podRequests(p podUsageItem) Requests {
	var sum Requests
	for _, c := range p.Spec.Containers {
		r, _ := readRequests(c.Resources.Requests)
		sum = sum.Plus(r)
	}
	for _, c := range p.Spec.InitContainers {
		r, _ := readRequests(c.Resources.Requests)
		sum.MilliCPU = max(sum.MilliCPU, r.MilliCPU)
		sum.MemoryBytes = max(sum.MemoryBytes, r.MemoryBytes)
		sum.DiskBytes = max(sum.DiskBytes, r.DiskBytes)
	}
	overhead, _ := readRequests(p.Spec.Overhead)
	return sum.Plus(overhead)
}

// readRequests reads the three resources out of a ResourceList. An
// absent one is zero; one that does not parse is an error, with what
// did parse still returned.
func readRequests(list map[string]string) (Requests, error) {
	var r Requests
	var errs []error
	if q, ok := list["cpu"]; ok {
		v, err := parseMilliCPU(q)
		errs = append(errs, err)
		r.MilliCPU = v
	}
	if q, ok := list["memory"]; ok {
		v, err := parseMemoryBytes(q)
		errs = append(errs, err)
		r.MemoryBytes = v
	}
	if q, ok := list["ephemeral-storage"]; ok {
		v, err := parseMemoryBytes(q)
		errs = append(errs, err)
		r.DiskBytes = v
	}
	return r, errors.Join(errs...)
}

// Fit is FitChecker's answer for one pod.
type Fit struct {
	// OK is whether the pod may be created now: some node has room for
	// it, or nothing could be read to say otherwise.
	OK bool
	// Known is whether the answer comes from a reading of the cluster at
	// all; an unknown one is always OK.
	Known bool
	// Never is set when no node's allocatable covers the pod even with
	// nothing else on it: waiting cannot help, so the caller should fail
	// rather than hold.
	Never bool
	// Reason, when !OK, is the one line that says what is short.
	Reason string
}

// DefaultFitTTL is how long one reading of the cluster answers for: long
// enough that a reconcile tick admitting a burst of work reads the
// cluster once rather than once per pod, short enough that room freed by
// a pod finishing is seen by the next tick.
const DefaultFitTTL = 5 * time.Second

// pendingTTL is how long a pod FitChecker created goes on being counted
// as its own guess when no reading has seen it bound to a node. A pod
// the scheduler has not placed after this is either gone or Unschedulable
// -- and the latter is the orchestrator's to release (WaitReady's
// unschedulable bound), not this ledger's to hold room for.
const pendingTTL = 10 * time.Minute

// FitChecker admits sandbox pods against the cluster's real free capacity
// rather than against grain's own budget alone: before a pod is created,
// at least one schedulable node must have room for its cpu, memory and
// ephemeral-storage requests on top of everything already bound to it.
//
// One reading answers for TTL, and the pods this checker has been told
// about since (Reserve) are placed onto that reading's nodes before the
// next pod is asked about, so a burst admitted inside one TTL cannot all
// be promised the same free node.
type FitChecker struct {
	Reader       UsageReader
	NodeSelector map[string]string
	// TTL is DefaultFitTTL when zero.
	TTL time.Duration
	// Now is time.Now when nil.
	Now func() time.Time

	mu         sync.Mutex
	reading    *ClusterUsage
	readAt     time.Time
	readErr    error
	refreshing bool
	pending    map[string]pendingPod
}

type pendingPod struct {
	req Requests
	at  time.Time
}

func (f *FitChecker) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *FitChecker) ttl() time.Duration {
	if f.TTL > 0 {
		return f.TTL
	}
	return DefaultFitTTL
}

// Refresh re-reads the cluster if the reading it holds is older than TTL.
func (f *FitChecker) Refresh(ctx context.Context) {
	if f == nil || f.Reader == nil {
		return
	}
	f.mu.Lock()
	fresh := !f.readAt.IsZero() && f.now().Sub(f.readAt) < f.ttl()
	if fresh || f.refreshing {
		f.mu.Unlock()
		return
	}
	f.refreshing = true
	f.mu.Unlock()
	usage, err := f.Reader.NodeUsage(ctx, f.NodeSelector)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshing = false
	f.readAt = f.now()
	f.readErr = err
	if err != nil {
		return
	}
	f.reading = &usage
	for name, p := range f.pending {
		if usage.Bound[name] || f.readAt.Sub(p.at) > pendingTTL {
			delete(f.pending, name)
		}
	}
}

// Check is Refresh followed by Peek: the answer an Acquire about to
// create a pod asks for.
func (f *FitChecker) Check(ctx context.Context, want Requests) Fit {
	f.Refresh(ctx)
	return f.peek(want, false)
}

// Peek answers from whatever reading is held, without reading the
// cluster: what an admission inside a dispatch cycle or the CI gate asks,
// many times a tick and under their own locks. A stale reading starts a
// refresh in the background for the next one.
func (f *FitChecker) Peek(want Requests) Fit { return f.peek(want, true) }

func (f *FitChecker) peek(want Requests, refreshStale bool) Fit {
	if f == nil {
		return Fit{OK: true}
	}
	f.mu.Lock()
	stale := refreshStale && f.Reader != nil && !f.refreshing && (f.readAt.IsZero() || f.now().Sub(f.readAt) >= f.ttl())
	reading := f.reading
	var pending []Requests
	for _, p := range f.pending {
		pending = append(pending, p.req)
	}
	f.mu.Unlock()
	if stale {
		go f.Refresh(context.Background())
	}
	if reading == nil {
		return Fit{OK: true}
	}
	return fits(reading.Nodes, pending, want)
}

// Reserve records that a pod called name, asking for req, has just been
// created, so it is counted until a reading sees it bound to a node.
func (f *FitChecker) Reserve(name string, req Requests) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		f.pending = map[string]pendingPod{}
	}
	f.pending[name] = pendingPod{req: req, at: f.now()}
}

// Release forgets name's reservation: the pod was deleted, so the room
// it was counted against is the scheduler's again.
func (f *FitChecker) Release(name string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, name)
}

// fits places every pending pod onto the node with the most free cpu that
// has room for it -- a guess at what the scheduler will do with pods it
// has not reported binding yet -- and then asks whether any node still
// has room for want.
func fits(nodes []NodeUsage, pending []Requests, want Requests) Fit {
	if len(nodes) == 0 {
		return Fit{Known: true, Never: true,
			Reason: "the cluster has no schedulable node for a sandbox pod (every node is cordoned, not Ready, " +
				"tainted NoSchedule/NoExecute, or outside the configured node selector)"}
	}
	free := make([]Requests, len(nodes))
	never := true
	for i, n := range nodes {
		free[i] = n.Free()
		if n.Allocatable.Covers(want) {
			never = false
		}
	}
	if never {
		return Fit{Known: true, Never: true, Reason: fmt.Sprintf(
			"no node's allocatable can ever fit this sandbox pod's requests (%s), even with nothing else on it",
			want)}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].MilliCPU > pending[j].MilliCPU })
	for _, p := range pending {
		best := -1
		for i := range free {
			if free[i].Covers(p) && (best < 0 || free[i].MilliCPU > free[best].MilliCPU) {
				best = i
			}
		}
		if best >= 0 {
			free[best] = free[best].Minus(p)
		}
	}
	var most Requests
	for _, fr := range free {
		if fr.Covers(want) {
			return Fit{OK: true, Known: true}
		}
		if fr.MilliCPU > most.MilliCPU {
			most = fr
		}
	}
	return Fit{Known: true, Reason: fmt.Sprintf(
		"no node in the cluster has room for this sandbox pod's requests (%s) right now; the node with the most "+
			"free cpu has %s", want, most)}
}

// FreeSummary is the reading as one record for a health pane: how many
// nodes a sandbox pod may land on, their allocatable and free totals,
// the largest single free node, and when it was read.
type FreeSummary struct {
	Known       bool
	Nodes       int
	Allocatable Requests
	Free        Requests
	LargestFree Requests
	LargestNode string
	Pending     int
	Partial     bool
	ReadAt      time.Time
	Err         string
}

// Summary describes the reading this checker holds, refreshing it first
// if it is stale.
func (f *FitChecker) Summary(ctx context.Context) FreeSummary {
	if f == nil {
		return FreeSummary{}
	}
	f.Refresh(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	s := FreeSummary{ReadAt: f.readAt, Pending: len(f.pending)}
	if f.readErr != nil {
		s.Err = f.readErr.Error()
	}
	if f.reading == nil {
		return s
	}
	s.Known = true
	s.Partial = f.reading.Partial
	s.Nodes = len(f.reading.Nodes)
	for _, n := range f.reading.Nodes {
		s.Allocatable = s.Allocatable.Plus(n.Allocatable)
		fr := n.Free()
		s.Free = s.Free.Plus(fr)
		if fr.MilliCPU > s.LargestFree.MilliCPU || s.LargestNode == "" {
			s.LargestFree, s.LargestNode = fr, n.Name
		}
	}
	return s
}

// String is a summary as one line of prose.
func (s FreeSummary) String() string {
	if !s.Known {
		if s.Err != "" {
			return "the cluster's free capacity could not be read: " + s.Err
		}
		return "the cluster's free capacity has not been read yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d schedulable node(s), %s free of %s allocatable; the roomiest node (%s) has %s free",
		s.Nodes, s.Free, s.Allocatable, s.LargestNode, s.LargestFree)
	if s.Pending > 0 {
		fmt.Fprintf(&b, "; %d sandbox pod(s) created since this reading are not counted in it yet", s.Pending)
	}
	if s.Partial {
		b.WriteString(" (only grain's own namespace's pods could be listed, so other workloads' requests are not subtracted)")
	}
	return b.String()
}
