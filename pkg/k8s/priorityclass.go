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

package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PriorityClassSpec is one of the PriorityClasses grain's sandbox pods
// run at by default: everything the cluster object says beyond its
// name. SandboxPriorityClasses is the one definition of all three --
// deploy/kubernetes/grain.yaml and grain-granule.yaml carry the same
// objects as YAML (tests/deploy checks they agree), `grain init`
// renders its manifest from it, and EnsurePriorityClasses creates
// from it.
type PriorityClassSpec struct {
	Name             string
	Value            int
	PreemptionPolicy string
	Description      string
}

// SandboxPriorityClasses is the three classes grain's sandbox pods name
// by default, highest first. Only the interactive class may preempt;
// the CI class is the lowest, so it is what a full cluster evicts first.
var SandboxPriorityClasses = []PriorityClassSpec{
	{
		Name:             PriorityClassInteractive,
		Value:            1000000,
		PreemptionPolicy: "PreemptLowerPriority",
		Description:      "grain sandbox pods for interactive sessions; may preempt grain agent and CI pods.",
	},
	{
		Name:             PriorityClassAgent,
		Value:            100000,
		PreemptionPolicy: "Never",
		Description:      "grain sandbox pods for agent runs, persistent sandboxes and base-branch repairs.",
	},
	{
		Name:             PriorityClassCI,
		Value:            1000,
		PreemptionPolicy: "Never",
		Description:      "grain sandbox pods for CI jobs; never preempts, and is retried when preempted.",
	},
}

// SandboxPriorityClass is the spec of name, if it is one of grain's own
// SandboxPriorityClasses.
func SandboxPriorityClass(name string) (PriorityClassSpec, bool) {
	for _, spec := range SandboxPriorityClasses {
		if spec.Name == name {
			return spec, true
		}
	}
	return PriorityClassSpec{}, false
}

// Manifest renders spec as the YAML document deploy/kubernetes/ ships
// for it.
func (s PriorityClassSpec) Manifest() string {
	return fmt.Sprintf(`apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: %s
  labels:
    app.kubernetes.io/name: grain
value: %d
globalDefault: false
preemptionPolicy: %s
description: %q
`, s.Name, s.Value, s.PreemptionPolicy, s.Description)
}

// PriorityClassReport is what EnsurePriorityClasses found, one entry
// per distinct name it was asked about.
type PriorityClassReport struct {
	// Present were there already.
	Present []string
	// Created were grain's own and missing, and are there now.
	Created []string
	// Missing are not there and could not be made: a name that is not
	// one of grain's SandboxPriorityClasses, or one whose create the
	// API server refused (PriorityClasses are cluster-scoped, so an
	// identity bound only to its namespace cannot create them).
	Missing []string
	// Unknown could not be read at all -- a get the API server refused
	// or never answered -- so whether they exist is not known.
	Unknown []string
	// Errors says, per name in Missing or Unknown, what went wrong.
	Errors map[string]error
}

// EnsurePriorityClasses makes sure every PriorityClass names lists
// exists in the cluster: each is read with `kubectl get`, and one that
// is not found and is one of grain's own SandboxPriorityClasses is
// created from that spec. Empty names and repeats are skipped. It never
// fails as a whole -- what it could not do is in the report, per name.
func (c Client) EnsurePriorityClasses(ctx context.Context, names []string) PriorityClassReport {
	report := PriorityClassReport{Errors: map[string]error{}}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		exists, err := c.PriorityClassExists(ctx, name)
		switch {
		case err != nil:
			report.Unknown = append(report.Unknown, name)
			report.Errors[name] = err
		case exists:
			report.Present = append(report.Present, name)
		default:
			spec, ours := SandboxPriorityClass(name)
			if !ours {
				report.Missing = append(report.Missing, name)
				report.Errors[name] = fmt.Errorf("%w %q, and it is not one of grain's own to create", ErrNoPriorityClass, name)
				continue
			}
			if err := c.CreatePriorityClass(ctx, spec); err != nil {
				report.Missing = append(report.Missing, name)
				report.Errors[name] = err
				continue
			}
			report.Created = append(report.Created, name)
		}
	}
	return report
}

// PriorityClassExists reports whether the cluster has a PriorityClass
// named name. A refusal or an unreachable API server is an error, not
// a false: neither says the class is missing.
func (c Client) PriorityClassExists(ctx context.Context, name string) (bool, error) {
	if _, err := c.run(ctx, "get", "priorityclass", name, "-o", "name"); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("k8s: reading PriorityClass %q: %w", name, err)
	}
	return true, nil
}

// CreatePriorityClass creates spec's PriorityClass. One that already
// exists -- another grain, or an operator, got there first -- is not an
// error. `create` rather than `apply`, because create is all the RBAC in
// deploy/kubernetes/ grants beyond get: apply would need patch as well,
// and this never changes a class someone else made.
func (c Client) CreatePriorityClass(ctx context.Context, spec PriorityClassSpec) error {
	if _, err := c.runInput(ctx, spec.Manifest(), "create", "--validate=false", "-f", "-"); err != nil {
		if isAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("k8s: creating PriorityClass %q: %w", spec.Name, err)
	}
	return nil
}

// isAlreadyExists reports whether err is kubectl create refusing an
// object that is already there.
func isAlreadyExists(err error) bool {
	var cmdErr *commandError
	if !errors.As(err, &cmdErr) {
		return false
	}
	return strings.Contains(cmdErr.stderr, "AlreadyExists") || strings.Contains(cmdErr.stderr, "already exists")
}
