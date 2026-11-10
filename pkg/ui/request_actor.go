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

package ui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Who a request acts as (operator-attribution spec §2).
//
// This file is the only place in pkg/ui that reads the deployment's own
// actor (Client.Config.Actor) or decides who a request is from:
// TestNothingOutsideRequestActorReadsTheActor fails on a reference to
// Config.Actor, requestUser, actingUser or teamActingUser anywhere else.
// Every write records RequestActor(ctx); every check that wants "the
// signed-in person, if any" asks requestPerson.

type requestSourceContextKey struct{}
type operatorActorContextKey struct{}
type onBehalfOfContextKey struct{}

// withRequestSource records how serveHTTP admitted a request that names
// nobody: the access code, break-glass over loopback, or in-process.
func withRequestSource(ctx context.Context, src model.AuthSource) context.Context {
	return context.WithValue(ctx, requestSourceContextKey{}, src)
}

// withOperatorActor records the deployment's own actor, for a request
// that names nobody to run as. A context that already carries one keeps
// it.
func withOperatorActor(ctx context.Context, p model.Principal) context.Context {
	if _, ok := ctx.Value(operatorActorContextKey{}).(model.Principal); ok {
		return ctx
	}
	return context.WithValue(ctx, operatorActorContextKey{}, p)
}

// withOnBehalfOf records who the agent the gate admitted acts for (spec
// §2.4). Only agentAPIGate calls it, with what agentOnBehalfOf read from
// the store: nothing a caller sends -- a header, a body field -- ever
// reaches it, so an agent cannot claim a person (§6.2). nil leaves ctx
// as it was.
func withOnBehalfOf(ctx context.Context, p *model.Principal) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, onBehalfOfContextKey{}, *p)
}

// operatorActor is the deployment's own actor withOperatorActor recorded,
// and DefaultActor's where nothing did, with no source.
func operatorActor(ctx context.Context) model.Principal {
	if ctx != nil {
		if p, ok := ctx.Value(operatorActorContextKey{}).(model.Principal); ok && p != (model.Principal{}) {
			p.Source = ""
			return p
		}
	}
	return DefaultActor("")
}

// RequestActor is who the request in ctx acts as, with how that was
// established (spec §2.2):
//
//   - the principal serveHTTP or the agent API gate attached -- a
//     verified person, a session or run acting through its grants, an
//     external agent token -- with its Source;
//   - otherwise the deployment's own actor, with the source serveHTTP
//     admitted the request by (access-code, loopback), or in-process for
//     a caller that never went through serveHTTP at all.
//
// OnBehalfOf is the person an agent or delegated service principal acts
// for: a delegated registry caller's person (AU-26), an external agent
// credential's owner (model.ExternalAgentToken.Owner, #XC-14), or the
// person the agent API gate derived from grain's own records
// (withOnBehalfOf, spec §2.4), and nil for anybody else.
func RequestActor(ctx context.Context) model.Attribution {
	if p, ok := PrincipalFromContext(ctx); ok {
		if del := delegatedIdentityFromContext(ctx); del != nil &&
			p.Kind == model.PrincipalHuman && p.Source == model.SourceHub && strings.EqualFold(p.ID, del.PersonEmail) {
			return model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalService, ID: del.AppEmail, Source: model.SourceHub},
				OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: del.PersonEmail, Source: model.SourceHub},
			}
		}
		if p.Kind == model.PrincipalAgent && p.Source == model.SourceExternalToken {
			if tok := externalTokenFromContext(ctx); tok != nil {
				return model.Attribution{Actor: p, OnBehalfOf: tok.OwnerPrincipal()}
			}
		}
		who := model.Attribution{Actor: p}
		if p.Kind == model.PrincipalAgent {
			if b, ok := ctx.Value(onBehalfOfContextKey{}).(model.Principal); ok && b.Kind == model.PrincipalHuman && b.Verified() {
				who.OnBehalfOf = &b
			}
		}
		return who
	}
	if del := delegatedIdentityFromContext(ctx); del != nil {
		return model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalService, ID: del.AppEmail, Source: model.SourceHub},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: del.PersonEmail, Source: model.SourceHub},
		}
	}
	op := operatorActor(ctx)
	op.Source = model.SourceInProcess
	if ctx != nil {
		if src, ok := ctx.Value(requestSourceContextKey{}).(model.AuthSource); ok && src != "" {
			op.Source = src
		}
	}
	if op.Source == model.SourceAccessCode {
		// Whoever presented the code, as they named themselves to it
		// (access_code_mode.go): still the code's power, and still an
		// unverified source, but no longer nobody.
		if name := accessCodeNameFromContext(ctx); name != "" && op.Kind == model.PrincipalHuman {
			op = model.Principal{Kind: model.PrincipalHuman, ID: AccessCodeActorPrefix + name, Source: model.SourceAccessCode}
		}
	}
	return model.Attribution{Actor: op}
}

