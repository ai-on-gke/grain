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

package cli

import (
	"os"
	"strings"
)

// envDefault sets *dst from the environment variable env when the flag
// named flag was not passed on the command line and env is set (trimmed,
// non-empty): the "$ENV when unset" rule many daemon flags share.
func envDefault(passed map[string]bool, flag, env string, dst *string) {
	if passed[flag] {
		return
	}
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		*dst = v
	}
}
