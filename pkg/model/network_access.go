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

package model

import (
	"strconv"
	"strings"
)

// NetworkAccess is how much of the internet a task's sandbox may reach
// while its run lasts (grain/task-269): NetworkOpen, everything the host
// itself can reach, or NetworkNone, nothing beyond the two things a
// sandbox cannot work without -- the control link its tool calls arrive
// on, and this deployment's own git proxy, which every checkout clones
// through.
//
// It is a per-task override (Task.NetworkAccess) over a deployment-wide
// default (Config.NetworkAccess), with the two declarations that file
// tasks on their own able to fix it for everything they file
// (Template.NetworkAccess, and Schedule.NetworkAccess for a schedule
// carrying its own inline content) -- the same three layers
// AgentFramework already has, and the same "zero means unset" contract:
// "" on a task or a template means "no override, take the deployment's
// answer", and "" on the deployment means NetworkOpen, which is what
// every sandbox grain ever built had before this setting existed.
//
// What NetworkNone is *for* is a job whose whole business is inside the
// deployment -- the memory agent reads a git repository and writes
// memory back through grain's own daemon, and has no reason to reach
// anything else. What it is not is a hypervisor-level cut: see
// orchestrator.GranuleSandboxes' own enforcement (and docs/design.md) for
// exactly how far it reaches, which is one narrowing of
// docs/design.md's egress_policy rather than an airgap.
//
// A string rather than a bool because "networked" is not the only other
// answer worth having: docs/design.md's egress_policy already imagines
// an allowlist, and a vocabulary that can grow a third word is what
// stops that arriving as a second boolean beside this one.
type NetworkAccess string

const (
	// NetworkOpen is a sandbox with the network it has always had: it
	// reaches whatever the host it is built on reaches. The deployment
	// default for a deployment that has never chosen.
	NetworkOpen NetworkAccess = "open"
	// NetworkNone is a sandbox with no route off its own segment --
	// no package index, no API, no repository host except through
	// grain's own git proxy. Only a real VM backend can give one
	// (orchestrator.GranuleSandboxes); the host-directory backend refuses
	// to acquire for a task asking for it rather than handing back a
	// sandbox with the daemon's own network.
	NetworkNone NetworkAccess = "none"
)

// NormalizeNetworkAccess is the deployment-wide reading of "": there is
// no "no answer" for a deployment, so an unset Config.NetworkAccess is
// NetworkOpen. Everything else is returned unchanged, including a value
// that names nothing -- the caller (ui.UpdateSettings) is the one to
// reject that.
//
// The per-task and per-template readings deliberately have no normalizer
// of their own: "" is a value in its own right there ("no override"),
// and folding it into a word would pin every task ever created to one.
func NormalizeNetworkAccess(v NetworkAccess) NetworkAccess {
	if v == "" {
		return NetworkOpen
	}
	return v
}

// NetworkAccesses is the vocabulary above as a list, in the order a
// human should be offered it: the deployment default first. Written once
// here so the pickers, the validators and the error messages cannot name
// different lists (AgentFrameworks' own reasoning).
func NetworkAccesses() []NetworkAccess {
	return []NetworkAccess{NetworkOpen, NetworkNone}
}

// ValidNetworkAccess reports whether v names an access grain can
// actually build a sandbox for. "" is not one: a caller that means "no
// override, use the deployment's own" has to say so itself, since the
// two readings differ by caller (Config.NetworkAccess's zero value is an
// access, Task.NetworkAccess's is not).
func ValidNetworkAccess(v NetworkAccess) bool {
	for _, name := range NetworkAccesses() {
		if v == name {
			return true
		}
	}
	return false
}

// NetworkAccessNames renders the vocabulary for an error message a human
// reads -- `"open" or "none"` -- so the sentence that rejects a bad value
// cannot name a shorter list than the check that rejected it.
func NetworkAccessNames() string {
	names := NetworkAccesses()
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(string(n))
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}
