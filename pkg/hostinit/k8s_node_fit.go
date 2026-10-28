package hostinit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// k8sNodeAllocatableFormat is the go-template checkK8sNodeFitsSandbox
// reads every node with: name, the three allocatable quantities a sandbox
// pod requests, whether the node is cordoned, and the effects of its
// taints. A template rather than jsonpath because "ephemeral-storage"
// needs index, and one line per node because a node name holds no "|".
const k8sNodeAllocatableFormat = `{{range .items}}{{.metadata.name}}` +
	`|{{.status.allocatable.cpu}}|{{.status.allocatable.memory}}|{{index .status.allocatable "ephemeral-storage"}}` +
	`|{{.spec.unschedulable}}|{{range .spec.taints}}{{.effect}},{{end}}{{"\n"}}{{end}}`

// k8sNode is one schedulable node's allocatable resources, in cores and
// bytes.
type k8sNode struct {
	name               string
	cpus, memory, disk float64
}

// sandboxShapeSettings is the part of GET /api/settings that says what
// size a sandbox is built at when a task names none: the stored value,
// or grain's own default where that is 0 (pkg/ui's Settings type).
type sandboxShapeSettings struct {
	SandboxCPUs            int `json:"sandboxCpus"`
	SandboxMemoryMB        int `json:"sandboxMemoryMb"`
	SandboxDiskGB          int `json:"sandboxDiskGb"`
	SandboxCPUsDefault     int `json:"sandboxCpusDefault"`
	SandboxMemoryMBDefault int `json:"sandboxMemoryMbDefault"`
	SandboxDiskGBDefault   int `json:"sandboxDiskGbDefault"`
}

func orSetting(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// checkK8sNodeFitsSandbox is checkSandboxBudget's counterpart on the two
// Kubernetes platforms, where the daemon reports no budget: the cluster's
// scheduler is what admits a sandbox there, and it admits one only onto a
// node whose allocatable resources cover the pod's requests. A default
// shape bigger than every node -- 8Gi of memory on a kind node with
// 7.75Gi allocatable, the 10-04 walkthrough -- leaves every run's pod
// Pending until the orchestrator's unschedulable bound fails it, so it is
// reported here as a manual step naming the settings to lower.
//
// It compares requests against allocatable only, not against what other
// pods already hold, so it catches a shape no node can ever fit rather
// than one that does not fit right now. A cluster init cannot read nodes
// in (namespace-scoped RBAC, most often) skips the check rather than
// failing on it.
func checkK8sNodeFitsSandbox(ctx context.Context, sys System, opts Options, r *Result, attempted bool) {
	const name = "check a node can fit one sandbox pod"
	url := fmt.Sprintf("http://%s/api/settings", opts.UIAddr)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: "would compare the deployment's default sandbox shape with each node's allocatable cpu, memory and ephemeral storage"})
		return
	}
	if !attempted {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: the daemon's API was not reached"})
		return
	}
	status, body, err := sys.HTTPRequest(ctx, http.MethodGet, url, nil)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: fmt.Sprintf("skipped: GET %s: status %d: %v", url, status, err)})
		return
	}
	var s sandboxShapeSettings
	if err := json.Unmarshal(body, &s); err != nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("skipped: parsing %s's response: %v", url, err)})
		return
	}
	cpus := orSetting(s.SandboxCPUs, s.SandboxCPUsDefault)
	memMB := orSetting(s.SandboxMemoryMB, s.SandboxMemoryMBDefault)
	diskGB := orSetting(s.SandboxDiskGB, s.SandboxDiskGBDefault)
	if cpus <= 0 && memMB <= 0 && diskGB <= 0 {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: the daemon reports no sandbox shape"})
		return
	}

	out, err := runKubectl(ctx, sys, opts.K8s, "get", "nodes", "-o", "go-template="+k8sNodeAllocatableFormat)
	if err != nil {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: fmt.Sprintf("skipped: kubectl get nodes: %v -- check by hand that one node's allocatable resources cover "+
				"%d cpu, %d Mi memory and %d Gi ephemeral storage", err, cpus, memMB, diskGB)})
		return
	}
	nodes, err := parseK8sNodes(out)
	if err != nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: " + err.Error()})
		return
	}
	if len(nodes) == 0 {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  "the cluster has no schedulable node (every node is cordoned or tainted NoSchedule/NoExecute), so no sandbox pod can run",
			Command: kubectlCommand(opts.K8s, "get", "nodes")})
		return
	}

	want := k8sNode{cpus: float64(cpus), memory: float64(memMB) * (1 << 20), disk: float64(diskGB) * (1 << 30)}
	var largest k8sNode
	for _, n := range nodes {
		if n.cpus >= want.cpus && n.memory >= want.memory && n.disk >= want.disk {
			r.add(Step{Name: name, Status: StepDone,
				Detail: fmt.Sprintf("node %s can fit a %d cpu / %d Mi / %d Gi sandbox", n.name, cpus, memMB, diskGB)})
			return
		}
		largest.cpus = math.Max(largest.cpus, n.cpus)
		largest.memory = math.Max(largest.memory, n.memory)
		largest.disk = math.Max(largest.disk, n.disk)
	}

	// Suggest three quarters of the largest node's allocatable in each
	// dimension that does not fit: the sandbox shares its node with
	// grain's own pod and the cluster's system pods, whose requests come
	// out of the same allocatable.
	var short []string
	fix := map[string]int{}
	if largest.cpus < want.cpus {
		short = append(short, fmt.Sprintf("%d cpu (the most any node has is %s)", cpus, strconv.FormatFloat(largest.cpus, 'f', -1, 64)))
		fix["sandboxCpus"] = int(largest.cpus * 3 / 4)
	}
	if largest.memory < want.memory {
		short = append(short, fmt.Sprintf("%d Mi memory (the most any node has is %d Mi)", memMB, int(largest.memory/(1<<20))))
		fix["sandboxMemoryMb"] = int(largest.memory*3/4/(1<<30)) * 1024
	}
	if largest.disk < want.disk {
		short = append(short, fmt.Sprintf("%d Gi ephemeral storage (the most any node has is %d Gi)", diskGB, int(largest.disk/(1<<30))))
		fix["sandboxDiskGb"] = int(largest.disk * 3 / 4 / (1 << 30))
	}
	detail := "no schedulable node can fit one sandbox pod of the default shape, so every run's pod would stay Pending until it is " +
		"failed as Unschedulable"
	if len(short) > 0 {
		detail += ": it requests " + strings.Join(short, ", ")
	} else {
		detail += fmt.Sprintf(" (%d cpu, %d Mi memory and %d Gi ephemeral storage together)", cpus, memMB, diskGB)
	}
	detail += ". Lower sandboxCpus/sandboxMemoryMb/sandboxDiskGb in Settings → Sandbox, or add a larger node"
	step := Step{Name: name, Status: StepManual}
	usable := len(fix) > 0
	for _, v := range fix {
		if v < 1 {
			usable = false
		}
	}
	if usable {
		reqBody, _ := json.Marshal(fix)
		detail += ", or run the command below"
		step.Command = fmt.Sprintf("curl -s -X PUT %s -H 'Content-Type: application/json' -d %s", url, shQuote(string(reqBody)))
	}
	step.Detail = detail
	r.add(step)
}

