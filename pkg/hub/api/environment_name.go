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

package api

import (
	"strings"
	"unicode"
)

// MaxEnvironmentNameRunes is the longest environment name a grain daemon
// accepts (model.Config.EnvironmentName, bounded by ui.UpdateSettings at
// the same 32). A hub-supplied default is cut to it rather than refused.
const MaxEnvironmentNameRunes = 32

// EnvironmentNameForEntry is the environment name the hub gives a grain
// instance by default: the entry's display name when it has one, else its
// entry name, made into a name the instance's Settings would accept
// (SanitizeEnvironmentName). Empty only when both are.
func EnvironmentNameForEntry(displayName, entryName string) string {
	if name := SanitizeEnvironmentName(displayName); name != "" {
		return name
	}
	return SanitizeEnvironmentName(entryName)
}

// SanitizeEnvironmentName makes s an environment name a grain daemon
// accepts instead of rejecting it: line breaks, tabs and every other
// control or non-printing character become spaces, runs of whitespace
// collapse to one, the ends are trimmed and the result is cut to
// MaxEnvironmentNameRunes. A display name is free text up to
// MaxDisplayNameRunes, and provisioning an instance must never fail over
// what its tab title says.
func SanitizeEnvironmentName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxEnvironmentNameRunes {
		s = strings.TrimSpace(string(r[:MaxEnvironmentNameRunes]))
	}
	return s
}
