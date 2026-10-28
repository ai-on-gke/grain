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

package mcp

// The live copy of a subprocess transport's output.
//
// DockerExecRunner and KubectlExecRunner both run a local binary -- docker
// or kubectl -- whose two streams they collect into two strings and hand
// back when the command is over. That is the whole of what a tool call
// needs, and it is not enough for grain's own CI: a job is minutes of
// shell, and somebody watching it (a person on the CI pane, an agent
// tailing get_ci_log) wants the line it is on now rather than the
// transcript afterwards (pkg/ci's live.go).
//
// So both runners grew a second entry point that is the first one plus a
// writer, and this file is the piece they share: a way to give a command's
// two streams to one caller-supplied writer without the two of them
// writing over each other. os/exec drains stdout and stderr on separate
// goroutines, so the writer they are teed into has to serialize them.

import (
	"io"
	"sync"
)

// liveStream is one caller's writer, made safe for the two goroutines
// os/exec drains a command's streams on.
//
// A whole Write is held under the lock rather than each stream being
// buffered separately, so a chunk of stdout and a chunk of stderr arrive
// whole and in the order they were produced -- interleaved lines are what
// a reader expects of a shell, half-lines spliced together are not.
type liveStream struct {
	mu sync.Mutex
	to io.Writer
}

func (s *liveStream) writerFor(collect io.Writer) io.Writer {
	if s == nil || s.to == nil {
		return collect
	}
	return io.MultiWriter(collect, s)
}

func (s *liveStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.to.Write(p)
	if err != nil {
		// Swallowed on purpose: this is somebody watching, not the
		// command's own output, which the caller collects separately and
		// gets whole. A follower that has gone away must not fail the
		// command it was following -- and reporting a short write back to
		// os/exec closes the pipe under a step that is still running.
		return len(p), nil
	}
	if n < len(p) {
		return len(p), nil
	}
	return n, nil
}
