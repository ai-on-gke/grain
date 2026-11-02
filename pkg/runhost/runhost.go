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

// Package runhost runs one agent run's CLI, and the "grain mcpserver" it
// forks, in a run host: a container that is a sibling of the daemon
// rather than a child of it, so that restarting the daemon does not kill
// the run (design/design/non-disruptive-upgrade.design.md, "Run hosts").
//
// The daemon creates, lists and destroys run hosts through a Driver. It
// never parents them, and it never calls into one: a run host's whole
// output is its spool (spool.go), an append-only directory the daemon
// tails from a cursor it keeps in the store, and a run host never opens
// the store. Everything a new daemon needs to recognise a run host it did
// not start is in its labels (Labels), which is what the adoption pass
// (adopt.go) decides on.
//
// What is here is the contract and the single-machine docker driver.
// agent.LocalHost stays what tests and a development daemon use.
package runhost

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Protocol is the run-host protocol this build speaks: the spool layout,
// the labels and the entrypoint's arguments. A change to any of them that
// an older daemon would misread is a new Protocol, never an edit of this
// one.
const Protocol = 1

// Supported reports whether a daemon of this build may adopt a run host
// that speaks protocol p. Skew is N-1 (design/design/non-disruptive-upgrade.design.md,
// "Version skew is never a stranding"): this build adopts its own
// protocol and the one before, and refuses the next one by name because
// a rolled-back daemon must not misread what a newer build wrote.
func Supported(p int) bool { return p == Protocol || (p == Protocol-1 && p > 0) }

// Label keys. Every key is under one prefix so List can select a
// deployment's run hosts with one filter, and a person reading `docker
// ps` can tell what a container is.
const (
	LabelPrefix     = "dev.grain.runhost"
	LabelDeployment = LabelPrefix + ".deployment"
	LabelTask       = LabelPrefix + ".task"
	LabelRun        = LabelPrefix + ".run"
	LabelGeneration = LabelPrefix + ".generation"
	LabelProtocol   = LabelPrefix + ".protocol"
	LabelDeadline   = LabelPrefix + ".deadline"
	LabelSpool      = LabelPrefix + ".spool"
)

// Labels are what a run host carries about itself: the whole of what a
// daemon that did not create it needs to adopt it or to refuse it.
type Labels struct {
	// Deployment is the daemon's sandbox prefix. Two deployments sharing
	// one docker host must never adopt, or free, each other's run hosts.
	Deployment string
	// Task and Run are the task and the task_run row that claim it.
	Task string
	Run  string
	// Generation is the build that created it (its version string), for
	// the post-upgrade report.
	Generation string
	// Protocol is the run-host protocol it speaks (see Protocol).
	Protocol int
	// Deadline is the hard ceiling the run host enforces on itself: the
	// run's budget plus an adoption allowance. It is a ceiling for when no
	// daemon is there to enforce the budget, not the budget itself, and
	// docker labels cannot be changed after creation.
	Deadline time.Time
	// Spool is the run host's spool directory, a path the daemon can
	// read at the same place.
	Spool string
}

// Map is l as docker labels.
func (l Labels) Map() map[string]string {
	return map[string]string{
		LabelDeployment: l.Deployment,
		LabelTask:       l.Task,
		LabelRun:        l.Run,
		LabelGeneration: l.Generation,
		LabelProtocol:   strconv.Itoa(l.Protocol),
		LabelDeadline:   l.Deadline.UTC().Format(time.RFC3339),
		LabelSpool:      l.Spool,
	}
}

// errUnlabelled is ParseLabels' answer for a container that is not a run
// host at all.
var errUnlabelled = errors.New("runhost: not a run host (no protocol label)")

// ParseLabels reads Labels back off a container's labels. A protocol label
// that is present but unreadable is returned as protocol 0, which no
// build supports, so the adoption pass refuses it legibly rather than
// dropping it from the listing and leaving it running unwatched.
func ParseLabels(m map[string]string) (Labels, error) {
	raw, ok := m[LabelProtocol]
	if !ok {
		return Labels{}, errUnlabelled
	}
	l := Labels{
		Deployment: m[LabelDeployment],
		Task:       m[LabelTask],
		Run:        m[LabelRun],
		Generation: m[LabelGeneration],
		Spool:      m[LabelSpool],
	}
	l.Protocol, _ = strconv.Atoi(raw)
	if d, err := time.Parse(time.RFC3339, m[LabelDeadline]); err == nil {
		l.Deadline = d
	}
	return l, nil
}

// Info is one run host as a Driver lists it.
type Info struct {
	// Name is the driver's own handle for it (the container name).
	Name   string
	Labels Labels
	// Running is false for a run host whose process has exited and which
	// has not been destroyed yet: its spool is final.
	Running bool
}

// Mount is one directory made visible inside a run host at the same path
// it has on the machine that created it. A Mount is always a directory:
// a CLI that refreshes a login by renaming a new file over the old one
// replaces the inode, and a bind mount of the single file would pin the
// old one (design/design/non-disruptive-upgrade.design.md, "The shared credential
// directory"). Spec.Validate refuses anything else.
type Mount struct {
	Path     string
	ReadOnly bool
}

// Spec is everything a Driver needs to create one run host.
type Spec struct {
	Name   string
	Image  string
	Labels Labels
	Mounts []Mount
	// User is "uid:gid" to run as, or "" for the image's own user.
	User string
	// Command is the entrypoint's argv: the grain binary inside the
	// image, then "runhost", "exec" and its flags.
	Command []string
}

// Validate refuses a Spec that would break the run host's contract.
func (s Spec) Validate(isDir func(string) bool) error {
	if s.Name == "" || s.Image == "" || len(s.Command) == 0 {
		return fmt.Errorf("runhost: spec needs a name, an image and a command")
	}
	if s.Labels.Run == "" || s.Labels.Deployment == "" {
		return fmt.Errorf("runhost: spec %s needs a run and a deployment label", s.Name)
	}
	for _, m := range s.Mounts {
		if !isDir(m.Path) {
			return fmt.Errorf("runhost: %s is not a directory; a run host is only ever given whole directories, "+
				"because a single-file bind mount pins an inode a refreshing CLI replaces", m.Path)
		}
	}
	return nil
}
