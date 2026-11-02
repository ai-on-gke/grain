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

// Package capabilitytest registers a made-up capability provider, and a
// made-up in-process capability server method, so that core packages can
// test what they do with a capability contributed from outside core
// (capability.RegisterProvider, capability.Register) without knowing any
// real environment's.
package capabilitytest

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ID, Name and Description are the made-up capability's.
const (
	ID          = "made-up-research"
	Name        = "Made-up research"
	Description = "Let this task look things up in a made-up research service"
)

// Provider is the made-up capability: a grant with nothing to mint.
type Provider struct {
	model.BaseCapability
}

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        ID,
		Label:       Name,
		Description: Description,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Register registers the made-up capability, as a run grant, until t
// ends. A test that calls it must not be parallel: every listing of
// grain's capabilities includes it while it is registered, so a parallel
// test asserting on one of those listings would see it come and go.
func Register(t testing.TB) {
	t.Helper()
	t.Cleanup(capability.RegisterProvider(capability.Registration{
		ID:          ID,
		Name:        Name,
		Description: Description,
		New: func() model.CapabilityProvider {
			return &Provider{}
		},
		RunGrant: true,
	}))
}

// The made-up in-process capability server method (capability.Register):
// MethodServer is its server, MethodRef and MethodGrant its reference and
// recorded grant, and MethodAlias the bare name it retires.
const (
	MethodServer = "https://grain.example/capabilities/made-up"
	MethodName   = "lookup"
	MethodRef    = MethodServer + "#" + MethodName
	MethodGrant  = MethodRef + "={}"
	MethodAlias  = "made-up-lookup"
	MethodLabel  = "Made-up lookup"
)

// RegisterMethod registers the made-up method, as a run grant on by
// default with MethodAlias as its alias, until t ends. Like Register, a
// test that calls it must not be parallel.
func RegisterMethod(t testing.TB) {
	t.Helper()
	t.Cleanup(capability.Register(capability.Method{
		Server:    MethodServer,
		Spec:      MethodSpec(MethodName),
		Name:      MethodLabel,
		RunGrant:  true,
		DefaultOn: true,
		Aliases:   []string{MethodAlias},
	}))
}

// MethodSpec is a made-up method called name, which mints nothing.
func MethodSpec(name string) protocol.MethodSpec {
	return protocol.MethodSpec{
		Name:        name,
		Description: "Let this task look things up in a made-up in-process service",
		Mint: func(context.Context, string, time.Time, map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: func() time.Time { return time.Time{} },
	}
}
