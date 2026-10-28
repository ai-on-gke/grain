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

package ui

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
)

// serveCapabilityIfForked is TestMain's first act: a test that attaches
// a built-in for real runs this binary as `<binary> capability <name>
// [flags]`, the stand-in for `grain capability <name>`, and in that child
// the in-tree server serves stdio and the process exits.
func serveCapabilityIfForked() {
	if len(os.Args) < 3 || os.Args[1] != "capability" {
		return
	}
	b, ok := intree.Lookup(os.Args[2])
	if !ok {
		fmt.Fprintf(os.Stderr, "no in-tree capability %q\n", os.Args[2])
		os.Exit(1)
	}
	ctx := context.Background()
	s, err := b.Build(ctx, os.Args[3:], os.Getenv, log.New(os.Stderr, "", 0))
	if err == nil {
		err = s.Serve(ctx, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "grain capability: "+err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}
