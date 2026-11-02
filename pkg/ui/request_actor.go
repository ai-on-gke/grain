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
	"net"
	"net/http"
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
// OnBehalfOf is always nil until spec §2.4 lands.
func RequestActor(ctx context.Context) model.Attribution {
	if p, ok := PrincipalFromContext(ctx); ok {
		return model.Attribution{Actor: p}
	}
	op := operatorActor(ctx)
	op.Source = model.SourceInProcess
	if ctx != nil {
		if src, ok := ctx.Value(requestSourceContextKey{}).(model.AuthSource); ok && src != "" {
			op.Source = src
		}
	}
	return model.Attribution{Actor: op}
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
