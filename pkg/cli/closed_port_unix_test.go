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

//go:build unix

package cli

import (
	"net"
	"strconv"
	"syscall"
	"testing"
)

// reserveClosedPort is a loopback address nothing listens on and nothing
// else can take until the test ends: a TCP socket bound to it but never
// listening, so a connection to it is refused, as one to a dead daemon is.
func reserveClosedPort(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		t.Fatalf("bound address %T is not IPv4", sa)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(in4.Port))
}
