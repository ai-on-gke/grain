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

package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdRelease is `grain release approve|reject vX.Y.Z`: a person's answer
// to a trunk release the release train has staged as vX.Y.Z-pending
// (design/design/release-from-trunk.design.md, §4). It starts
// .grain/workflows/promote-release.yml through the person-only
// pending-releases routes, which refuse an agent whatever it holds -- so
// run from an agent's sandbox this is refused, which is the point.
func cmdRelease(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain release", flag.ContinueOnError)
	repo := fs.String("repo", model.GrainSourceRepo().String(), "the repository whose pending release this is")
	shortSoakOK := fs.Bool("short-soak-ok", false, "approve only: accept a release whose staging soak was under the minimum (its notification says SHORT SOAK)")
	allowBreaking := fs.Bool("allow-breaking", false, "approve only: accept a release that carries breaking changes without being the major release RELEASE_MAJOR asks for")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: grain release approve|reject [-repo owner/name] [-short-soak-ok] [-allow-breaking] vX.Y.Z")
		fmt.Fprintln(fs.Output(), "       grain release approval-key   (the value for GitHub's GRAIN_RELEASE_APPROVAL_KEY variable)")
		fs.PrintDefaults()
	}
	if len(args) == 0 {
		fs.Usage()
		return flag.ErrHelp
	}
	verb := args[0]
	if verb == "approval-key" {
		key, err := c.ReleaseApprovalKey(ctx)
		if err != nil {
			return err
		}
		fmt.Print(key)
		return nil
	}
	if verb != "approve" && verb != "reject" {
		fs.Usage()
		return fmt.Errorf("unknown release command %q: approve or reject", verb)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("grain release %s takes the pending release, vX.Y.Z", verb)
	}
	if *shortSoakOK && verb != "approve" {
		return fmt.Errorf("-short-soak-ok is for grain release approve")
	}
	if *allowBreaking && verb != "approve" {
		return fmt.Errorf("-allow-breaking is for grain release approve")
	}
	decision, err := c.DecideRelease(ctx, *repo, fs.Arg(0), verb == "approve",
		ui.ReleaseApproval{ShortSoakOK: *shortSoakOK, AllowBreaking: *allowBreaking})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(decision)
		return nil
	}
	if decision.Approve {
		var accepted string
		if decision.ShortSoakOK {
			accepted += ", short soak accepted"
		}
		if decision.AllowBreaking {
			accepted += ", breaking changes accepted"
		}
		fmt.Printf("approved %s%s: promote-release is tagging it and moving latest onto it\n", decision.Version, accepted)
	} else {
		fmt.Printf("rejected %s: promote-release is dropping it; the next train run cuts its next rc\n", decision.Version)
	}
	if d := decision.Dispatched; d != nil {
		fmt.Printf("dispatched %s's %s on %s for %s (%s-pending at %.12s), signed for %s\n",
			d.Repo, d.Workflow, d.Ref, decision.Version, decision.Version, d.Commit, d.By)
		return nil
	}
	out.ciStarted(decision.Run)
	return nil
}