// deploymentActorOf is actor, RequestActor's for some request, as the
// deployment's own actor where it is a named access-code session's: what
// /api/config reports as the actor a CLI configures itself with, which is
// the deployment's, not whoever signed in with the code.
func deploymentActorOf(ctx context.Context, actor model.Principal) model.Principal {
	if actor.Source != model.SourceAccessCode || !strings.HasPrefix(actor.ID, AccessCodeActorPrefix) {
		return actor
	}
	op := operatorActor(ctx)
	op.Source = actor.Source
	return op
}

// requestActor is RequestActor for a Client method, which may be called
// in-process with nothing on its context: it runs as this Client's own
// actor then.
func (c *Client) requestActor(ctx context.Context) model.Attribution {
	if ctx == nil {
		ctx = context.Background()
	}
	return RequestActor(c.withOperatorActor(ctx))
}

// withOperatorActor is withOperatorActor with this Client's own actor:
// what serveHTTP runs a request that names nobody as.
func (c *Client) withOperatorActor(ctx context.Context) context.Context {
	return withOperatorActor(ctx, c.Config.Actor)
}

// operatorID is the deployment's own actor's id, for the allowlist that
// always admits it.
func (c *Client) operatorID() string {
	return strings.TrimSpace(c.Config.Actor.ID)
}

// trustedRequestSource is how singleTenantTrusted admitted r: by the
// access code (as a bearer token or an access session), as an in-process
// request (no RemoteAddr), or otherwise as a loopback break-glass caller.
func trustedRequestSource(r *http.Request) model.AuthSource {
	cfg, _ := IAPConfigFromContext(r.Context())
	if presentsAccessCode(r, cfg) {
		return model.SourceAccessCode
	}
	if _, ok := accessSessionIssued(r, cfg); ok {
		return model.SourceAccessCode
	}
	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if remoteAddr == "" {
		return model.SourceInProcess
	}
	// This package's own tests stand in for an in-process caller from
	// httptest's TEST-NET-1 range (testSourceNets), and record as one.
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, n := range testSourceNets {
			if n.Contains(ip) {
				return model.SourceInProcess
			}
		}
	}
	return model.SourceLoopback
}

// requestPerson is the person RequestActor(ctx) names -- a human or
// service principal some verifier established -- and "" for an agent or
// for the operator fallback. A user named in a request body is never
// believed (grain#2379, grain/task-2078): anything able to reach the
// daemon over loopback could otherwise be any team admin or system admin
// by typing their name.
func requestPerson(ctx context.Context) string {
	p, ok := PrincipalFromContext(ctx)
	if !ok || (p.Kind != model.PrincipalHuman && p.Kind != model.PrincipalService) {
		return ""
	}
	return strings.TrimSpace(p.ID)
}

// requestPersonOrOperator is requestPerson, falling back to the
// deployment's own actor's id when that is a person: who a write such as
// filing a task is submitted by.
func (c *Client) requestPersonOrOperator(ctx context.Context) string {
	if user := requestPerson(ctx); user != "" {
		return user
	}
	return c.operatorPerson()
}

// operatorPerson is the deployment's own actor's id when that is a
// person, and "" otherwise.
func (c *Client) operatorPerson() string {
	if c.Config.Actor.Kind == model.PrincipalHuman {
		return strings.TrimSpace(c.Config.Actor.ID)
	}
	return ""
}

