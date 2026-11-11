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
	"flag"
	"os"
	"strings"
)

// registerDefaultEnvironmentNameFlag declares the daemon's
// -default-environment-name flag on fs; read it with
// defaultEnvironmentNameOf once fs has parsed.
func registerDefaultEnvironmentNameFlag(fs *flag.FlagSet) *string {
	return fs.String("default-environment-name", "",
		"the environment name (the UI's tab title and sidebar badge) this deployment takes when it has none, "+
			"or still has the one this flag gave it before: never one its operator set in Settings or its state repo. "+
			"A Grain Hub sets it to a hosted instance's entry display name ($GRAIN_DEFAULT_ENVIRONMENT_NAME when unset)")
}

// defaultEnvironmentNameOf is the -default-environment-name a daemon
// runs with: the flag's value when it was passed, and
// $GRAIN_DEFAULT_ENVIRONMENT_NAME, when set, when it was not.
func defaultEnvironmentNameOf(value string, passed bool) string {
	if !passed {
		if env := strings.TrimSpace(os.Getenv("GRAIN_DEFAULT_ENVIRONMENT_NAME")); env != "" {
			value = env
		}
	}
	return strings.TrimSpace(value)
}
