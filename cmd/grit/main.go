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

// Command grit is grain's CI engine as a binary of its own
// (docs/design/grit-split.md). Its subcommands:
//
//	grit act [flags]     run one GitHub Actions job in a disposable Granule VM
//	grit verify [flags]  check a run attestation against a public key
//
// The old flag-only form, `grit -workflow ... -job ...`, still runs
// `grit act`: a first argument that is a flag (or no argument at all)
// means act. `serve`, `pod` and `client` arrive with the rest of
// GS-2 and GS-3.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"github.com/ai-on-gke/grain/pkg/version"
)

type mappings map[string]string

func (m mappings) String() string { return "label=image" }
func (m mappings) Set(v string) error {
	label, image, ok := strings.Cut(v, "=")
	if !ok || label == "" || image == "" {
		return fmt.Errorf("want label=image")
	}
	m[label] = image
	return nil
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// subcommands are grit's verbs. act is also what an argument list
// starting with a flag means, so scripts written against the flag-only
// binary keep working.
var subcommands = map[string]func(context.Context, []string, io.Writer, io.Writer) int{
	"act":     act,
	"verify":  verify,
	"serve":   serve,
	"client":  clientCmd,
	"version": versionCmd,
}

func versionCmd(_ context.Context, _ []string, stdout, _ io.Writer) int {
	v := version.Release
	if v == "" {
		v = "grit/unversioned"
	}
	fmt.Fprintln(stdout, v)
	return 0
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return act(ctx, args, stdout, stderr)
	}
	cmd, ok := subcommands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "grit: unknown command %q (want act, verify, serve, client, or version)\n", args[0])
		return 2
	}
	return cmd(ctx, args[1:], stdout, stderr)
}

// actRun is actions.Run, held in a variable so a test can see what act
// asked for without a Granule VM.
var actRun = actions.Run

func act(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grit act", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "input Git repository")
	workflow := fs.String("workflow", "", "workflow path relative to the repository")
	job := fs.String("job", "", "job id")
	event := fs.String("event", "push", "Actions event")
	runner := fs.String("runner", actions.DefaultRunner, "GitHub runner label")
	name := fs.String("name", "", "Granule VM name")
	images := mappings{actions.DefaultRunner: "grain-sandbox:dev"}
	fs.Var(images, "runner-image", "runner-to-Granule mapping (repeatable label=image)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	err := actRun(ctx, actions.RunRequest{Repo: *repo, Workflow: *workflow, Job: *job, Event: *event, Runner: *runner, Name: *name, Images: actions.RunnerImages(images)}, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
