package ci

import (
	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// The workflow parser and planner moved to pkg/workflows, the neutral
// library grain and grit both link (docs/design/grit-split.md, GS-2).
// These aliases keep this package's own callers and its importers
// compiling while they move over in their own time.

type (
	ActionsJob       = workflows.ActionsJob
	ArtifactSpec     = workflows.ArtifactSpec
	BranchTrigger    = workflows.BranchTrigger
	Event            = workflows.Event
	Job              = workflows.Job
	Key              = workflows.Key
	Plan             = workflows.Plan
	PlannedJob       = workflows.PlannedJob
	Priority         = workflows.Priority
	QuarantineConfig = workflows.QuarantineConfig
	QuarantineEntry  = workflows.QuarantineEntry
	Request          = workflows.Request
	SandboxSpec      = workflows.SandboxSpec
	ScheduleTrigger  = workflows.ScheduleTrigger
	Step             = workflows.Step
	Tree             = workflows.Tree
	TreeFile         = workflows.TreeFile
	Triggers         = workflows.Triggers
	Workflow         = workflows.Workflow
)

const (
	DefaultJobTimeout       = workflows.DefaultJobTimeout
	DeployedImagesEnv       = workflows.DeployedImagesEnv
	EventManual             = workflows.EventManual
	EventPullRequest        = workflows.EventPullRequest
	EventPush               = workflows.EventPush
	EventRelease            = workflows.EventRelease
	EventSchedule           = workflows.EventSchedule
	GitHubRunnerLabel       = workflows.GitHubRunnerLabel
	GitHubWorkflowDir       = workflows.GitHubWorkflowDir
	NoWorkflowAnswersPrefix = workflows.NoWorkflowAnswersPrefix
	PriorityMergeHead       = workflows.PriorityMergeHead
	PriorityNormal          = workflows.PriorityNormal
	PriorityReleaseGate     = workflows.PriorityReleaseGate
	PriorityRepair          = workflows.PriorityRepair
	PriorityUnderway        = workflows.PriorityUnderway
	RecurrenceDaily         = workflows.RecurrenceDaily
	RecurrenceEveryNHours   = workflows.RecurrenceEveryNHours
	RecurrenceEveryNMinutes = workflows.RecurrenceEveryNMinutes
	RecurrenceMonthly       = workflows.RecurrenceMonthly
	RecurrenceWeekly        = workflows.RecurrenceWeekly
	ReleaseVersionEnv       = workflows.ReleaseVersionEnv
	RequestedByEnv          = workflows.RequestedByEnv
	RequestedByKindEnv      = workflows.RequestedByKindEnv
	RunnerLabel             = workflows.RunnerLabel
	WorkflowDir             = workflows.WorkflowDir
)

var (
	Fingerprint               = workflows.Fingerprint
	FingerprintIn             = workflows.FingerprintIn
	GuestCacheKey             = workflows.GuestCacheKey
	HashOf                    = workflows.HashOf
	LoadQuarantine            = workflows.LoadQuarantine
	Parse                     = workflows.Parse
	ParseCron                 = workflows.ParseCron
	PlanID                    = workflows.PlanID
	PlanRun                   = workflows.PlanRun
	PlanRunIn                 = workflows.PlanRunIn
	RunEnv                    = workflows.RunEnv
	ValidateReleaseSecretName = workflows.ValidateReleaseSecretName
	WorkflowEvent             = workflows.WorkflowEvent
	RemovedGateJobs           = workflows.RemovedGateJobs
	EvaluateQuarantine        = workflows.EvaluateQuarantine
	NoWorkflowAnswered        = workflows.NoWorkflowAnswered
	ArtifactNamePattern       = workflows.ArtifactNamePattern
	JobIDPattern              = workflows.JobIDPattern
)

func init() {
	// The parser checks capability names against grain's own grammar
	// whenever grain is linked, which is the behaviour it had while it
	// lived in this package.
	workflows.Capabilities = workflows.CapabilitySyntax{
		CheckGrant: model.CheckCapabilityGrantSyntax,
		DecodeList: func(node *yaml.Node) ([]string, error) {
			var list model.CapabilityList
			if err := node.Decode(&list); err != nil {
				return nil, err
			}
			return list, nil
		},
		IsRepoAccess: IsRepoAccessCapability,
		DeclaredOnly: declaredOnly,
	}
}

func runEnv(req Request, workflow, jobID string) map[string]string {
	return workflows.RunEnv(req, workflow, jobID)
}

func guestCacheKey(repo string, files []TreeFile) Key { return workflows.GuestCacheKey(repo, files) }

// SandboxCommitment is what a sandbox of this shape takes out of the
// host while it exists -- what the deployment's gate holds against its
// resource budget (model.Budget, grain/task-173). It was
// SandboxSpec.Commitment before SandboxSpec moved to pkg/workflows,
// which may not import pkg/model.
//
// Disk is carried as the size promised, which the budget holds against
// the volume times its disk overcommit ratio rather than the volume
// itself: a shape's disk is an overlay that grows as it is written.
// model.Commitment says the same in more detail.
func SandboxCommitment(s SandboxSpec) model.Commitment {
	return model.Commitment{CPUs: s.CPUs, MemoryMB: s.MemoryMB, DiskGB: s.DiskGB}
}
