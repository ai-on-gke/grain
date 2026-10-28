package ci

import (
	gritartifacts "github.com/bwsalmon/grain/pkg/grit/artifacts"
	gritcache "github.com/bwsalmon/grain/pkg/grit/cache"
	gritsteps "github.com/bwsalmon/grain/pkg/grit/steps"
	"github.com/bwsalmon/grain/pkg/workflows"
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
