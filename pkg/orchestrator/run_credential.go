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

package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
)

// runCredentialFileName is the file hideRunCredential writes the
// credential to, inside a directory of its own.
const runCredentialFileName = "credential"

// hideRunCredential places a run's credential where a confined host
// sandbox cannot read it. A host-directory sandbox's run_command runs as
// the daemon's own account with all of / bound in except the paths it is
// told to hide (mcp.NewHostSandboxTools), so any file the credential is
// written into -- the claude MCP config in $TMPDIR, the agy home, the
// codex spec -- would be readable from inside it, and the credential with
// it. So for such a run (hide non-empty) the credential goes into a 0600
// file in a fresh 0700 directory, that directory is added to the paths
// run_command hides, and the mcpserver is told only the file's path
// (agent.RunConfig.RunCredentialFile). It returns the file, the hide list
// to run with, and what removes the directory once the run is over.
//
// Any other sandbox -- a VM, a container, a pod -- runs the agent's
// commands somewhere the controller's files are not, and the credential
// is handed over in the mcpserver's environment (agent.RunConfig.
// RunCredential): file is "" and hide is returned as it was.
func hideRunCredential(credential string, hide []string) (file string, hideWith []string, cleanup func(), err error) {
	cleanup = func() {}
	if credential == "" || len(hide) == 0 {
		return "", hide, cleanup, nil
	}
	dir, err := os.MkdirTemp("", "grain-run-credential-")
	if err != nil {
		return "", nil, cleanup, fmt.Errorf("making a directory for the run's credential: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	file = filepath.Join(dir, runCredentialFileName)
	if err := os.WriteFile(file, []byte(credential+"\n"), 0o600); err != nil {
		cleanup()
		return "", nil, func() {}, fmt.Errorf("writing the run's credential: %w", err)
	}
	return file, append(append([]string(nil), hide...), dir), cleanup, nil
}
