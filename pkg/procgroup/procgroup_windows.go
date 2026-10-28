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

//go:build windows

package procgroup

import (
	"os"
	"os/exec"
)

// prepare leaves cmd's Cancel at exec.CommandContext's own default (kill
// cmd.Process alone) -- Windows has no POSIX process-group signal to send
// instead, and nothing in this codebase currently ships or tests a
// Windows build; a real fix here would use a Job Object.
func prepare(cmd *exec.Cmd) {}

// kill has no process group to reach on Windows, for prepare's own
// reason: there is nothing here that puts one together.
func kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
