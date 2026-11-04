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

// mergequeue.go is `grain merge-queue`: the merge queue from a terminal
// -- the pull requests queued to merge, the batches and CI runs testing
// them, and the gates holding any of it back -- read from the same
// deploy://merge-queue a sandbox's `grain merge-queue` reads, over the
// daemon's GET /api/fs/deploy/merge-queue (design/spec/external-cli/
// external-cli.spec.md, #XC-27).
//
// The route answers ci:read as well as deployment:read, and under ci:read
// alone it leaves out every repository the caller may not read, so the
// filtering is the daemon's rather than this command's: -repo narrows
// what the daemon was going to show, and cannot widen it. It only reads;
// forcing a merge stays `grain force-merge`, behind merge:force.
package cli

import (
	"context"
	"flag"
	"net/url"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/nsclient"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mergeQueueURI is the namespace file `grain merge-queue` reads, on
// either side of the sandbox wall.
const mergeQueueURI = "deploy://merge-queue"

func cmdMergeQueue(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain merge-queue", flag.ContinueOnError)
	repo := fs.String("repo", "", "only this repository's queue, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	q := url.Values{}
	if r := strings.TrimSpace(*repo); r != "" {
		if _, err := model.ParseRepo(r); err != nil {
			return usageErrorf("-repo: %v", err)
		}
		q.Set("repo", r)
	}
	if out.json {
		// The report the text is rendered from (mcp.MergeQueueReport).
		q.Set("format", "json")
	}
	return fsResult(fsClient(c, os.Stdout, os.Stderr).Read(mergeQueueURI, q, nsclient.ReadOptions{}))
}
