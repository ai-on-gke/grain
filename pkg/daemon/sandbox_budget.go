package daemon

import (
	"context"
	"log"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/sysstat"
	"github.com/bwsalmon/grain/pkg/ui"
)

// defaultShaper is the sandbox-backend half of a changed sandbox-cpus or
// sandbox-memory-mb: orchestrator.GranuleSandboxes and
// orchestrator.K8sSandboxes implement it -- a VM's create flags and a
// pod's resource requests are the same setting expressed twice -- and
// orchestrator.HostSandboxes deliberately does not, since a local
// directory has no CPU or memory shape to default, which is the same
// reason those two settings are documented as meaningless under it.
// Declared here,
// where it is consumed, rather than in pkg/orchestrator, exactly as
// orphanReaper above is.
type defaultShaper interface {
	SetDefaultShape(orchestrator.Shape)
}

// overcommitShaper is the sandbox-backend half of a changed
// sandbox-cpu-overcommit, sandbox-memory-overcommit or
// sandbox-disk-overcommit on a cluster-backed
// deployment: orchestrator.K8sSandboxes and orchestrator.K8sGranuleSandboxes
// implement it so each sandbox pod's resource requests are scaled by the
// overcommit ratios while its resource limits stay at the full sandbox shape.
type overcommitShaper interface {
	SetOvercommit(cpu, memory, disk float64)
}

// sandboxShapeIgnored reports whether this deployment's sandbox backend
// drops the deployment-wide sandbox shape on the floor -- true for the
// backend that is not a defaultShaper, which is exactly the test
// refresh above applies before handing a changed shape on.
//
// Asked of the backend rather than of cfg.granuleSandboxes so the answer
// cannot drift from the code that actually applies the setting: whatever
// refresh would skip is what the Settings pane annotates as not in
// effect (ui.Config.SandboxShapeIgnored). A backend that grew a
// SetDefaultShape tomorrow would stop being annotated on the same
// commit that made the annotation false.
func SandboxShapeIgnored(sandboxes orchestrator.Sandboxes) bool {
	_, shaped := sandboxes.(defaultShaper)
	return !shaped
}

// localHostSandboxes reports whether this deployment's sandboxes run on the
// same host whose /proc pkg/sysstat reads -- true for local granule VMs,
// docker containers, and host directories, and false for Kubernetes pods.
func localHostSandboxes(sandboxes orchestrator.Sandboxes) bool {
	switch sandboxes.(type) {
	case *orchestrator.GranuleSandboxes, *orchestrator.ContainerSandboxes, *orchestrator.HostSandboxes:
		return true
	default:
		return false
	}
}

// sandboxHostCapacity is startUIServer's ui.Config.SandboxHostCapacity and
// Config.withHostCapacity's capacity reader: how much machine or cluster
// capacity this deployment's sandboxes have to share, for the arithmetic
// Settings does against max_workers and the sandbox shape
// (ui.SandboxCommitment) and for the deployment-wide model.Budget that
// bounds Ledger and ci.SharedGate.
//
// For local-host backends (GranuleSandboxes, ContainerSandboxes,
// HostSandboxes), it reads this host's /proc via sysstat.Read.
// For Kubernetes backends (K8sSandboxes, K8sGranuleSandboxes), it sums
// allocatable CPU, memory and ephemeral storage across ready, schedulable cluster nodes matching
// the backend's NodeSelector via HostCapacity, so Config.Budget().Known() is
// true and ci.SharedGate rations CI jobs and reserves capacity for waiting
// agent runs and console sessions instead of flooding kube-scheduler with
// unbounded Pending pods.
func sandboxHostCapacity(sandboxes orchestrator.Sandboxes) func() (ui.HostCapacity, error) {
	switch sb := sandboxes.(type) {
	case *orchestrator.GranuleSandboxes, *orchestrator.ContainerSandboxes, *orchestrator.HostSandboxes:
		return func() (ui.HostCapacity, error) {
			snap, err := sysstat.Read()
			if err != nil {
				return ui.HostCapacity{}, err
			}
			return ui.HostCapacity{CPUs: snap.CPUs, MemoryMB: snap.MemTotalMB}, nil
		}
	case *orchestrator.K8sSandboxes:
		return func() (ui.HostCapacity, error) {
			cpus, memoryMB, diskGB, err := sb.HostCapacity(context.Background())
			if err != nil {
				return ui.HostCapacity{}, err
			}
			return ui.HostCapacity{CPUs: cpus, MemoryMB: memoryMB, DiskGB: diskGB}, nil
		}
	case *orchestrator.K8sGranuleSandboxes:
		return func() (ui.HostCapacity, error) {
			cpus, memoryMB, diskGB, err := sb.HostCapacity(context.Background())
			if err != nil {
				return ui.HostCapacity{}, err
			}
			return ui.HostCapacity{CPUs: cpus, MemoryMB: memoryMB, DiskGB: diskGB}, nil
		}
	default:
		return nil
	}
}

