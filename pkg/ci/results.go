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

import "github.com/ai-on-gke/grain/pkg/workflows"

// The run record, its filters and the cache and artifact interfaces moved
// to pkg/workflows with the parser (design/design/grit-split.design.md, GS-2), so
// that grain and grit agree on one set of wire types. These aliases keep
// this package's callers and importers compiling.

type (
	Artifact             = workflows.Artifact
	ArtifactBounds       = workflows.ArtifactBounds
	ArtifactRef          = workflows.ArtifactRef
	ArtifactStats        = workflows.ArtifactStats
	ArtifactStore        = workflows.ArtifactStore
	Build                = workflows.Build
	Cache                = workflows.Cache
	CacheBounds          = workflows.CacheBounds
	CacheStats           = workflows.CacheStats
	CancelSource         = workflows.CancelSource
	ClearableArtifacts   = workflows.ClearableArtifacts
	Conclusion           = workflows.Conclusion
	Diagnostic           = workflows.Diagnostic
	FailureSummary       = workflows.FailureSummary
	InspectableArtifacts = workflows.InspectableArtifacts
	InspectableCache     = workflows.InspectableCache
	JobAttempt           = workflows.JobAttempt
	JobRun               = workflows.JobRun
	Run                  = workflows.Run
	RunFilter            = workflows.RunFilter
	RunSortKey           = workflows.RunSortKey
	Signature            = workflows.Signature
	StepRun              = workflows.StepRun
	StorageBounds        = workflows.StorageBounds
	TestFailure          = workflows.TestFailure
	WaitCause            = workflows.WaitCause
	Wait                 = workflows.Wait
)

const (
	ArtifactContentType             = workflows.ArtifactContentType
	ArtifactExt                     = workflows.ArtifactExt
	CancelByOperator                = workflows.CancelByOperator
	CancelBySystem                  = workflows.CancelBySystem
	ConclusionCancelled             = workflows.ConclusionCancelled
	ConclusionErrored               = workflows.ConclusionErrored
	ConclusionFailure               = workflows.ConclusionFailure
	ConclusionPending               = workflows.ConclusionPending
	ConclusionSkipped               = workflows.ConclusionSkipped
	ConclusionSuccess               = workflows.ConclusionSuccess
	DefaultArtifactMaxAge           = workflows.DefaultArtifactMaxAge
	DefaultArtifactMaxArtifactBytes = workflows.DefaultArtifactMaxArtifactBytes
	DefaultArtifactMaxBytes         = workflows.DefaultArtifactMaxBytes
	DefaultArtifactMaxRunBytes      = workflows.DefaultArtifactMaxRunBytes
	DefaultCacheMaxAge              = workflows.DefaultCacheMaxAge
	DefaultCacheMaxBytes            = workflows.DefaultCacheMaxBytes
	DefaultCacheMaxEntries          = workflows.DefaultCacheMaxEntries
	DefaultCacheMaxEntryBytes       = workflows.DefaultCacheMaxEntryBytes
	MaxStoredRunOutput              = workflows.MaxStoredRunOutput
	MaxStoredStepOutput             = workflows.MaxStoredStepOutput
	MaxStepOutput                   = workflows.MaxStepOutput
	MinStoredStepOutput             = workflows.MinStoredStepOutput
	SortRunsByDuration              = workflows.SortRunsByDuration
	SortRunsByFinished              = workflows.SortRunsByFinished
	SortRunsByStarted               = workflows.SortRunsByStarted
	StatePending                    = workflows.StatePending
	StateRunning                    = workflows.StateRunning
	WaitForGrant                    = workflows.WaitForGrant
	WaitForGuest                    = workflows.WaitForGuest
	WaitForHole                     = workflows.WaitForHole
	WaitForPriority                 = workflows.WaitForPriority
	WaitForRoom                     = workflows.WaitForRoom
	WaitForRun                      = workflows.WaitForRun
	WaitForSlot                     = workflows.WaitForSlot
)

var (
	BuildOf               = workflows.BuildOf
	DefaultStorageBounds  = workflows.DefaultStorageBounds
	FilterRuns            = workflows.FilterRuns
	JobStateOf            = workflows.JobStateOf
	MatchingJobs          = workflows.MatchingJobs
	ParseRunSortKey       = workflows.ParseRunSortKey
	ParseRunState         = workflows.ParseRunState
	ParseSince            = workflows.ParseSince
	RunSortKeys           = workflows.RunSortKeys
	RunStateOf            = workflows.RunStateOf
	RunStates             = workflows.RunStates
	SortRuns              = workflows.SortRuns
	SupersededByRunReason = workflows.SupersededByRunReason
	SupersedingRun        = workflows.SupersedingRun
	Cacheable             = workflows.Cacheable
	ArtifactNote          = workflows.ArtifactNote
	ElisionNote           = workflows.ElisionNote
	TruncateOutput        = workflows.TruncateOutput
	TruncateOutputTo      = workflows.TruncateOutputTo
	ArtifactRunPattern    = workflows.ArtifactRunPattern
)
