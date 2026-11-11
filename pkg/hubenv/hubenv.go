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

// Package hubenv reads the Grain Hub's environment variables, which were
// named GRAIN_REGISTRY_* before the Grain Registry was renamed the Grain
// Hub. Both spellings are read for a release; the new one wins.
package hubenv

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

var warned sync.Map

// Log is where deprecation warnings go: the standard logger's writer by
// default. A seam for tests.
var Log = func() io.Writer { return log.Writer() }

// Former is the name a GRAIN_HUB_* variable had before the rename.
func Former(name string) string {
	return strings.Replace(name, "GRAIN_HUB", "GRAIN_REGISTRY", 1)
}

// Get returns $name, or -- when that is unset or empty -- the variable's
// former GRAIN_REGISTRY_* spelling, warning once a process that the old
// name is deprecated.
func Get(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	old := Former(name)
	if old == name {
		return ""
	}
	v := os.Getenv(old)
	if v != "" {
		Warn("$"+old, "$"+name)
	}
	return v
}

// Warn logs, once a process per old name, that old is deprecated in
// favour of new.
func Warn(old, new string) {
	if _, done := warned.LoadOrStore(old, true); done {
		return
	}
	log.New(Log(), "", log.LstdFlags).Printf("warning: %s is deprecated and will be removed in a future release; use %s (the Grain Registry is now the Grain Hub)", old, new)
}

// ResetForTest forgets which warnings were already given.
func ResetForTest() { warned = sync.Map{} }
