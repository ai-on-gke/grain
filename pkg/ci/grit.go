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
