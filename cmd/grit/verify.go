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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/attest"
)

// verify is `grit verify -key KEY.pem [-tree SHA256 -git-tree ID
// [-commit SHA]] ENVELOPE.json`: it checks a run attestation's
// signature against a public key, and, when given one, that it is about
// the tree named, then prints what it states. It needs nothing but the
// envelope and the key, so anyone can check a run without grain. Reading
// grit's published key set (GET /.well-known/grit-keys.json) instead of
// a key file arrives with GS-3's KMS keys.
func verify(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFile := fs.String("key", "", "PEM public key the envelope must be signed by")
	treeSHA := fs.String("tree", "", "sha256 of the canonical archive the run must be about")
	gitTree := fs.String("git-tree", "", "git tree id the run must be about")
	commit := fs.String("commit", "", "commit the run must name (with -tree)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *keyFile == "" || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: grit verify -key KEY.pem [-tree SHA256 -git-tree ID [-commit SHA]] ENVELOPE.json")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "grit verify:", err); return 1 }
	keyPEM, err := os.ReadFile(*keyFile)
	if err != nil {
		return fail(err)
	}
	kid, pub, err := attest.ParsePublicKey(keyPEM)
	if err != nil {
		return fail(err)
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fail(err)
	}
	var env attest.Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return fail(fmt.Errorf("%s: %w", fs.Arg(0), err))
	}
	s, err := attest.Verify(&env, attest.KeySet{kid: {Public: pub, Status: attest.KeyActive}})
	if err != nil {
		return fail(err)
	}
	if *treeSHA != "" || *gitTree != "" || *commit != "" {
		if err := s.CheckSubject(attest.Tree{SHA256: *treeSHA, GitTree: *gitTree}, *commit); err != nil {
			return fail(err)
		}
	}
	p := s.Predicate
	tree := s.Tree()
	fmt.Fprintf(stdout, "verified: signed by key %s\n", kid)
	fmt.Fprintf(stdout, "tree:     sha256 %s, git %s\n", tree.SHA256, tree.GitTree)
	if c := s.Commit(); c != "" {
		fmt.Fprintf(stdout, "commit:   %s\n", c)
	}
	fmt.Fprintf(stdout, "run:      %s for %s (request %s, client %s), issued %s by %s\n",
		p.Request.Run, p.Request.Repo, p.Request.RequestID, p.Client.ID, p.IssuedAt.Format("2006-01-02T15:04:05Z07:00"), p.Grit.Version)
	for _, j := range p.Jobs {
		how := "ran"
		if j.Cached != nil {
			how = "cached from " + j.Cached.Run + " (" + j.Cached.Attestation + ")"
		}
		fmt.Fprintf(stdout, "job:      %s %s, %s\n", j.ID, j.Result.Conclusion, how)
	}
	return 0
}
