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

package codex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// ReviewedCLIVersion is deliberately exact, not a minimum version.
// Older CLIs may ignore the no-environment configuration; newer CLIs may
// add tools that are not environment-backed. Updating this pin requires
// reviewing the tool registry and running the credential-free hostile
// tool probes for both direct-tool and Code Mode model profiles.
const ReviewedCLIVersion = "0.153.0"

// validateRuntime asks the binary what version it is -- through the
// Host, so that the copy being checked is the copy that will run rather
// than whatever a path of that name resolves to somewhere else.
func validateRuntime(ctx context.Context, host agent.Host, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := host.Run(ctx, agent.Command{Path: binary, Args: []string{"--version"}}, nil)
	if err != nil {
		return fmt.Errorf("codex: checking CLI version: %w", err)
	}
	return validateVersion(out)
}

func validateVersion(output string) error {
	if strings.TrimSpace(output) != "codex-cli "+ReviewedCLIVersion {
		return fmt.Errorf("codex: refusing an unreviewed CLI version; Grain's MCP-only policy requires codex-cli %s (install the pinned CLI and matching codex-code-mode-host from Grain's Dockerfile)", ReviewedCLIVersion)
	}
	return nil
}
