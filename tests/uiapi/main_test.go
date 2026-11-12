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

// Package uiapi holds the server half of the classic Playwright suite
// (ui/e2e), ported to Go against the seeded demo pkg/demo/demotest starts:
// one file per area of docs/test-plans/classic-ui-e2e-migration, each
// test named TestClassic_<spec>_<what> with a "ported from
// ui/e2e/<spec>:<line>" comment, which classification.csv's ported_to
// column names back.
package uiapi

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

func TestMain(m *testing.M) { demotest.Main(m) }
