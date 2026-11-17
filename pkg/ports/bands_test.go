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

package ports_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/ports"
)

// TestBuiltinCapabilityPortsStayInTheirBand holds core's built-in
// capability servers' data planes to 4251-4259, so none drifts into
// 4260-4269, which downstream editions' host-only servers use on the
// same loopback.
func TestBuiltinCapabilityPortsStayInTheirBand(t *testing.T) {
	builtins := map[string]int{
		"ports.Figma":                ports.Figma,
		"ports.Git":                  ports.Git,
		"intree.DefaultGChatPort":    intree.DefaultGChatPort,
		"intree.DefaultGCPTokenPort": intree.DefaultGCPTokenPort,
	}
	for name, port := range builtins {
		if port < ports.BuiltinCapabilityMin || port > ports.BuiltinCapabilityMax {
			t.Errorf("%s = %d, want it in the built-in capability band %d-%d", name, port, ports.BuiltinCapabilityMin, ports.BuiltinCapabilityMax)
		}
		if port >= ports.EditionMin && port <= ports.EditionMax {
			t.Errorf("%s = %d is in the band reserved for editions, %d-%d", name, port, ports.EditionMin, ports.EditionMax)
		}
	}
}
