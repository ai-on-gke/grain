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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A deployment set up before grain shipped its sandbox PriorityClasses,
// and upgraded without re-applying deploy/kubernetes/, has a cluster with
// none of the classes its settings name -- and the API server refuses
// every pod that names a class it does not have. So both Kubernetes
// backends check the classes exist (reconcileK8sPriorityClasses, at the
// first cycle and every so often after), create grain's own when they
// are missing (k8s.EnsurePriorityClasses), and create pods with *no*
// class for any name that is still missing rather than failing them,
// with an alert saying what to apply. A class only matters on a full
// cluster; a pod that cannot be created at all matters on every one.

// k8sPriorityClassRecheck is how often classes already found present are
// checked again, and k8sPriorityClassMissingRecheck how often while any
// is missing -- the shorter, so an operator who applies the manifest
// sees pods get their class back, and the alert clear, within a minute
// or so rather than ten.
const (
	k8sPriorityClassRecheck        = 10 * time.Minute
	k8sPriorityClassMissingRecheck = time.Minute
)

// k8sPriorityGuard is the part of a Kubernetes backend that knows which
// PriorityClasses its pods may not name: those the cluster was found
// not to have, by a check or by a create the API server refused for it.
// Shared by K8sSandboxes and K8sGranuleSandboxes. The zero value is
// usable and lets every class through.
type k8sPriorityGuard struct {
	mu        sync.Mutex
	missing   map[string]bool
	checkedAt time.Time
}

// resolve is the priorityClassName a pod that wants name is created
// with: name itself, or "" while the cluster is known not to have it --
// unless strict, which always names it, and so fails the create with
// k8s.ErrNoPriorityClass as it did before there was a fallback.
func (g *k8sPriorityGuard) resolve(name string, strict bool) string {
	if strict || name == "" {
		return name
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.missing[name] {
		return ""
	}
	return name
}

// markMissing records that the cluster has no class named name.
func (g *k8sPriorityGuard) markMissing(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.missing == nil {
		g.missing = map[string]bool{}
	}
	if !g.missing[name] {
		log.Printf("orchestrator: the cluster has no PriorityClass %q; sandbox pods that name it are created without one until it exists", name)
	}
	g.missing[name] = true
}

// due reports whether the classes should be checked again as of now.
func (g *k8sPriorityGuard) due(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.checkedAt.IsZero() {
		return true
	}
	every := k8sPriorityClassRecheck
	if len(g.missing) > 0 {
		every = k8sPriorityClassMissingRecheck
	}
	return now.Sub(g.checkedAt) >= every
}

// apply folds a check's report in: what is there now is let through
// again, what is missing is held back, and what could not be read is
// left as it was -- an unreadable class is not evidence either way, and
// a create that is refused for it marks it missing soon enough.
func (g *k8sPriorityGuard) apply(report k8s.PriorityClassReport, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.missing == nil {
		g.missing = map[string]bool{}
	}
	for _, name := range append(append([]string(nil), report.Present...), report.Created...) {
		delete(g.missing, name)
	}
	for _, name := range report.Missing {
		g.missing[name] = true
	}
	g.checkedAt = now
}

// missingNames is the classes pods are being created without, sorted.
func (g *k8sPriorityGuard) missingNames() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	names := make([]string, 0, len(g.missing))
	for name := range g.missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// createPod is client.Create for a pod whose opts already carry
// resolve's class, falling back to a pod with no class when the API
// server refuses it for naming a class it does not have. strict turns
// the fallback off and returns that refusal (k8s.ErrNoPriorityClass)
// as it is.
func (g *k8sPriorityGuard) createPod(ctx context.Context, client k8s.Client, name string, shape k8s.PodShape,
	opts k8s.PodOptions, strict bool) error {

	err := client.Create(ctx, name, shape, opts)
	if err == nil || strict || opts.PriorityClassName == "" || !errors.Is(err, k8s.ErrNoPriorityClass) {
		return err
	}
	g.markMissing(opts.PriorityClassName)
	opts.PriorityClassName = ""
	return client.Create(ctx, name, shape, opts)
}

// K8sPriorityClassStatus is what one check of a backend's classes found.
type K8sPriorityClassStatus struct {
	// Created were grain's own, missing, and created by this check.
	Created []string
	// Missing are the classes the backend's pods are created without,
	// because the cluster does not have them.
	Missing []string
	// Unknown could not be read (k8s.PriorityClassReport.Unknown).
	Unknown []string
	// Errors says what went wrong, per name in Missing or Unknown that
	// this check itself tried.
	Errors map[string]error
	// Manifest is the file in deploy/kubernetes/ that defines grain's
	// classes for this backend.
	Manifest string
}

// K8sPriorityClassEnsurer is a Sandboxes backend whose pods name
// PriorityClasses: both Kubernetes backends.
type K8sPriorityClassEnsurer interface {
	// EnsurePriorityClasses checks, and where it can creates, the
	// classes this backend's pods name, if a check is due as of now. ran
	// is false when it was not, and nothing was asked of the cluster.
	EnsurePriorityClasses(ctx context.Context, now time.Time) (status K8sPriorityClassStatus, ran bool)
}

// ensureK8sPriorityClasses is EnsurePriorityClasses for either backend.
func ensureK8sPriorityClasses(ctx context.Context, guard *k8sPriorityGuard, client k8s.Client,
	classes K8sPriorityClasses, manifest string, now time.Time) (K8sPriorityClassStatus, bool) {

	if !guard.due(now) {
		return K8sPriorityClassStatus{}, false
	}
	report := client.EnsurePriorityClasses(ctx, []string{classes.Interactive, classes.Agent, classes.CI})
	for _, name := range report.Created {
		log.Printf("orchestrator: created PriorityClass %q, which sandbox pods name and the cluster did not have", name)
	}
	for _, name := range report.Unknown {
		log.Printf("orchestrator: could not tell whether the cluster has PriorityClass %q: %v", name, report.Errors[name])
	}
	guard.apply(report, now)
	return K8sPriorityClassStatus{
		Created:  report.Created,
		Missing:  guard.missingNames(),
		Unknown:  report.Unknown,
		Errors:   report.Errors,
		Manifest: manifest,
	}, true
}

// FamilySandboxPriorityClass is the notification family of a sandbox
// PriorityClass the cluster does not have.
const FamilySandboxPriorityClass = "sandbox.priority-class"

// k8sPriorityClassSubject is the one notification this raises: it is
// about the deployment's cluster, of which there is one.
var k8sPriorityClassSubject = model.NotificationSubject(FamilySandboxPriorityClass, "missing")

var priorityClassNotifier = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "k8s-priority-classes"})

