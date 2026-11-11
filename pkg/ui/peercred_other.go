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

//go:build !linux

package ui

import (
	"errors"
	"io/fs"
	"net"
)

// peerUID needs SO_PEERCRED, which only Linux has here: everywhere else
// every connection to the API socket is refused.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer credentials (SO_PEERCRED) are only read on Linux")
}

// ownedByThisAccount is true where ownership cannot be read: the API
// socket is refused on these platforms anyway (peerUID).
func ownedByThisAccount(fs.FileInfo) bool { return true }
