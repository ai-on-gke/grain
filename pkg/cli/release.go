package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// cmdRelease is `grain release approve|reject vX.Y.0`: a person's answer
// to a trunk release the release train has staged as vX.Y.0-pending
// (docs/design/release-from-trunk.md, §4). It starts
// .grain/workflows/promote-release.yml through the person-only
// pending-releases routes, which refuse an agent whatever it holds -- so
// run from an agent's sandbox this is refused, which is the point.
func cmdRelease(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain release", flag.ContinueOnError)
	repo := fs.String("repo", model.GrainSourceRepo().String(), "the repository whose pending release this is")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: grain release approve|reject [-repo owner/name] vX.Y.0")
		fs.PrintDefaults()
	}
	if len(args) == 0 {
		fs.Usage()
		return flag.ErrHelp
	}
	verb := args[0]
	if verb != "approve" && verb != "reject" {
		fs.Usage()
		return fmt.Errorf("unknown release command %q: approve or reject", verb)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("grain release %s takes the pending release, vX.Y.0", verb)
	}
	decision, err := c.DecideRelease(ctx, *repo, fs.Arg(0), verb == "approve")
	if err != nil {
		return err
	}
	if out.json {
		out.encode(decision)
		return nil
	}
	if decision.Approve {
		fmt.Printf("approved %s: promote-release is tagging it and moving latest onto it\n", decision.Version)
	} else {
		fmt.Printf("rejected %s: promote-release is dropping it; the next train run cuts its next rc\n", decision.Version)
	}
	out.ciStarted(decision.Run)
	return nil
}
