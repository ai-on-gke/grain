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
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The X-Grain-Client label (design/spec/external-cli/external-cli.spec.md#XC-15):
// the tool and session a client says it runs in, recorded as the FiledVia
// of the tasks and comments it posts. It is the caller's own word about
// itself, so it is shown as provenance and consulted for nothing else --
// no scope, repository, approval or owner-trust decision reads it.

type filedViaContextKey struct{}

// withFiledVia records r's sanitized X-Grain-Client label on its context,
// for CreateTask and the comment writers to store.
func withFiledVia(r *http.Request) *http.Request {
	label := model.SanitizeGrainClient(r.Header.Get(model.GrainClientHeader))
	if label == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), filedViaContextKey{}, label))
}

// filedViaFromContext is the label withFiledVia recorded, "" for none.
func filedViaFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	label, _ := ctx.Value(filedViaContextKey{}).(string)
	return label
}

// provenanceLabel is how a task or comment filed through a labelled
// client reads to a person: "filed from jetski/1.4 by jane@example.com",
// "" when it carries no label.
func provenanceLabel(filedVia, by string) string {
	filedVia = strings.TrimSpace(filedVia)
	if filedVia == "" {
		return ""
	}
	label := "filed from " + filedVia
	if by = strings.TrimSpace(by); by != "" {
		label += " by " + by
	}
	return label
}

// attributedPerson is the person attribution a names: the person it was
// on behalf of, else its actor when that is a person, else "".
func attributedPerson(a model.Attribution) string {
	if b := a.OnBehalfOf; b != nil && (b.Kind == model.PrincipalHuman || b.Kind == model.PrincipalService) {
		return b.ID
	}
	if a.Actor.Kind == model.PrincipalHuman || a.Actor.Kind == model.PrincipalService {
		return a.Actor.ID
	}
	return ""
}
