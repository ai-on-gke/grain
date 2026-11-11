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

// hubManagedFlags are the daemon's flags for a deployment whose lifecycle
// is managed from outside it, such as a Grain Hub hosted instance:
// -management-url and -default-environment-name, each falling back to
// its environment variable when not passed.
type hubManagedFlags struct {
	managementURLFlag, defaultEnvironmentNameFlag *string
}

// registerHubManagedFlags declares the flags on fs; call applyEnv once fs
// has parsed.
func registerHubManagedFlags(fs *flag.FlagSet) hubManagedFlags {
	return hubManagedFlags{
		managementURLFlag: fs.String("management-url", "",
			"optional external http:// or https:// URL where this deployment's lifecycle and upgrades are managed "+
				"(for example its registry entry page), shown in place of in-VM self-upgrade controls when "+
				"-disable-self-upgrade is set ($GRAIN_MANAGEMENT_URL when unset)"),
		defaultEnvironmentNameFlag: fs.String("default-environment-name", "",
			"the environment name (the UI's tab title and sidebar badge) this deployment takes when it has none, "+
				"or still has the one this flag gave it before: never one its operator set in Settings or its state repo. "+
				"A Grain Hub sets it to a hosted instance's entry display name ($GRAIN_DEFAULT_ENVIRONMENT_NAME when unset)"),
	}
}

// applyEnv fills each flag that was not passed from its environment
// variable, when that is set.
func (f hubManagedFlags) applyEnv(passed map[string]bool) {
	for name, v := range map[string]struct {
		dst *string
		env string
	}{
		"management-url":           {f.managementURLFlag, "GRAIN_MANAGEMENT_URL"},
		"default-environment-name": {f.defaultEnvironmentNameFlag, "GRAIN_DEFAULT_ENVIRONMENT_NAME"},
	} {
		if passed[name] {
			continue
		}
		if env := strings.TrimSpace(os.Getenv(v.env)); env != "" {
			*v.dst = env
		}
	}
}

func (f hubManagedFlags) managementURL() string { return strings.TrimSpace(*f.managementURLFlag) }
func (f hubManagedFlags) defaultEnvironmentName() string {
	return strings.TrimSpace(*f.defaultEnvironmentNameFlag)
}
