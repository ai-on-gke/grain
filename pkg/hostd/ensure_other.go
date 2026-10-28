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

//go:build !unix

package hostd

import "errors"

// EnsureBuiltins needs a unix host, the only kind grain hostd runs on.
func EnsureBuiltins(path, command string, builtins []BuiltinEntry) (EnsureResult, error) {
	return EnsureResult{}, errors.New("grain hostd runs on Linux only")
}
