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

package ci

import (
	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// The workflow parser and planner moved to pkg/workflows, the neutral
// library grain and grit both link (design/design/grit-split.design.md, GS-2).
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
	SandboxCommitment         = workflows.SandboxCommitment
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