// budgetSentence is the half of the startup line that says what those
// counts are held against, for a reader who would otherwise take "up to
// 15 CI job(s)" as a promise the host can keep.
//
// kubernetesMode gets its own answer rather than sharing the
// host-could-not-be-read one: both leave limits.Budget unknown, but only
// one of them is a gap. See logSandboxBudget's and sandboxHostCapacity's
// own doc comments for why Kubernetes has nothing to read in the first
// place and what holds its sandboxes instead.
func BudgetSentence(limits model.Limits, kubernetesMode bool) string {
	if !limits.Budget.Known() {
		if kubernetesMode {
			return "there is no host-wide budget to hold it to: each sandbox pod carries its own CPU and " +
				"memory request, equal to its limit, and the cluster scheduler is what refuses to place one " +
				"where a node has no room"
		}
		return "nothing here knows how big this host is, so nothing holds this deployment's sandboxes to it"
	}
	return "a sandbox is admitted only while " + limits.Budget.Summary() + " has room for its shape"
}

// logSandboxBudget says what this deployment will commit its host to,
// and what it has kept back -- the total every count above is now held
// against (model.Budget).
//
// Said on every start rather than only when something looks wrong, which
// is the difference between this and logSandboxCommitment below: a
// budget is the number an operator needs *before* they raise max_workers
// or max_ci_jobs, and the whole lesson of grain/task-173 is that both
// settings were raised by people with no figure in front of them.
// Silent only where there is nothing to say, which is where the host
// could not be read at all.
//
// Kubernetes gets a third message, neither of the above: no budget is
// known there either, but not because anything failed to read -- see
// sandboxHostCapacity's own doc comment for why there is no host figure
// this deployment could take in the first place. Saying so plainly is
// the point: the other branch's wording ("could not be read") is correct
// for a backend that was supposed to have a number and didn't get one,
// and wrong here, where asking the question at all would be the mistake.
func LogSandboxBudget(c Config) {
	limits := c.Limits()
	if !limits.Budget.Known() {
		if KubernetesSandboxes(c) {
			log.Print("grain daemon: Kubernetes sandboxing has no sandbox budget of its own, and that is not " +
				"a setting to go looking for: a sandbox pod's node is not this daemon's own machine, and there " +
				"are usually several of them, so there is no single host size to hold a total against. Each " +
				"sandbox pod still carries its own CPU and memory request, equal to its limit, and it is the " +
				"cluster's own scheduler that refuses to place one where a node has no room left for it -- " +
				"that is the admission control here")
			return
		}
		log.Print("grain daemon: this host's cores and memory could not be read, so nothing bounds how much " +
			"machine this deployment's sandboxes commit -- nothing then stands between this deployment and an " +
			"overcommitted machine")
		return
	}
	log.Printf("grain daemon: sandbox budget -- %s. Every sandbox on this host shares it: every agent "+
		"run (one at %s) and every grain CI job, charged at the shape it actually holds and only for as long "+
		"as it holds it, so a job or a run whose shape does not fit waits for memory rather than the host "+
		"swapping to find it. Nothing counts them",
		limits.Budget.Summary(), limits.AgentShape)
}
