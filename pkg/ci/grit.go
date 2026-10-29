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
	gritartifacts "github.com/ai-on-gke/grain/pkg/grit/artifacts"
	gritcache "github.com/ai-on-gke/grain/pkg/grit/cache"
	gritengine "github.com/ai-on-gke/grain/pkg/grit/engine"
	gritsteps "github.com/ai-on-gke/grain/pkg/grit/steps"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// The job cache and artifact store implementations moved to grit
// (docs/design/grit-split.md, GS-2: pkg/grit/cache and
// pkg/grit/artifacts), which grain still links for embedded mode. These
// aliases keep this package's callers and importers compiling.

type (
	DirCache          = gritcache.DirCache
	DirCacheOptions   = gritcache.DirCacheOptions
	GuestCacheRequest = gritcache.GuestCacheRequest
	GuestCacheWarmer  = gritcache.GuestCacheWarmer
	GuestWarmer       = gritcache.GuestWarmer
)

const (
	DefaultGuestWarmCooldown = gritcache.DefaultGuestWarmCooldown
	DefaultGuestWarmMaxTime  = gritcache.DefaultGuestWarmMaxTime
	DefaultGuestWarmPoll     = gritcache.DefaultGuestWarmPoll
)

var (
	OpenDirCache = gritcache.OpenDirCache
)

type (
	DirArtifacts        = gritartifacts.DirArtifacts
	DirArtifactsOptions = gritartifacts.DirArtifactsOptions
)

var (
	OpenDirArtifacts = gritartifacts.OpenDirArtifacts
)

var humanBytes = workflows.HumanBytes

type (
	Admission        = gritsteps.Admission
	Command          = gritsteps.Command
	CommandResult    = gritsteps.CommandResult
	LiveOutput       = gritsteps.LiveOutput
	Sandbox          = gritsteps.Sandbox
	SandboxRequest   = gritsteps.SandboxRequest
	Sandboxes        = gritsteps.Sandboxes
	StreamingSandbox = gritsteps.StreamingSandbox
)

const (
	LiveOutputInterval = gritsteps.LiveOutputInterval
)

var (
	NewLiveOutput = gritsteps.NewLiveOutput
	StepIDFor     = gritsteps.StepIDFor
)

type (
	AdoptedJob         = gritengine.AdoptedJob
	Engine             = gritengine.Engine
	Gate               = gritengine.Gate
	GateRequest        = gritengine.GateRequest
	OOMReporter        = gritengine.OOMReporter
	PreemptionReporter = gritengine.PreemptionReporter
	StepAttachment     = gritengine.StepAttachment
	StepAttachments    = gritengine.StepAttachments
)

const (
	DefaultAgeBoost            = gritengine.DefaultAgeBoost
	DefaultConcurrency         = gritengine.DefaultConcurrency
	DefaultGatePoll            = gritengine.DefaultGatePoll
	DefaultReserveAfter        = gritengine.DefaultReserveAfter
	GoTestLogBeginMarker       = gritengine.GoTestLogBeginMarker
	GoTestLogEndMarker         = gritengine.GoTestLogEndMarker
	GoTestLogStatusMarker      = gritengine.GoTestLogStatusMarker
	MaxJobRetries              = gritengine.MaxJobRetries
	MaxSandboxName             = gritengine.MaxSandboxName
	OOMRetryReasonPrefix       = gritengine.OOMRetryReasonPrefix
	PreemptedRetryReasonPrefix = gritengine.PreemptedRetryReasonPrefix

	goTestLogBeginMarker  = gritengine.GoTestLogBeginMarker
	goTestLogEndMarker    = gritengine.GoTestLogEndMarker
	goTestLogStatusMarker = gritengine.GoTestLogStatusMarker
)

var (
	ConcludeRun                      = gritengine.ConcludeRun
	IsDaemonServiceError             = gritengine.IsDaemonServiceError
	IsDetaching                      = gritengine.IsDetaching
	IsJobRetryEligible               = gritengine.IsJobRetryEligible
	IsTransientOrInfrastructureError = gritengine.IsTransientOrInfrastructureError
	JobFailedOnDaemonService         = gritengine.JobFailedOnDaemonService
	NewJobRun                        = gritengine.NewJobRun
	SandboxName                      = gritengine.SandboxName
	SortJobRuns                      = gritengine.SortJobRuns
	SummarizeFailure                 = gritengine.SummarizeFailure
	WithDetach                       = gritengine.WithDetach

	concludeRun = gritengine.ConcludeRun
	isDetaching = gritengine.IsDetaching
	newJobRun   = gritengine.NewJobRun
	sandboxName = gritengine.SandboxName
)

func init() {
	gritengine.ExtractFailureSummary = ExtractFailureSummary
}
