package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwsalmon/granule/pkg/granulefile"
	"github.com/bwsalmon/granule/pkg/guestwrap"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// runGranule dispatches "granule granulefile <subcommand>".
func runGranule(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("granulefile: expected a subcommand (check, build)")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "check":
		return runGranuleCheck(rest, stdin, stdout, stderr)
	case "build":
		return runGranuleBuild(ctx, rest, stdin, stdout, stderr)
	default:
		return fmt.Errorf("granulefile: unknown subcommand %q", cmd)
	}
}

// granulefileCheckUsage is the spelling granule gives §4.6's --check: a
// way to validate a file and report, building nothing.
//
// It is a command rather than a flag on the build because it is the
// cheap half -- it needs no /dev/kvm, no docker and no network, and
// executes nothing of the file's -- so it belongs in an editor and in
// CI whether or not the machine it runs on could have built the thing.
// `granule granulefile build -print` is the other end of the same
// question: what the file compiles to, without running it.
const granulefileCheckUsage = `usage: granule granulefile check [-f file] [flags] [context]

Validates a Granule -- a Dockerfile with granule's VM instruction in
it -- and reports every problem in it, building nothing. See
docs/granulefile.md for the language: VM FROM, VM UNPACK FROM,
VM PACK FROM and VM WRAP FROM start a stage, VM PACK and VM WRAP convert
the stage they are in, and VM SET and VM SNAPSHOT say how the machine
boots.

With no -f, the file is "Granule" in the build context, then
"Dockerfile" if there is no Granule: the name selects nothing about
what is built, the contents do. "-f -" reads standard input.

What it checks is the whole file, including stages a -target would not
build, so a file's problems arrive in one report rather than one
--target at a time.

Flags:
`

func runGranuleCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule granulefile check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, granulefileCheckUsage)
		fs.PrintDefaults()
	}

	file := fs.String("f", "", "Granule to check (default \"Granule\" in the build context, then \"Dockerfile\"). \"-\" reads standard input")
	target := fs.String("target", "", "`stage` the build would produce, by name or index (default the last stage in the file)")
	level := fs.Int("level", int(granulefile.Level3), "conformance `level` to check against: 1 Dockerfile, 2 converters, 3 VM stages. A construct above it is reported as unimplemented at that level rather than as forbidden. The default checks the whole language, which is what a check that builds nothing can do")

	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return fmt.Errorf("a check takes at most one context directory, and was given %d: %s", len(rest), strings.Join(rest, " "))
	}
	if *level < int(granulefile.Level1) || *level > int(granulefile.Level3) {
		return fmt.Errorf("-level %d: the levels are 1 (Dockerfile), 2 (converters) and 3 (VM stages)", *level)
	}

	context := "."
	if len(rest) == 1 {
		context = rest[0]
	}
	path, source, err := readGranule(context, *file, stdin)
	if err != nil {
		return err
	}

	f, err := granulefile.Check(path, source, granulefile.Options{
		Level:  granulefile.Level(*level),
		Target: *target,
	})
	if err != nil {
		return err
	}

	printPlan(stdout, f, *target, granulefile.Level(*level))
	return nil
}

// readGranule resolves the file to check (§3.1): the one named, "-"
// for standard input, or Granule then Dockerfile in the context root.
func readGranule(context, file string, stdin io.Reader) (path, source string, err error) {
	if file == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", "", fmt.Errorf("reading the file from standard input: %w", err)
		}
		return "<stdin>", string(b), nil
	}

	if file == "" {
		for _, name := range []string{"Granule", "Dockerfile"} {
			candidate := filepath.Join(context, name)
			if _, err := os.Stat(candidate); err == nil {
				file = candidate
				break
			}
		}
		if file == "" {
			return "", "", fmt.Errorf("%s: no Granule and no Dockerfile. Name one with -f, or run this where one of them is", context)
		}
	}

	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("%s: no such file", file)
		}
		return "", "", fmt.Errorf("reading %s: %w", file, err)
	}
	return file, string(b), nil
}

