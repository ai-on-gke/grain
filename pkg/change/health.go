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

package change

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CredentialRecheckInterval is how often a kind blocked on expired
// credentials is checked again: one cheap Check, and nothing else polls
// it meanwhile (changelists.md §8.3).
const CredentialRecheckInterval = 5 * time.Minute

// Checker is a provider that can cheaply check whether its forge can be
// reached with the operator's credentials.
type Checker interface {
	Check(ctx context.Context) error
}

// Notice is the operator notification a provider's expired credentials
// raise, which clears itself when a Check passes.
type Notice struct {
	Subject string
	Title   string
	Body    string
}

// CredentialNoticer is a provider that says what its expired credentials
// look like to the operator.
type CredentialNoticer interface {
	CredentialNotice() Notice
}

// Notifier raises and clears operator notifications: the daemon's store.
type Notifier interface {
	RaiseNotification(ctx context.Context, n model.Notification) (model.NotificationRaise, error)
	ClearNotification(ctx context.Context, subject, by string) (bool, error)
}

// raisedBy is the principal a credential notification is raised as.
const raisedBy = "automation:change-health"

func notice(p Provider) Notice {
	if n, ok := p.(CredentialNoticer); ok {
		return n.CredentialNotice()
	}
	return Notice{
		Subject: "credentials-expired." + string(p.Kind()),
		Title:   fmt.Sprintf("The operator's credentials for %s changes have expired", p.Kind()),
	}
}

// HealthKind is the kind p's deployment health is recorded under
// (HealthOf): its Opener's target kind, or its link kind.
func HealthKind(p Provider) string { return targetKind(p) }

func targetKind(p Provider) string {
	if o, ok := p.(Opener); ok && o.TargetKind() != "" {
		return o.TargetKind()
	}
	return string(p.Kind())
}

// NoteError records what err says about p's deployment health: an
// expired credential blocks the kind and raises p's notice (once). Any
// other error, or none, changes nothing -- only a passing Check clears
// the block.
func NoteError(ctx context.Context, p Provider, err error, n Notifier, now time.Time) {
	if !IsCredentialsExpired(err) {
		return
	}
	kind := targetKind(p)
	if !MarkBlocked(kind, BlockedCredentials, err.Error(), now) || n == nil {
		return
	}
	no := notice(p)
	_, _ = n.RaiseNotification(ctx, model.Notification{
		Subject: no.Subject, Severity: model.NotificationWarning, Title: no.Title, Body: no.Body,
		RaisedBy: raisedBy, RaisedAt: now,
	})
}

// Observe asks the provider for kind about target, unless the
// deployment's health for it is blocked: then nothing is polled and the
// block is the answer, until Recheck clears it.
func Observe(ctx context.Context, task model.Task, kind model.LinkKind, target string, n Notifier, now time.Time) (Observation, error) {
	p, ok := Lookup(kind)
	if !ok {
		return Observation{}, fmt.Errorf("no provider for %s links", kind)
	}
	if h := HealthOf(targetKind(p)); h.Blocked != "" {
		return Observation{}, &CredentialsExpiredError{Kind: targetKind(p), Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339)}
	}
	obs, err := p.Observe(ctx, task, target)
	NoteError(ctx, p, err, n, now)
	return obs, err
}

// Recheck runs one Check for every blocked provider that has one and
// clears the block, and its notification, for each that passes.
func Recheck(ctx context.Context, n Notifier) {
	for _, k := range Kinds() {
		p, _ := Lookup(k)
		kind := targetKind(p)
		if HealthOf(kind).Blocked == "" {
			continue
		}
		c, ok := p.(Checker)
		if !ok {
			continue
		}
		if err := c.Check(ctx); err != nil {
			continue
		}
		if ClearBlocked(kind) && n != nil {
			_, _ = n.ClearNotification(ctx, notice(p).Subject, raisedBy)
		}
	}
}

// Watch runs Recheck every CredentialRecheckInterval until ctx ends.
func Watch(ctx context.Context, n Notifier) {
	t := time.NewTicker(CredentialRecheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Recheck(ctx, n)
		}
	}
}
