//go:build ignore

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

// Command grain_headerverifier runs core grain (pkg/cli.Main) with a
// loopback-only identity verifier, "walk-header", that admits the
// principal named in X-Walk-User. The 2026-11-18 §8 walk uses it to sign
// a browser in as a person holding grain/viewer (item 6), which an
// access-code session -- always the deployment's owner -- cannot be.
// Never for anything but a throwaway loopback daemon.
package main

import (
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/cli"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type headerVerifier struct{}

func (headerVerifier) Name() string      { return "walk-header" }
func (headerVerifier) Headers() []string { return []string{"X-Walk-User"} }

func (headerVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	who := strings.TrimSpace(r.Header.Get("X-Walk-User"))
	if who == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	return ui.VerifiedIdentity{Email: who, Kind: model.PrincipalHuman}, true, nil
}

func main() {
	ui.RegisterIdentityVerifier(headerVerifier{})
	cli.Main()
}
