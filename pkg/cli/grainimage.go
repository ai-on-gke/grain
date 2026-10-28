// The image this build of grain is published as.
//
// The sibling of sandboximage.go, and the same trick for the other half
// of the pair: that file answers "which sandbox goes with this build",
// this one answers "which build is this". Both are stamped in at link
// time (the Dockerfile passes --build-arg GRAIN_IMAGE_REF, the Makefile
// turns it into a -ldflags -X), so the answer travels inside the image
// it describes and a rollback to an older tag says its own name rather
// than today's.
//
// It exists because of one thing a deployment writes about itself into
// somewhere else: the CI step grain installs in its own state repository
// (pkg/staterepo/format.go), which is a `docker run <image> state check`
// and so has to name an image. `grain state check` refuses a dump
// stamped with a schema it does not know -- "repository is at schema N,
// this build knows M" -- so the image that check runs has to be the
// build the deployment is running, and until now it was
// staterepo.DefaultCheckImage, which is main's. Right for a deployment
// tracking main, and wrong for every other one: a deployment held at an
// older tag got a workflow that failed every pull request against its
// state for a reason that had nothing to do with the change proposed in
// it, until an operator noticed and pinned it by hand.
//
// A deployment that does not run from an image at all -- `make build` on
// a host, `go run` in a checkout -- has no tag to name, and the fallback
// below is the best available answer there for the same reason
// defaultSandboxImage's is: a moving tag that resolves to something real
// beats naming nothing.

package cli

import (
	"flag"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

func grainImageCmd(args []string) {
	fs := flag.NewFlagSet("grain image", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return
	}
	fmt.Println(daemon.DefaultGrainImage)
}
