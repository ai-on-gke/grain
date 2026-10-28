// Package guestdockerfile builds a granule guest rootfs from a
// consumer's Dockerfile: it validates the instruction list, supplies the
// base where the file names none, and drives an ordinary `docker build`.
//
// It is deliberately not a Dockerfile interpreter. Docker does the
// building and the layer caching, and nothing here parses beyond the
// instruction list -- what an instruction means is docker's business,
// and what is allowed to appear is granule's. That division is the whole
// design: a consumer gets the format, the cache and the tooling they
// already have, and granule keeps the two decisions it cannot delegate.
//
// **The base.** A guest is not a container image that happens to boot.
// It carries granule-agent (which is how `granule exec` reaches in at
// all), the control-net overlay, the growfs and DNS and route units --
// see deploy/guest-image/README.md. A file naming its own FROM is
// therefore naming only half of a machine, and granule supplies one
// (Options.From, default DefaultBase) when the file names none.
//
// A file may name its own, and the semantic that makes that safe is not
// a check but a mechanism that already exists: **the base supplies the
// filesystem payload, and granule's own half is thrown away and re-laid
// from the current build at pack time.** pkg/guestpack lays the
// overlay and the agents over the rootfs before `mke2fs -d` runs, out of
// the granule image being packed onto, so a guest built FROM anything
// boots with the packer's agents in it rather than with whatever its
// base carried. What naming a base buys, beyond convenience, is a pinned
// one: an invisible granule-supplied base cannot be written down, so the
// same file silently builds on a different machine as granule moves.
//
// The last FROM is the base; earlier ones are build stages and may name
// anything, since nothing of a stage reaches the guest but what a COPY
// --from takes out of it. Which bases actually work is pack's question
// and not a string's: a guest rootfs image packs, a plain OS image and
// an image that is not an OS at all are refused there, by the script
// that can see the filesystem (deploy/guest-image/pack-guest).
//
// **The subset.** The vocabulary is docker's wherever docker has a word
// for the thing, and what is not implemented is refused loudly, naming
// the file, the line and the instruction -- see Validate. A format that
// quietly ignores half of what it was handed is worse than one that
// never claimed the resemblance.
//
// What this path cannot do is the price of not booting: it runs on the
// build host's kernel, as root but with docker's default capabilities
// and no init running. `granule guest build -setup` boots a real
// machine and is where a step that must start a service, run a container
// or touch /dev/kvm belongs. deploy/guest-image/README.md states the
// list, and states the failure that does not announce itself -- a step
// that probes the build host and writes the answer into the guest.
package guestdockerfile

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

// DefaultBase is the guest rootfs image a build is based on when the
// caller names none: what deploy/publish-images.sh publishes out of the
// `guest-rootfs-image` target of this repo's own Dockerfile.
// granule/alpine.rootfs and granule/debian12.rootfs are the other
// variants; each base OS has its own package with a :latest moving tag.
// The immutable tag beside each is not: a rootfs is published as
// :os-<key>, where the key is a hash of the recipe that built
// it (deploy/guest-rootfs-key.sh), because these stages build an
// operating system and nothing of granule's -- so what a pin should
// follow is the OS rather than this repository's commits.
//
// A published reference and not a local tag, now that there is one to
// name. The point of publishing a base is that a consumer does not have
// to build it, and a default nobody can resolve until they have run a
// `docker build --target guest-rootfs-image` themselves gives that back
// -- it made the cheap route (no KVM, no boot, any CI runner with
// docker) the one with a build of granule's own tree as a prerequisite.
// The cost is that a default now pulls: name a pinned keyed tag or your own
// registry's mirror with -from where that matters, which is also how a
// build reaches a rootfs image that is not this one.
const DefaultBase = "ghcr.io/gke-labs/granule/debian.rootfs:latest"

// Options configures one Dockerfile-driven build.
type Options struct {
	// File is the consumer's Dockerfile as the caller named it. It is
	// what every refusal is reported against, so it should be the path
	// the caller typed rather than a resolved one.
	File string

	// Source is that file's text. The caller reads it, so a path that
	// does not exist fails before docker is invoked.
	Source string

	// Context is the build context directory passed to `docker build`.
	// Empty means the directory containing File, which is what a bare
	// `granule guest build` in a repository means.
	Context string

	// From is the base image. Empty means DefaultBase.
	From string

	// Tag is the image reference to give the result.
	Tag string

	// BuildArgs are `--build-arg` values ("KEY=VALUE"), for the ARGs the
	// file declares.
	BuildArgs []string

	// ExtraBuildArgs are passed to `docker build` verbatim, before the
	// context. This is how a build reaches a private registry, a proxy,
	// a builder other than the default, or asks for --no-cache.
	ExtraBuildArgs []string

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	Stdout io.Writer
	Stderr io.Writer
}

// Dockerfile validates opts.Source and returns the file docker will
// actually be given: granule's FROM, then the consumer's instructions
// verbatim.
//
// Verbatim matters. Everything below the header is the consumer's own
// bytes, so what docker builds is what they wrote, and `docker build`'s
// cache keys, its error messages and its own future features all keep
// working without granule having to know about any of it.
func Dockerfile(opts Options) (string, error) {
	text, _, err := plan(opts)
	return text, err
}

// plan is Dockerfile plus the instruction list it was derived from,
// which Build says things about on its way past.
func plan(opts Options) (string, []Instruction, error) {
	instrs, err := Validate(opts.File, opts.Source)
	if err != nil {
		return "", nil, err
	}
	own, hasOwn := Base(instrs)
	if hasOwn && opts.From != "" {
		return "", nil, fmt.Errorf("%s:%d: this file names its own base (FROM %s) and -from names %s: one of the two is not being built. Drop -from to build the file as written, or the FROM to let the flag supply the base",
			opts.File, own.Line, own.Image, opts.From)
	}
	return header(opts, instrs) + opts.Source, instrs, nil
}

