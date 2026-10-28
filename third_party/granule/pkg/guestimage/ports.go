package guestimage

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

// What an image declares with EXPOSE, read back off the image, for the
// commands that act on the declaration.
//
// It lives here rather than in the package that writes it because there
// is now more than one kind of caller. A build -- `granule guest pack`,
// `granule guest rebase` -- reads a declaration in order to *carry* it
// into the image it produces, and a launcher -- `granule vm create
// -publish-declared` -- reads the same declaration in order to *grant*
// it. Both ask docker the same question about the same field, and both
// have to treat the answer as somebody else's bytes, so they ask it in
// one place.
//
// # Reading one is not honouring one
//
// EXPOSE opens nothing, here or under docker: an image says what it
// wants reachable and something else decides whether that happens. A
// granule guest is shielded by default -- it is a VM behind its
// container's namespace, reachable only over the path netshim builds --
// and a declaration that opened a port by itself would mean every
// casually written documentation line punched a hole in that shield,
// with the same Dockerfile meaning "nothing" under `docker build` and
// "open 8080" under granule. So this package reads, and the caller
// decides. See the top-level README's "Publishing a guest's ports".

// DeclaredPorts is what ref declares with EXPOSE, in docker's own
// spelling ("8080/tcp"), sorted and deduplicated.
//
// The answer comes from `docker image inspect` -- a manifest already on
// the machine -- so nothing of ref is started and no code of its runs.
//
// It is validated rather than trusted, because it is a string out of an
// image somebody else authored and every caller puts it somewhere that
// gives it meaning: a generated Dockerfile for a build, a `docker run
// -p` argument for a launch. An entry that is not a port declaration at
// all is dropped with a line on report rather than passed on; report may
// be nil for a caller with nowhere to say so.
//
// An image that cannot be inspected is an error, not an empty list. The
// commonest cause is an image that is not local yet, and the two
// answers are worlds apart for a caller acting on the result: "this
// image declares nothing" and "nobody could ask" differ by exactly the
// ports the caller was about to publish.
func DeclaredPorts(ctx context.Context, dockerBinary, ref string, report io.Writer) ([]string, error) {
	if report == nil {
		report = io.Discard
	}
	d := &dockercli.CLI{Binary: dockerBinary}
	raw, err := d.ExposedPorts(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("reading what %s declares with EXPOSE: %w", ref, err)
	}
	ports := make([]string, 0, len(raw))
	for _, port := range raw {
		normalized, err := dockercli.NormalizePort(port)
		if err != nil {
			fmt.Fprintf(report, "ignoring %s's own %q, which is not a port declaration granule can act on\n", ref, port)
			continue
		}
		ports = append(ports, normalized)
	}
	// The source is a map in an image's config, which has no order of
	// its own: two reads of one image have to come out the same, since
	// what is built from this ends up in a Dockerfile somebody diffs and
	// what is launched from it ends up in a command line somebody reads.
	sort.Strings(ports)
	return slices.Compact(ports), nil
}