// printPlan says what the file would build, which is the question an
// author has once it is valid: which stages there are, which of them are
// machines, and what the target produces.
func printPlan(stdout io.Writer, f *granulefile.File, target string, level granulefile.Level) {
	fmt.Fprintf(stdout, "%s: ok (%s, checked against %s)\n", f.Path, granulefile.Version, level)
	for _, s := range f.Stages {
		// Both names, where a stage carries two: the header's names the
		// filesystem as it stood before the converter, and the
		// converter's names the machine it made of it (§4.1).
		names := []string{}
		for _, n := range s.Names() {
			names = append(names, n.Name)
		}
		name := "-"
		if len(names) > 0 {
			name = strings.Join(names, ", ")
		}
		// A stage that converts is two kinds, in the order it is them
		// (§4.2): what its instructions mean changes at the converter.
		kind := s.Kind().String()
		if s.HeaderKind() != s.Kind() {
			kind = s.HeaderKind().String() + " -> " + s.Kind().String()
		}
		line := fmt.Sprintf("  stage %-2d %-24s %-16s %s %s", s.Index, name, kind, s.Header, s.Ref)
		if c := s.Converter(); c != nil {
			line += fmt.Sprintf(" (converted by %s on line %d)", c, c.Line)
		}
		fmt.Fprintln(stdout, strings.TrimRight(line, " "))
	}

	if t, kind, ok := f.Target(target); ok {
		result := "an OCI container image"
		if kind == granulefile.VM {
			result = "a granule VM image"
		}
		// The name the caller gave, where they gave one: a stage with two
		// names produces a different thing under each, so echoing the
		// stage's own label back would answer a question nobody asked.
		label := target
		if label == "" {
			label = t.Label()
		}
		fmt.Fprintf(stdout, "target: %s, which produces %s\n", label, result)
	}
	fmt.Fprintln(stdout, "nothing was built: this checks the file.")
}

const granulefileBuildUsage = `usage: granule granulefile build [-f file] -t tag [flags] [context]

Builds a Granule -- a Dockerfile with granule's VM instruction in
it -- producing a container image or a bootable VM image according
to the target stage.

With no -f, the file is "Granule" in the build context, then
"Dockerfile" if there is no Granule. "-f -" reads standard input.

Flags:
`

func runGranuleBuild(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule granulefile build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, granulefileBuildUsage)
		fs.PrintDefaults()
	}

	file := fs.String("f", "", "Granule to build (default \"Granule\" in the build context, then \"Dockerfile\"). \"-\" reads standard input")
	tag := fs.String("t", "", "image `tag` to publish the result under")
	target := fs.String("target", "", "`stage` to build (default the last stage)")
	printDF := fs.Bool("print", false, "print the generated Dockerfile without building")
	guestImage := fs.String("guest-image", "", "guest rootfs image (default "+guestwrap.DefaultGuestImage+")")
	konturImage := fs.String("kontur-image", "", "granule base image (default "+vmspec.DockerImage+")")
	packerImage := fs.String("packer-image", "", "packer image for guest disk formatting")
	variant := fs.String("variant", "", "guest overlay variant (debian, alpine, auto)")
	diskExtraMB := fs.Int("disk-extra-mb", 0, "extra disk headroom in MiB")
	var buildArgs repeatedFlag
	fs.Var(&buildArgs, "build-arg", "a build variable as `KEY=VALUE` (repeatable), overriding the file's global ARG default. granule expands these into the references it resolves itself -- the image a VM PACK packs, the machine a VM UNPACK FROM unpacks -- and passes them on to the underlying build")

	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return fmt.Errorf("a build takes at most one context directory, and was given %d: %s", len(rest), strings.Join(rest, " "))
	}
	contextDir := "."
	if len(rest) == 1 {
		contextDir = rest[0]
	}

	path, source, err := readGranule(contextDir, *file, stdin)
	if err != nil {
		return err
	}

	return granulefile.Build(ctx, path, source, granulefile.BuildOptions{
		Context:         contextDir,
		Tag:             *tag,
		Target:          *target,
		GuestImage:      *guestImage,
		KonturImage:     *konturImage,
		PackerImage:     *packerImage,
		Variant:         *variant,
		DiskExtraMB:     *diskExtraMB,
		BuildArgs:       buildArgs,
		PrintDockerfile: *printDF,
		Stdout:          stdout,
		Stderr:          stderr,
	})
}