// reconcileK8sPriorityClasses checks the PriorityClasses a Kubernetes
// backend's pods name, creating grain's own where they are missing, and
// keeps an operator notification open for as long as any is still
// missing. Placed ahead of everything that creates a pod, so the first
// cycle after a start -- or an upgrade's rollout -- has its classes in
// place before it dispatches anything.
func reconcileK8sPriorityClasses(ctx context.Context, deps Deps, now time.Time) error {
	ensurer, ok := deps.Sandboxes.(K8sPriorityClassEnsurer)
	if !ok {
		return nil
	}
	status, ran := ensurer.EnsurePriorityClasses(ctx, now)
	if !ran || deps.Store == nil {
		return nil
	}
	if len(status.Missing) == 0 {
		if len(status.Unknown) > 0 {
			// Nothing known to be missing, but not everything known to
			// be there either: an open alert stays open until a check
			// can say.
			return nil
		}
		return clearPipelineNotification(ctx, deps.Store, k8sPriorityClassSubject, priorityClassNotifier)
	}
	n := k8sPriorityClassNotification(status)
	open, err := deps.Store.OpenNotification(ctx, n.Subject)
	if err != nil {
		return err
	}
	if open != nil && open.Title == n.Title {
		return nil
	}
	_, err = raisePipelineNotification(ctx, deps.Store, n, now)
	return err
}

// k8sPriorityClassNotification says which classes are missing, that
// pods are running without them, and the exact command that defines
// them.
func k8sPriorityClassNotification(status K8sPriorityClassStatus) model.Notification {
	var ours, custom []string
	for _, name := range status.Missing {
		if _, ok := k8s.SandboxPriorityClass(name); ok {
			ours = append(ours, name)
		} else {
			custom = append(custom, name)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The cluster has no PriorityClass %s, which Settings -> Sandbox names for sandbox pods. "+
		"grain is creating those pods with no priority class instead of failing them, so on a full cluster an "+
		"interactive session cannot preempt CI jobs to make room.", quoteJoin(status.Missing))
	if len(ours) > 0 {
		fmt.Fprintf(&b, "\n\ngrain could not create %s itself", quoteJoin(ours))
		if err := firstError(status.Errors, ours); err != nil {
			fmt.Fprintf(&b, " (%v)", err)
		}
		fmt.Fprintf(&b, ". PriorityClasses are cluster-scoped, so its ServiceAccount needs the "+
			"`grain-priorityclasses` ClusterRole the manifest ships. Apply the manifest this deployment was "+
			"installed from, which defines both:\n\n```\nkubectl apply -f deploy/kubernetes/%s\n```", status.Manifest)
	}
	if len(custom) > 0 {
		verb := "are"
		if len(custom) == 1 {
			verb = "is"
		}
		fmt.Fprintf(&b, "\n\n%s %s not one of grain's own classes, so grain does not create it: create it in the "+
			"cluster (`kubectl create priorityclass <name> --value=<priority>`), or change or clear the name in "+
			"Settings -> Sandbox.", quoteJoin(custom), verb)
	}
	b.WriteString("\n\nThis clears by itself once the classes exist.")
	return model.Notification{
		Subject:  k8sPriorityClassSubject,
		Severity: model.NotificationWarning,
		Title:    "Sandbox pods are running without a PriorityClass: the cluster has no " + strings.Join(status.Missing, ", "),
		Body:     b.String(),
		RaisedBy: priorityClassNotifier,
	}
}

func quoteJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

func firstError(errs map[string]error, names []string) error {
	for _, name := range names {
		if err := errs[name]; err != nil {
			return err
		}
	}
	return nil
}
