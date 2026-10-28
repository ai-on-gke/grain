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

package sysstat

import "fmt"

// DiskUsage is the no-statfs half of the same split pkg/procgroup uses:
// Linux and darwin share one statfs(2) implementation (disk_unix.go), and
// a build for anything else keeps compiling and reports the same "no
// reading to give" every other optional pane already has a shape for,
// rather than needing a syscall this file's platform may spell
// differently.
//
// The tag is the negation of disk_unix.go's rather than !linux, which is
// what it was while Linux was the only half: adding darwin there without
// narrowing here would have given this package two DiskUsage symbols on
// darwin and broken the build outright.
func DiskUsage(path string) (totalMB, usedMB int, err error) {
	return 0, 0, fmt.Errorf("sysstat: disk usage is only available on Linux and macOS")
}

// FilesystemID is the no-statfs half of the same split, and says the same
// thing DiskUsage above does: a caller with no usage figure to report has
// nothing to fold two of them together over either.
func FilesystemID(path string) (uint64, error) {
	return 0, fmt.Errorf("sysstat: filesystem identity is only available on Linux and macOS")
}
