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

//go:build !linux && !darwin

package console

import (
	"errors"
	"os"
	"os/exec"
)

// errNoPTY is every platform but Linux and macOS. grain is deployed as
// one container on one Linux VM (scripts/setup.sh, terraform/gcp) and
// developed on macOS laptops, so the PTY half of this package is written
// against those two platforms' own ioctls (pty_linux.go, pty_darwin.go)
// and this file is what keeps the rest of it -- the Manager, the ring,
// the wrapper -- compiling anywhere else.
//
// A deployment that ever wants console sessions elsewhere writes the
// three functions below for that platform; nothing above them is
// platform-specific.
var errNoPTY = errors.New("console: interactive sessions need a pseudo-terminal, which this platform's build of grain does not provide")

func openPTY() (master, slave *os.File, err error) { return nil, nil, errNoPTY }

func attachTTY(cmd *exec.Cmd) {}

func resizePTY(master *os.File, rows, cols int) error { return errNoPTY }