// header is the part of the built file that is granule's: comments
// always, and a FROM only for a file that names no base of its own.
func header(opts Options, instrs []Instruction) string {
	preamble := fmt.Sprintf("# Generated by \"granule guest build\" from %s -- edit that file, not this.\n#\n", opts.File)
	if own, ok := Base(instrs); ok {
		return preamble + fmt.Sprintf(`# The base is this file's own (FROM %s, line %d). Whatever it carries of
# granule's own guest half -- the agents, the control-net overlay, the units
# that start them -- is discarded and re-laid at pack time from the granule
# image being packed onto, so the guest that boots carries the current one
# however old the base is. See pkg/guestpack.
`, own.Image, own.Line)
	}
	return preamble + fmt.Sprintf(`# The base is granule's, because this file names none: a guest wants the
# control-net overlay and the agents in its filesystem, and a base that
# has never been through that customisation is a machine nothing can
# reach. Name one with FROM to pin it. See deploy/guest-image/README.md.
FROM %s
`, base(opts))
}

// HeaderLines is how many lines granule prepends to the consumer's file.
// A line number in docker's own output is that many more than the same
// line in the file the consumer wrote, and a build says so on its way
// past rather than leaving the discrepancy to be worked out from a
// failing step.
//
// It reads opts.Source, since a file that names its own base gets a
// header of a different length than one granule supplies a FROM for. A
// source that does not validate has no header at all; 0 is the honest
// answer there and no caller reaches this before Validate anyway.
func HeaderLines(opts Options) int {
	instrs, err := Validate(opts.File, opts.Source)
	if err != nil {
		return 0
	}
	return strings.Count(header(opts, instrs), "\n")
}

func base(opts Options) string {
	if opts.From == "" {
		return DefaultBase
	}
	return opts.From
}

// buildBase is what the build is actually going to stand on, for the
// line that says so: the file's own base where it names one, and
// granule's where it does not.
func buildBase(opts Options, instrs []Instruction) string {
	if own, ok := Base(instrs); ok {
		return own.Image + " (named by the file)"
	}
	return base(opts)
}

// notes are the things a build should say about what it was handed
// before it hands it to docker -- each one a place where a granule guest
// build means slightly more, or slightly less, than the same file would
// mean to docker.
func notes(instrs []Instruction) []string {
	var out []string

	// A file that names a base and does nothing to it is not a mistake
	// here, which it would be for docker: it is a repack. The base's
	// payload is taken as it stands and granule's own half is re-laid
	// from the current build, which is the complete form of a rebase
	// written as a Dockerfile -- see docs/guest-images.md.
	onlyFrom := true
	var exposes bool
	for _, in := range instrs {
		if in.Name != "FROM" {
			onlyFrom = false
		}
		if in.Name == "EXPOSE" {
			exposes = true
		}
	}
	if own, ok := Base(instrs); ok && onlyFrom {
		out = append(out, fmt.Sprintf("this file names a base and does nothing else, so the build is a repack of %s: its payload as it stands, with granule's own guest half re-laid from the current build", own.Image))
	}
	if exposes {
		out = append(out, exposeNote)
	}
	return out
}

// Build validates the file, hands docker the file above, and tags the
// result.
//
// What comes out is a guest *rootfs* image -- the consumer's filesystem,
// as ordinary docker layers, on granule's own. Packing it into a bootable
// disk is pkg/guestpack's, and stays a step of its own: the pieces
// that must not come from a consumer (the agents, the overlay) are laid
// on at pack time, from the packer's own current build, so a rootfs
// published months ago boots with today's agent in it. `granule guest
// build` runs the two in one command, and the rootfs image is still
// named and kept, because that is what a consumer publishes and what
// somebody else packs. See docs/guest-images.md.
func Build(ctx context.Context, opts Options) error {
	if opts.Tag == "" {
		return fmt.Errorf("an output tag is required")
	}
	if opts.File == "" {
		return fmt.Errorf("a Dockerfile is required")
	}
	if opts.Context == "" {
		return fmt.Errorf("a build context is required")
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	dockerfile, instrs, err := plan(opts)
	if err != nil {
		return err
	}

	args := []string{"build", "-f", "-", "-t", opts.Tag}
	for _, a := range opts.BuildArgs {
		args = append(args, "--build-arg", a)
	}
	args = append(args, opts.ExtraBuildArgs...)
	args = append(args, opts.Context)

	fmt.Fprintf(opts.Stdout, "building %s from %s on %s\n", opts.Tag, opts.File, buildBase(opts, instrs))
	for _, note := range notes(instrs) {
		fmt.Fprintf(opts.Stdout, "%s\n", note)
	}
	fmt.Fprintf(opts.Stdout, "granule supplies the first %d lines of the file docker is given, so a line number below is %d more than the same line in %s\n", HeaderLines(opts), HeaderLines(opts), opts.File)

	binary := opts.DockerBinary
	if binary == "" {
		binary = dockercli.Binary()
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(dockerfile)
	// Docker's build progress goes to stderr and stays there: it is
	// output about the build rather than output of it, and a caller
	// piping stdout somewhere should get the build's own words, not a
	// progress bar.
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}

	fmt.Fprintf(opts.Stdout, "built %s\n", opts.Tag)
	return nil
}
