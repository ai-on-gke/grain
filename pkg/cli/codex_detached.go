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
	"context"
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

func runCodexDetached(args []string) {
	if err := protocol.DropCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "grain %s: %v\n", codex.DetachedSubcommand, err)
		os.Exit(1)
	}
	code, err := codex.RunDetached(context.Background(), args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grain %s: %v\n", codex.DetachedSubcommand, err)
		os.Exit(code)
	}
	os.Exit(code)
}
