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

// envFallback is a daemon flag whose value, when the flag was not passed,
// comes from an environment variable instead -- what a unit file's
// EnvironmentFile or a container's --env-file sets.
type envFallback struct {
	flag, env string
	dst       *string
}

// applyEnvFallbacks fills every flag in fallbacks that was not passed from
// its environment variable, trimmed, leaving the flag's own default when
// the variable is unset or blank. A flag on the command line always wins.
func applyEnvFallbacks(passed map[string]bool, fallbacks []envFallback) {
	for _, f := range fallbacks {
		if passed[f.flag] {
			continue
		}
		if env := strings.TrimSpace(os.Getenv(f.env)); env != "" {
			*f.dst = env
		}
	}
}