// requestPersonOrTrustedOperator is requestPersonOrOperator for a request
// with no principal at all: an agent in the context never falls back to
// the deployment's actor, since it is no owner.
func (c *Client) requestPersonOrTrustedOperator(ctx context.Context) string {
	if _, named := PrincipalFromContext(ctx); named {
		return requestPerson(ctx)
	}
	return c.requestPersonOrOperator(ctx)
}

// DeploymentOwnerEnv is the environment variable fallback for -deployment-owner.
const DeploymentOwnerEnv = "GRAIN_DEPLOYMENT_OWNER"

// NormalizeDeploymentOwner validates and normalizes -deployment-owner
// ($GRAIN_DEPLOYMENT_OWNER) to a lowercase human email address
// ("alice@example.com"). An optional "user:" prefix is accepted and stripped;
// empty input returns ("", nil).
func NormalizeDeploymentOwner(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if prefix, rest, ok := strings.Cut(s, ":"); ok {
		if !strings.EqualFold(strings.TrimSpace(prefix), "user") {
			return "", fmt.Errorf("invalid -deployment-owner %q: must be a user email address, not %s:", raw, strings.TrimSpace(prefix))
		}
		s = strings.TrimSpace(rest)
	}
	s = strings.ToLower(s)
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(s, " \t\r\n,") || strings.Contains(domain, "@") || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("invalid -deployment-owner %q: must be an email address", raw)
	}
	if strings.HasSuffix(domain, ".gserviceaccount.com") {
		return "", fmt.Errorf("invalid -deployment-owner %q: must be a human user email address, not a service account", raw)
	}
	return s, nil
}

// explicitDeploymentOwnerEmail returns the normalized -deployment-owner
// ($GRAIN_DEPLOYMENT_OWNER) email when configured, or "" otherwise.
func (c *Client) explicitDeploymentOwnerEmail() string {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.Config.DeploymentOwner)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(DeploymentOwnerEnv))
	}
	owner, err := NormalizeDeploymentOwner(raw)
	if err != nil {
		return ""
	}
	return owner
}

// deploymentOwnerEmail returns the explicit -deployment-owner email when set,
// else the deployment's own actor's email when Config.Actor names an email
// address.
func (c *Client) deploymentOwnerEmail() string {
	if owner := c.explicitDeploymentOwnerEmail(); owner != "" {
		return owner
	}
	if c != nil {
		if owner := c.operatorPerson(); strings.Contains(owner, "@") {
			return strings.ToLower(owner)
		}
	}
	return ""
}

// IsDeploymentOwner reports whether ctx acts as the deployment owner:
//
//   - Never an agent or service account.
//   - Never a verified session holding a limited RBAC role (even one granted
//     capabilities:admin).
//   - An un-named trusted caller (the deployment access code, loopback
//     break-glass, or an in-process Client call) acts as the deployment owner.
//   - A verified human holding full owner access (grain/operator, role.Full)
//     is the deployment owner when neither -deployment-owner nor Config.Actor
//     names an email address (the default OS account), or when their verified
//     email matches -deployment-owner (or Config.Actor when -deployment-owner
//     is unset).
func (c *Client) IsDeploymentOwner(ctx context.Context) bool {
	if c == nil {
		return false
	}
	if role := accessRoleFromContext(ctx); role != nil && !role.Full {
		return false
	}
	p, named := PrincipalFromContext(ctx)
	if !named {
		return true
	}
	if p.Kind != model.PrincipalHuman {
		return false
	}
	if owner := c.deploymentOwnerEmail(); owner != "" {
		return strings.EqualFold(strings.TrimSpace(p.ID), owner)
	}
	return true
}

func (c *Client) requireDeploymentOwner(ctx context.Context, action string) error {
	if c.IsDeploymentOwner(ctx) {
		return nil
	}
	if owner := c.deploymentOwnerEmail(); owner != "" {
		return &ForbiddenError{err: fmt.Errorf("only the deployment owner (%s) may %s", owner, action)}
	}
	return &ForbiddenError{err: fmt.Errorf("only the deployment owner may %s", action)}
}
