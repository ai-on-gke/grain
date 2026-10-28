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

// Package installtracktest registers a made-up install track, "acme", for
// core tests of code that behaves differently on a registered track: core
// grain registers none of its own, and core tests never rely on an
// edition's.
package installtracktest

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

const (
	// Track is the made-up track.
	Track installtrack.Track = "acme"
	// Repository is its default image repository.
	Repository = "registry.example/acme/acme-grain"
	// MovedRepository is a repository its deployments move off, onto
	// Repository.
	MovedRepository = "registry.example/acme/old-acme-grain"
	// OptInRepository is a repository its deployments may choose but
	// never default to.
	OptInRepository = "mirror.example/acme/grain"
	// RepositoryEnv overrides Repository.
	RepositoryEnv = "GRAIN_ACME_IMAGE_REPO"
	// HostSuffix marks a host as one of its installs.
	HostSuffix = ".acme.example"
	// Signal is what Detection.Signals reports for such a host.
	Signal = "host:*.acme.example"
	// AgyPath marks a unit's -agy-path as one of its installs.
	AgyPath = "/opt/acme/bin/agy"
	// SignalAgyPath is what Detection.Signals reports for it.
	SignalAgyPath = "unit:-agy-path=/opt/acme"
)

// Definition is the made-up track.
var Definition = installtrack.Definition{
	Track:             Track,
	DefaultRepository: Repository,
	RepositoryEnv:     RepositoryEnv,
	MovesFrom:         []string{installtrack.LegacyGrainRepository, MovedRepository},
	Successors:        []string{OptInRepository},
	Detect: func(f installtrack.Facts) []string {
		var signals []string
		if h := strings.ToLower(strings.TrimSuffix(f.Hostname, ".")); strings.HasSuffix(h, HostSuffix) && len(h) > len(HostSuffix) {
			signals = append(signals, Signal)
		}
		if strings.HasPrefix(f.AgyPath, "/opt/acme/") {
			signals = append(signals, SignalAgyPath)
		}
		return signals
	},
}

// Register registers the made-up track. It is idempotent, and is meant
// for a test package's init (or TestMain).
func Register() { installtrack.RegisterTrack(Definition) }