// parseK8sNodes reads k8sNodeAllocatableFormat's output, leaving out
// every node a sandbox pod cannot be scheduled onto: a cordoned one, or
// one carrying a NoSchedule or NoExecute taint (sandbox pods carry no
// tolerations -- pkg/k8s.Client.Manifest).
func parseK8sNodes(out string) ([]k8sNode, error) {
	var nodes []k8sNode
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != 6 {
			return nil, fmt.Errorf("reading kubectl get nodes: unexpected line %q", line)
		}
		if f[4] == "true" || strings.Contains(f[5], "NoSchedule") || strings.Contains(f[5], "NoExecute") {
			continue
		}
		n := k8sNode{name: f[0]}
		var err error
		if n.cpus, err = parseK8sQuantity(f[1]); err != nil {
			return nil, fmt.Errorf("node %s's allocatable cpu: %w", f[0], err)
		}
		if n.memory, err = parseK8sQuantity(f[2]); err != nil {
			return nil, fmt.Errorf("node %s's allocatable memory: %w", f[0], err)
		}
		if n.disk, err = parseK8sQuantity(f[3]); err != nil {
			return nil, fmt.Errorf("node %s's allocatable ephemeral-storage: %w", f[0], err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// parseK8sQuantity reads a Kubernetes resource quantity ("8", "7800m",
// "8129388Ki", "16Gi", "100G", "1e9") as a plain number of base units.
// An absent quantity (a node that reports none, rendered as "<no value>"
// or empty) reads as unbounded, so it never fails the fit on its own.
func parseK8sQuantity(q string) (float64, error) {
	q = strings.TrimSpace(q)
	if q == "" || q == "<no value>" {
		return math.Inf(1), nil
	}
	suffixes := []struct {
		s string
		m float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
		{"n", 1e-9}, {"u", 1e-6}, {"m", 1e-3}, {"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18},
	}
	for _, sfx := range suffixes {
		if num, ok := strings.CutSuffix(q, sfx.s); ok {
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("quantity %q: %w", q, err)
			}
			return v * sfx.m, nil
		}
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0, fmt.Errorf("quantity %q: %w", q, err)
	}
	return v, nil
}
