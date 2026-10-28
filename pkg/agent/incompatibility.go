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

package agent

import (
	"errors"
	"fmt"
)

// ModelIncompatibilityError says that the configured model cannot be used by
// the account behind a framework. It is a deployment condition, not a task
// failure: changing the model/account (or selecting an explicitly configured
// alternate backend) can recover it without changing the task.
type ModelIncompatibilityError struct {
	Framework string
	Model     string
	Message   string
	Err       error
}

func (e *ModelIncompatibilityError) Error() string {
	what := "configured model is incompatible with this account"
	if e.Model != "" {
		what = fmt.Sprintf("configured model %q is incompatible with this account", e.Model)
	}
	if e.Framework != "" {
		what = e.Framework + ": " + what
	}
	if e.Message != "" {
		what += "; " + e.Message
	}
	return what
}

func (e *ModelIncompatibilityError) Unwrap() error { return e.Err }

// ModelIncompatibility finds a typed compatibility refusal through wrappers.
func ModelIncompatibility(err error) (*ModelIncompatibilityError, bool) {
	var incompat *ModelIncompatibilityError
	if errors.As(err, &incompat) {
		return incompat, true
	}
	return nil, false
}
