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

package lifecycle

// Assembly: this deployment's container half, one repository's guest disk,
// and no RUN anywhere in between.
//
// Driven through the Runner seam rather than against a docker daemon, so
// what is under test is the policy -- which commands are run, in which
// order, what is copied, and which images are refused -- rather than
// docker's behaviour.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/qcow2"
)

// Digests that are the right shape and belong to nothing.
const (
	testDigest      = "sha256:9f2ce1aa00000000000000000000000000000000000000000000000000000001"
	otherRepoDigest = "sha256:9f2ce1aa00000000000000000000000000000000000000000000000000000002"
)

// fakeDocker stands in for the daemon. guest is what the repository's
// image is pretending to carry under /var/lib/granule/guest.
type fakeDocker struct {
	guest    map[string]string
	commands [][]string
	haveTags map[string]bool
	labels   map[string]string // tag -> its grain.lifecycle.image label
	manifest string
	failOn   string
}

func (f *fakeDocker) run(_ context.Context, out io.Writer, argv ...string) error {
	f.commands = append(f.commands, argv)
	if f.failOn != "" && strings.Contains(strings.Join(argv, " "), f.failOn) {
		return errors.New("fake docker: refused " + f.failOn)
	}
	switch argv[1] {
	case "manifest":
		fmt.Fprint(out, f.manifest)
	case "build":
		f.build(argv)
	case "image":
		switch argv[2] {
		case "ls":
			for _, tag := range f.matching(flag(argv, "--filter")) {
				fmt.Fprintln(out, tag)
			}
		case "rm":
			delete(f.haveTags, argv[len(argv)-1])
			delete(f.labels, argv[len(argv)-1])
		default:
			if !f.haveTags[argv[len(argv)-1]] {
				return errors.New("no such image")
			}
		}
	case "create":
		fmt.Fprintln(out, "cafebabe1234")
	case "cp":
		// `docker cp <container>:<path>/. <dir>` -- write out whatever
		// the repository's image is pretending to carry.
		dir := argv[len(argv)-1]
		if f.guest == nil {
			return errors.New("no such directory in container")
		}
		for name, body := range f.guest {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// build remembers a built tag the way a daemon does: the image, and the
// label that says whose lifecycle it was assembled from.
func (f *fakeDocker) build(argv []string) {
	tag := flag(argv, "-t")
	if tag == "" {
		return
	}
	if f.haveTags == nil {
		f.haveTags = map[string]bool{}
	}
	if f.labels == nil {
		f.labels = map[string]string{}
	}
	f.haveTags[tag] = true
	for i, arg := range argv {
		if arg == "--label" && i+1 < len(argv) {
			if key, value, ok := strings.Cut(argv[i+1], "="); ok && key == LabelImage {
				f.labels[tag] = value
			}
		}
	}
}

// matching answers `docker image ls --filter label=grain.lifecycle.image=x`.
func (f *fakeDocker) matching(filter string) []string {
	want := strings.TrimPrefix(filter, "label="+LabelImage+"=")
	var tags []string
	for tag := range f.haveTags {
		if f.labels[tag] == want {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

// flag reads the argument after name, the way the real docker would.
func flag(argv []string, name string) string {
	for i, arg := range argv {
		if arg == name && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func (f *fakeDocker) ran(name string) []string {
	for _, argv := range f.commands {
		if len(argv) > 1 && argv[1] == name {
			return argv
		}
	}
	return nil
}

func images(f *fakeDocker) *DockerImages {
	return &DockerImages{StockGuest: "ghcr.io/bwsalmon/grain-sandbox:v9", Runner: f.run}
}

func TestResolveReadsTheDigestBehindTheReleaseTag(t *testing.T) {
	f := &fakeDocker{manifest: `{"Ref":"ghcr.io/acme/billing/lc:c0ffee","Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"` + testDigest + `","size":1234}}`}
	digest, err := images(f).Resolve(context.Background(), "ghcr.io/acme/billing/lc", "c0ffee")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if digest != testDigest {
		t.Errorf("digest = %q", digest)
	}
	argv := f.ran("manifest")
	if argv == nil || argv[len(argv)-1] != "ghcr.io/acme/billing/lc:c0ffee" {
		t.Errorf("inspected %q", argv)
	}
}

// A tag that has never resolved is a repository whose release job has not
// published yet, and the error says the reference it looked for.
func TestResolveSaysWhichReferenceIsMissing(t *testing.T) {
	f := &fakeDocker{failOn: "manifest"}
	_, err := images(f).Resolve(context.Background(), "ghcr.io/acme/billing/lc", "c0ffee")
	if err == nil || !strings.Contains(err.Error(), "ghcr.io/acme/billing/lc:c0ffee") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareAssemblesTheContainerHalfWithTheRepositorysDisk(t *testing.T) {
	f := &fakeDocker{guest: map[string]string{
		GuestDisk:          "a raw disk",
		"layer.01.qcow2":   overlay(t, GuestDisk),
		"vmlinuz":          "the repository's kernel, which must not travel",
		"initrd.img":       "nor this",
		"kontur-agent.bin": "nor this",
	}}
	d := images(f)
	var built string
	d.Runner = func(ctx context.Context, out io.Writer, argv ...string) error {
		if argv[1] == "build" {
			built = argv[len(argv)-1]
			// Read the context before the fake returns and it is removed.
			body, err := os.ReadFile(filepath.Join(built, "Dockerfile"))
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "RUN") {
					return errors.New("the assembly executes something out of the repository's image")
				}
			}
			names, err := os.ReadDir(filepath.Join(built, "guest"))
			if err != nil {
				return err
			}
			var kept []string
			for _, entry := range names {
				kept = append(kept, entry.Name())
			}
			if len(kept) != 2 {
				return fmt.Errorf("the build context carries %v", kept)
			}
			return nil
		}
		return f.run(ctx, out, argv...)
	}

	local, err := d.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !strings.HasPrefix(local, AssembledRepository+":") {
		t.Errorf("assembled image %q", local)
	}
	if built == "" {
		t.Fatal("nothing was built")
	}
	// The repository's image is opened, never run: `docker create` starts
	// nothing and `docker cp` reads a filesystem.
	if f.ran("create") == nil || f.ran("cp") == nil {
		t.Errorf("commands = %v", f.commands)
	}
	for _, argv := range f.commands {
		if argv[1] == "run" || argv[1] == "start" {
			t.Errorf("the repository's image was executed: %v", argv)
		}
	}
	// The container half is always this deployment's current one, so a
	// kontur fix reaches every repository's lifecycle without a rebuild.
	if second, err := d.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest); err != nil || second != local {
		t.Errorf("second Prepare: %q, %v", second, err)
	}
	other := &DockerImages{StockGuest: "ghcr.io/bwsalmon/grain-sandbox:v10", Runner: f.run}
	moved, err := other.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
	if err != nil {
		t.Fatal(err)
	}
	if moved == local {
		t.Error("an upgraded container half reused the image assembled onto the old one")
	}
}

// An assembled image already on the host costs an inspect and nothing
// else.
func TestPrepareReusesAnImageThisHostAlreadyHas(t *testing.T) {
	f := &fakeDocker{guest: map[string]string{GuestDisk: "a raw disk"}}
	d := images(f)
	local, err := d.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
	if err != nil {
		t.Fatal(err)
	}
	f.haveTags = map[string]bool{local: true}
	f.commands = nil
	if again, err := d.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest); err != nil || again != local {
		t.Fatalf("%q, %v", again, err)
	}
	if f.ran("pull") != nil {
		t.Errorf("it pulled an image it already had: %v", f.commands)
	}
}

// The one check on the pulled image, and it is a refusal rather than a
// warning: an image with nothing at /var/lib/granule/guest is not a guest.
// There is deliberately no base-label check beside it -- a label is a
// string the image supplies about itself.
func TestPrepareRefusesAnImageThatCarriesNoGuest(t *testing.T) {
	for name, guest := range map[string]map[string]string{
		"nothing there at all": nil,
		"a directory with no disk in it": {
			"vmlinuz": "a kernel", "initrd.img": "an initramfs",
		},
	} {
		f := &fakeDocker{guest: guest}
		_, err := images(f).Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("%s: %v is not a refusal, so it would read as `failed` rather than `refused`", name, err)
		}
		if !strings.Contains(err.Error(), GuestDir) {
			t.Errorf("%s: %v does not name what is missing", name, err)
		}
	}
}

// A qcow2 records its backing file as a path, so a chain with a hole in
// it is refused before the first boot rather than discovered as a guest
// that will not start. This is what the fixed naming convention is for.
func TestPrepareRefusesABrokenBackingChain(t *testing.T) {
	f := &fakeDocker{guest: map[string]string{
		GuestDisk:        "a raw disk",
		"layer.01.qcow2": overlay(t, GuestDisk),
		"layer.03.qcow2": overlay(t, "layer.02.qcow2"),
	}}
	_, err := images(f).Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
	if err == nil {
		t.Fatal("a chain with a hole in it was assembled")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("%v is not a refusal", err)
	}
	if !strings.Contains(err.Error(), "layer.02.qcow2") {
		t.Errorf("%v does not name the missing link", err)
	}
}

// An assembled guest is gigabytes, and its tag is content-addressed over
// (stock guest, digest) -- so an upgrade names every serving repository's
// image afresh and leaves the one it replaces behind. Nothing serves that
// one any more, and it goes.
func TestPrepareRemovesTheAssembledImagesItSupersedes(t *testing.T) {
	const billing, payroll = "ghcr.io/acme/billing/lc", "ghcr.io/acme/payroll/lc"
	f := &fakeDocker{guest: map[string]string{GuestDisk: "a raw disk"}}
	superseded, err := images(f).Prepare(context.Background(), billing, testDigest)
	if err != nil {
		t.Fatal(err)
	}
	// A second repository, served by this deployment from the same stock
	// guest. Pruning billing's images must not reach it.
	kept, err := images(f).Prepare(context.Background(), payroll, otherRepoDigest)
	if err != nil {
		t.Fatal(err)
	}
	if built := f.ran("build"); !strings.Contains(strings.Join(built, " "), LabelImage+"="+billing) {
		t.Fatalf("an assembled image says nothing about whose it is: %v", built)
	}

	// The upgrade: the same repository at the same digest, on this
	// deployment's new container half.
	upgraded := &DockerImages{StockGuest: "ghcr.io/bwsalmon/grain-sandbox:v10", Runner: f.run}
	local, err := upgraded.Prepare(context.Background(), billing, testDigest)
	if err != nil {
		t.Fatal(err)
	}
	if local == superseded {
		t.Fatal("the upgrade reassembled onto the same tag")
	}
	if f.haveTags[superseded] {
		t.Errorf("%s is still on the host, and nothing will ever boot it: %v", superseded, f.commands)
	}
	if !f.haveTags[local] {
		t.Errorf("the prune removed the image it had just assembled: %v", f.commands)
	}
	if !f.haveTags[kept] {
		t.Errorf("pruning %s's images removed %s's assembled guest %s", billing, payroll, kept)
	}
}

// docker refuses to remove an image a container was created from, which
// is the right answer for one a sandbox is still serving from -- and a
// line in the journal rather than a failed assembly.
func TestPrepareSurvivesAnImageDockerWillNotRemove(t *testing.T) {
	const billing = "ghcr.io/acme/billing/lc"
	f := &fakeDocker{guest: map[string]string{GuestDisk: "a raw disk"}}
	serving, err := images(f).Prepare(context.Background(), billing, testDigest)
	if err != nil {
		t.Fatal(err)
	}

	f.failOn = "image rm"
	var logged []string
	upgraded := &DockerImages{
		StockGuest: "ghcr.io/bwsalmon/grain-sandbox:v10", Runner: f.run,
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	local, err := upgraded.Prepare(context.Background(), billing, testDigest)
	if err != nil {
		t.Fatalf("a refusal to remove an old image failed an assembly that had succeeded: %v", err)
	}
	if local == "" || !f.haveTags[serving] {
		t.Errorf("local = %q, and the refused image is %v", local, f.haveTags)
	}
	if !strings.Contains(strings.Join(logged, "\n"), serving) {
		t.Errorf("nothing in the journal says what was left behind: %v", logged)
	}
}

func TestPrepareNeedsAStockGuestToAssembleOnto(t *testing.T) {
	d := &DockerImages{Runner: (&fakeDocker{}).run}
	if _, err := d.Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest); err == nil {
		t.Fatal("it assembled onto nothing")
	}
}

func TestFindDigestTakesTheManifestsOwn(t *testing.T) {
	for name, body := range map[string]string{
		"verbose": `{"Descriptor":{"digest":"` + testDigest + `","size":1}}`,
		"spaced":  `{ "digest" : "` + testDigest + `" }`,
		"nested":  `{"SchemaV2Manifest":{"config":{"digest":"sha256:aaa"}},"Descriptor":{"digest":"` + testDigest + `"}}`,
	} {
		if got := findDigest(body); got == "" {
			t.Errorf("%s: found nothing in %s", name, body)
		}
	}
	if got := findDigest(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`); got != "" {
		t.Errorf("found %q where there is no digest", got)
	}
}

// overlay is a real qcow2 layer backed by backing, as the bytes the fake
// image carries.
func overlay(t *testing.T, backing string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "layer.qcow2")
	if err := qcow2.WriteOverlay(path, backing, 1<<20, qcow2.WithClusterBits(16)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The names are only the convention; a layer's header is what the
// hypervisor follows, with backing_files=on, inside a privileged
// container. A correctly named chain whose pointers lead anywhere but the
// sibling beneath each layer is refused (#2396).
func TestPrepareRefusesABackingPointerThatLeavesTheChain(t *testing.T) {
	notQcow2 := "an overlay in name only"
	rawQcow2 := overlay(t, "")
	for name, guest := range map[string]map[string]string{
		"an absolute backing path": {
			GuestDisk: "a raw disk", "layer.01.qcow2": overlay(t, "/etc/passwd"),
		},
		"a backing path out of the directory": {
			GuestDisk: "a raw disk", "layer.01.qcow2": overlay(t, "../../../etc/passwd"),
		},
		"a backing path through a subdirectory": {
			GuestDisk: "a raw disk", "layer.01.qcow2": overlay(t, "./"+GuestDisk),
		},
		"a layer that skips the one beneath it": {
			GuestDisk:        "a raw disk",
			"layer.01.qcow2": overlay(t, GuestDisk),
			"layer.02.qcow2": overlay(t, GuestDisk),
		},
		"a layer with no backing file": {
			GuestDisk: "a raw disk", "layer.01.qcow2": rawQcow2,
		},
		"a layer that is not a qcow2": {
			GuestDisk: "a raw disk", "layer.01.qcow2": notQcow2,
		},
		"a base that is a qcow2 with a backing file of its own": {
			GuestDisk: overlay(t, "/etc/shadow"), "layer.01.qcow2": overlay(t, GuestDisk),
		},
		"a base that is a qcow2 at all": {
			GuestDisk: rawQcow2,
		},
	} {
		f := &fakeDocker{guest: guest}
		_, err := images(f).Prepare(context.Background(), "ghcr.io/acme/billing/lc", testDigest)
		if err == nil {
			t.Errorf("%s: assembled", name)
			continue
		}
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("%s: %v is not a refusal", name, err)
		}
		if f.ran("build") != nil {
			t.Errorf("%s: built before refusing", name)
		}
	}
}

// A symlink in the guest directory would have its target's header read
// on the host and its link copied into the container; it is refused
// rather than followed.
func TestKeepOnlyTheDiskRefusesASymlinkedDisk(t *testing.T) {
	for _, name := range []string{GuestDisk, "layer.01.qcow2"} {
		guest := t.TempDir()
		if err := os.WriteFile(filepath.Join(guest, GuestDisk), []byte("a raw disk"), 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(target, []byte(overlay(t, GuestDisk)), 0o644); err != nil {
			t.Fatal(err)
		}
		if name == GuestDisk {
			os.Remove(filepath.Join(guest, GuestDisk))
		}
		if err := os.Symlink(target, filepath.Join(guest, name)); err != nil {
			t.Fatal(err)
		}
		err := (&DockerImages{}).keepOnlyTheDisk("ghcr.io/acme/billing/lc", guest)
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("a symlinked %s: %v", name, err)
		}
	}
}

// The chain the convention describes, with the pointers it describes, is
// accepted.
func TestKeepOnlyTheDiskAcceptsAWellFormedChain(t *testing.T) {
	guest := t.TempDir()
	for name, body := range map[string]string{
		GuestDisk:        "a raw disk",
		"layer.01.qcow2": overlay(t, GuestDisk),
		"layer.02.qcow2": overlay(t, "layer.01.qcow2"),
		"layer.03.qcow2": overlay(t, "layer.02.qcow2"),
	} {
		if err := os.WriteFile(filepath.Join(guest, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&DockerImages{}).keepOnlyTheDisk("ghcr.io/acme/billing/lc", guest); err != nil {
		t.Fatal(err)
	}
}
