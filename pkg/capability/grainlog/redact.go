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

package grainlog

import "github.com/ai-on-gke/grain/pkg/redact"

// Redactor is pkg/redact's, which this package's journal was the first
// caller of: the same shapes are now cut out of a host sandbox's
// run_command output and a run's stored transcript, so they live where
// every one of those can reach them.
type Redactor = redact.Redactor

// KnownValue is pkg/redact's, for Redactor's reason.
type KnownValue = redact.KnownValue

// NewRedactor is redact.New, kept under the name this package's callers
// already use.
func NewRedactor(known []KnownValue) *Redactor { return redact.New(known) }
