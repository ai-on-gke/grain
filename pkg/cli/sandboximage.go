// The sandbox image this build of grain goes with, plus `grain sandbox-image
// contract` and `grain sandbox-image check <image>` for inspecting the
// versioned sandbox boundary (pkg/sandboximage).
//
// A granule-backed deployment runs two images, not one: this one, and the
// sandbox each task's VM runs -- a granule image carrying grain's own
// provisioned guest, built from ../../Granulefile and published
// as `guest` by ../../scripts/release-images.sh, which is what this
// repository's own release job runs on every merge, and by
// ../../.github/workflows/build-artifacts.yml where Actions still run
// (grain/task-188). They are
// built from the same commit, and a deployment running two commits' worth
// of them is exactly the drift scripts/setup.sh used to avoid by building
// on the host.
//
// It no longer builds either (bwsalmon/agents#645): it pulls this image,
// and asks *this image* which sandbox goes with it, which is what this
// file exists to answer. The reference is stamped in at build time
// (Dockerfile passes --build-arg SANDBOX_IMAGE, the Makefile turns it
// into a -ldflags -X), so a deployment needs to be told nothing at all,
// and a `grain sandbox-image` out of any image says exactly which
// sandbox that build expects -- including a rollback to an older tag,
// whose answer is its own older sandbox rather than today's.
//
// Users who build custom sandbox images FROM Grain's minimal base images
// (`ghcr.io/bwsalmon/grain/guest-base:<tag>` or
// `ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>`) do NOT need to rebuild on
// every Grain commit: they only need to rebuild when the integer contract
// (`dev.grain.sandbox.contract`, or Granule's `dev.granule.guest.contract`)
// bumps. `grain sandbox-image contract` prints the current contract and
// required boundary components, and `grain sandbox-image check <image>` checks
// an image against that contract.

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/sandboximage"
)

var defaultK8sSandboxImage = model.DefaultK8sSandboxImage

func init() {
	if defaultK8sSandboxImage != model.DefaultK8sSandboxImage {
		daemon.DefaultK8sSandboxImage = defaultK8sSandboxImage
	}
}

func sandboxImageCmd(args []string) {
	if err := runSandboxImageCmd(args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

func runSandboxImageCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "contract":
			return runSandboxImageContractCmd(args[1:], stdout, stderr)
		case "check":
			return runSandboxImageCheckCmd(args[1:], stdout, stderr)
		case "base":
			fmt.Fprintln(stdout, sandboximage.BaseImageFor(daemon.DefaultSandboxImage, "guest-base", sandboximage.DefaultGuestBaseImage))
			return nil
		}
	}
	fs := flag.NewFlagSet("grain sandbox-image", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.Bool("base", false, "print the minimal guest-base image reference instead of the full guest image")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base {
		fmt.Fprintln(stdout, sandboximage.BaseImageFor(daemon.DefaultSandboxImage, "guest-base", sandboximage.DefaultGuestBaseImage))
		return nil
	}
	fmt.Fprintln(stdout, daemon.DefaultSandboxImage)
	return nil
}

func runSandboxImageContractCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain sandbox-image contract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print contract details as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := sandboximage.GetContractInfo(daemon.DefaultSandboxImage, defaultK8sSandboxImage)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	fmt.Fprint(stdout, sandboximage.FormatContractInfo(info))
	return nil
}

func runSandboxImageCheckCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain sandbox-image check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	backend := fs.String("backend", "auto", "sandbox backend to verify against: auto, granule, k8s, or container")
	probe := fs.Bool("probe", false, "also run the in-container boundary verification script (VerifyScript) via docker run")
	dockerBin := fs.String("docker", "docker", "docker binary to inspect/probe the image with")
	asJSON := fs.Bool("json", false, "print check result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		err := fmt.Errorf("usage: grain sandbox-image check [-backend auto|granule|k8s|container] [-probe] [-json] <image>")
		fmt.Fprintln(stderr, err)
		return err
	}
	report, err := sandboximage.InspectAndCheckImage(context.Background(), *dockerBin, fs.Arg(0), *backend, *probe)
	if err != nil {
		fmt.Fprintf(stderr, "grain sandbox-image check: %v\n", err)
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Fprint(stdout, sandboximage.FormatImageCheckReport(report))
	}
	if !report.OK {
		return fmt.Errorf("sandbox image %s does not satisfy contract", report.Image)
	}
	return nil
}

// The local Docker and Kubernetes backends run the same user-space image.
// Give each backend a command in its own vocabulary so callers do not have
// to pretend a native Docker deployment is using Kubernetes.
func containerSandboxImageCmd(args []string) {
	if len(args) > 0 && args[0] == "base" {
		fmt.Println(sandboximage.BaseImageFor(defaultK8sSandboxImage, "k8s-sandbox-base", sandboximage.DefaultK8sSandboxBaseImage))
		return
	}
	fs := flag.NewFlagSet("grain container-sandbox-image", flag.ExitOnError)
	base := fs.Bool("base", false, "print the minimal k8s-sandbox-base image reference")
	if err := fs.Parse(args); err != nil {
		return
	}
	if *base {
		fmt.Println(sandboximage.BaseImageFor(defaultK8sSandboxImage, "k8s-sandbox-base", sandboximage.DefaultK8sSandboxBaseImage))
		return
	}
	fmt.Println(defaultK8sSandboxImage)
}

func k8sSandboxImageCmd(args []string) {
	if len(args) > 0 && args[0] == "base" {
		fmt.Println(sandboximage.BaseImageFor(defaultK8sSandboxImage, "k8s-sandbox-base", sandboximage.DefaultK8sSandboxBaseImage))
		return
	}
	fs := flag.NewFlagSet("grain k8s-sandbox-image", flag.ExitOnError)
	base := fs.Bool("base", false, "print the minimal k8s-sandbox-base image reference")
	if err := fs.Parse(args); err != nil {
		return
	}
	if *base {
		fmt.Println(sandboximage.BaseImageFor(defaultK8sSandboxImage, "k8s-sandbox-base", sandboximage.DefaultK8sSandboxBaseImage))
		return
	}
	fmt.Println(defaultK8sSandboxImage)
}
