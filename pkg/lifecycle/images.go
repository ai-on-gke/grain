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

// Grain does not boot a repository's image. It assembles one.
//
// Everything in this file follows from one problem, stated in full in
// docs/task-lifecycle-sandbox.md's "The wrinkle": this is the first time
// grain boots a sandbox whose image was chosen by repository content, and
// "is this really a kontur guest" does not have a check-shaped answer. A
// label is a string the image supplies about itself. Layer provenance
// answers less than it looks like it does, because a docker commit layer
// is a whole filesystem diff and the top layer is the attacker's -- it
// can replace /usr/bin/kontur, the entrypoint, and the guest disk itself.
// And the consequence is not merely "a container instead of a VM": on the
// kontur backend NetworkNone is enforced cooperatively *inside* the
// guest, by exec'ing `ip route del default` and reading /proc/net/route
// back through the same control link, so an image that is not a guest
// answers exit 0 with an empty route table and is handed a sandbox that
// still has the namespace's network.
//
// The answer is to stop treating the image as a machine. A kontur guest
// image has two halves: cloud-hypervisor and kontur are the container half,
// and /var/lib/granule/guest is the disk, kernel and initramfs. Everything
// grain's isolation rests on is in the first; everything a repository
// needs to customize is in the second. So grain takes its own container
// half and the repository's guest disk, and nothing else.
//
// There is no RUN in the assembly. A COPY out of a pulled layer executes
// no code from the repository's image, which is what makes it safe to do
// host-side, next to the daemon, where a host-side *build* would not be.
//
// What this settles, and what it does not, is worth keeping straight:
// /usr/bin/kontur is grain's, so the fake-VMM attack is gone at the root
// and the VM boundary is real. "Is it a kontur guest" then answers itself
// by function rather than by label -- a guest disk not derived from the
// stock one has no kontur-control-net service, so it has no route in
// under flat mode, so the control link never answers and readiness times
// out. That is the check, and it needed no checking. What it does not
// settle is enforceGuestNetwork, which is still cooperative; the worst
// case is now a real VM that kept the network an ordinary sandbox has,
// which is a promise unkept rather than a boundary crossed.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/qcow2"
)

// GuestDir is the half of a kontur guest image a repository supplies, and
// the only part of one grain reads. Repeated from kontur's own layout
// rather than imported because it is a path in an image, not in this
// process.
const GuestDir = "/var/lib/granule/guest"

// GuestDisk is the raw base every guest has, and the one file an image
// must carry to be a guest at all.
const GuestDisk = "disk.img"

// AssembledRepository is the local image repository every assembled
// serving guest is committed under.
//
// Local, and deliberately not a name any registry serves: an assembled
// image is this deployment's container half welded to one repository's
// disk, and pushing one would publish a mixture neither side asked for.
const AssembledRepository = "grain-lifecycle-guest"

// LabelImage and LabelDigest say, on an assembled image, which
// repository's lifecycle image it was assembled from and at which
// digest.
//
// The tag cannot say it: it is a hash of (stock guest, digest), which is
// the right cache key and tells an operator -- and pruneOthers -- nothing
// about whose image it is. The labels are what make "the assembled images
// of this lifecycle image that are not the one being served" a set docker
// can be asked for, and they make `docker image ls --filter
// label=grain.lifecycle.image=<image>` answer the same question by hand.
// Named the way pkg/orchestrator names grain.sandbox.
const (
	LabelImage  = "grain.lifecycle.image"
	LabelDigest = "grain.lifecycle.digest"
)

// layerName is the fixed naming convention the qcow2 chain uses:
// disk.img is the raw base, layer.01.qcow2 is backed by it, layer.02.qcow2
// by layer.01, and so on (docs/kontur-guest-images.md). The convention has
// to be exact because a qcow2 records its backing file as a *path*, which
// is resolved inside the container -- layers copied over with their
// backing pointers intact only work if the convention puts them where
// those pointers say.
var layerName = regexp.MustCompile(`^layer\.(\d{2,})\.qcow2$`)

// Refusal is an image grain will not serve, as opposed to one it could
// not fetch.
//
// The distinction is the difference between "fix the image" and "try
// again", and it is what lets a caller report `refused` rather than
// `failed` -- so an operator is told to look at what their release job
// published instead of at the registry.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

// DockerImages is Images against the host's docker daemon.
//
// Runner is the seam that lets a test drive all of this without a
// registry, a daemon or a guest anywhere -- pkg/sandboximage's own
// arrangement, for its own reason.
type DockerImages struct {
	// Docker is the client binary. Empty is "docker".
	Docker string
	// StockGuest is this deployment's own guest image: the container half
	// every assembled image is built on. Required.
	//
	// It is always the *current* one, which is the point: an upgrade moves
	// the container half for every deployment at once, so a kontur or
	// cloud-hypervisor fix reaches every repository's lifecycle without a
	// single rebuild. Refusing an image whose base no longer matched would
	// have stranded every repository on every upgrade, to enforce
	// something a label could not enforce anyway.
	StockGuest string
	// TempDir is where guest halves are staged. Empty is the system's.
	TempDir string
	// Runner runs one command, collecting its output. Empty is os/exec.
	Runner func(ctx context.Context, out io.Writer, argv ...string) error
	// Log is optional, and is where pruning says what it removed and what
	// it left behind.
	Log func(format string, args ...any)
}

// Resolve reads the digest behind one tag.
//
// A `docker manifest inspect`, which needs no CI feature that does not
// exist: there is no need for the release job to report an output, and
// grain CI has no way for it to.
func (d *DockerImages) Resolve(ctx context.Context, image, tag string) (string, error) {
	reference := image + ":" + tag
	var out bytes.Buffer
	if err := d.run(ctx, &out, d.docker(), "manifest", "inspect", "--verbose", reference); err != nil {
		return "", fmt.Errorf("%s has not been published: %w: %s", reference, err, trim(out.String()))
	}
	digest := findDigest(out.String())
	if digest == "" {
		return "", fmt.Errorf("%s's manifest carries no digest", reference)
	}
	return digest, nil
}

// Prepare pulls the repository's image and assembles a servable guest
// from it and this deployment's own container half.
//
// The result is named after its inputs, sandboximage's own trick: the
// stock guest and the pinned digest hashed together. An upgrade that
// moves the stock guest names an image that does not exist yet and so
// reassembles; anything else names one already on the host and costs an
// image inspect.
//
// Either way it ends in a prune of what this repository's lifecycle no
// longer serves, the already-assembled path included: that path is an
// image ls and usually nothing else, and running it there is what
// collects an assembly whose own prune was interrupted -- by a shutdown,
// or by a cancelled dispatch -- rather than leaving it until the next
// time the stock guest happens to move.
func (d *DockerImages) Prepare(ctx context.Context, image, digest string) (string, error) {
	if strings.TrimSpace(d.StockGuest) == "" {
		return "", errors.New("no stock guest image to assemble onto")
	}
	local := AssembledRepository + ":" + assembledTag(d.StockGuest, digest)
	if d.have(ctx, local) {
		d.pruneOthers(ctx, image, local)
		return local, nil
	}

	reference := image + "@" + digest
	if err := d.run(ctx, io.Discard, d.docker(), "pull", "--quiet", reference); err != nil {
		return "", fmt.Errorf("pulling %s: %w", reference, err)
	}

	staged, err := os.MkdirTemp(d.TempDir, "grain-lifecycle-guest-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)

	if err := d.extractGuest(ctx, reference, staged); err != nil {
		return "", err
	}
	if err := d.writeDockerfile(staged); err != nil {
		return "", err
	}
	if err := d.run(ctx, io.Discard, d.docker(), "build", "--quiet",
		"--label", LabelImage+"="+image, "--label", LabelDigest+"="+digest,
		"-t", local, staged); err != nil {
		return "", fmt.Errorf("assembling %s: %w", local, err)
	}
	d.pruneOthers(ctx, image, local)
	return local, nil
}

// pruneOthers removes the assembled images of one repository's lifecycle
// image that this deployment no longer serves -- every tag labelled with
// that image but the one just prepared.
//
// The reason is the one any locally assembled guest image has: guest images
// are gigabytes each, and the tag here is content-addressed over (stock
// guest, digest), which is exactly what makes them accumulate. Every
// upgrade moves the stock guest, so every serving repository's assembled
// image is named afresh and the one it replaces would otherwise sit in
// docker's data root for ever, boot by nothing.
//
// Keyed on the declared image rather than on the whole of
// AssembledRepository, because that repository holds every serving
// repository's assembled guest and this call knows only about one of
// them. What a repository no longer serves is safe to remove: Registry
// .start refuses any version but the one its repository has resolved, so
// an older assembled image is already unservable rather than merely
// unused. A repository that changes its declared image leaves the old
// image's assemblies behind, which is one stale image per rename and the
// price of not having to know what the rest of the deployment serves.
//
// Best effort in every sense: docker refuses to remove an image a
// container was created from, which is exactly the right answer for one a
// sandbox is still serving from, and a refusal here is a line in the
// journal rather than a failure of an assembly that has already
// succeeded.
func (d *DockerImages) pruneOthers(ctx context.Context, image, keep string) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var buf bytes.Buffer
	if err := d.run(ctx, &buf, d.docker(), "image", "ls", "--format", "{{.Repository}}:{{.Tag}}",
		"--filter", "label="+LabelImage+"="+image, AssembledRepository); err != nil {
		d.logf("lifecycle: listing the assembled images of %s to remove: %v", image, err)
		return
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		old := strings.TrimSpace(line)
		if old == "" || old == keep || !strings.HasPrefix(old, AssembledRepository+":") {
			continue
		}
		if err := d.run(ctx, io.Discard, d.docker(), "image", "rm", old); err != nil {
			d.logf("lifecycle: leaving %s in place (a sandbox may still be serving from it): %v", old, err)
			continue
		}
		d.logf("lifecycle: removed %s, the assembled guest of %s that nothing serves any more", old, image)
	}
}

// extractGuest copies the repository's guest half out of its image
// without running any of it.
//
// `docker create` makes a container and starts nothing, and `docker cp`
// reads that container's filesystem, so between them no instruction from
// the repository's image is executed -- which is the property this whole
// arrangement exists to keep.
func (d *DockerImages) extractGuest(ctx context.Context, reference, staged string) error {
	var created bytes.Buffer
	// An entrypoint that is never run, named only because `docker create`
	// wants a command for an image whose own may be absent.
	if err := d.run(ctx, &created, d.docker(), "create", "--entrypoint", "/nonexistent", reference); err != nil {
		return fmt.Errorf("opening %s to read its guest: %w: %s", reference, err, trim(created.String()))
	}
	container := trim(created.String())
	if i := strings.LastIndexByte(container, '\n'); i >= 0 {
		container = strings.TrimSpace(container[i+1:])
	}
	defer d.run(context.WithoutCancel(ctx), io.Discard, d.docker(), "rm", "-f", container)

	guest := filepath.Join(staged, "guest")
	if err := os.MkdirAll(guest, 0o755); err != nil {
		return err
	}
	var copied bytes.Buffer
	if err := d.run(ctx, &copied, d.docker(), "cp", container+":"+GuestDir+"/.", guest); err != nil {
		return &Refusal{Reason: fmt.Sprintf("%s carries no %s, so it is not a kontur guest image: %v: %s",
			reference, GuestDir, err, trim(copied.String()))}
	}
	return d.keepOnlyTheDisk(reference, guest)
}

// keepOnlyTheDisk reduces what was extracted to the disk and the qcow2
// layers stacked on it, and refuses what is left if it is not a chain.
//
// The kernel and initramfs beside them stay this deployment's, which is
// the whole reason the copy is selective: everything grain's isolation
// rests on has to come from the stock guest, and everything a repository
// needs to customize is inside the disk.
func (d *DockerImages) keepOnlyTheDisk(reference, guest string) error {
	entries, err := os.ReadDir(guest)
	if err != nil {
		return err
	}
	var layers []string
	disk := false
	for _, entry := range entries {
		name := entry.Name()
		// A symlink is refused rather than followed: reading its header
		// would open whatever host path it names, and copying it on would
		// hand the guest that path inside the container.
		if (name == GuestDisk || layerName.MatchString(name)) && !entry.Type().IsRegular() {
			return &Refusal{Reason: fmt.Sprintf("%s's guest %s is not a regular file (%s); "+
				"a guest disk and its layers are copied as files, not links", reference, name, entry.Type())}
		}
		switch {
		case name == GuestDisk:
			disk = true
		case layerName.MatchString(name):
			layers = append(layers, name)
		default:
			if err := os.RemoveAll(filepath.Join(guest, name)); err != nil {
				return err
			}
		}
	}
	if !disk {
		return &Refusal{Reason: fmt.Sprintf("%s has no %s/%s in it, so there is no guest to serve from. "+
			"A lifecycle image is built from this deployment's stock guest (konturctl guest build), "+
			"not from an ordinary container base", reference, GuestDir, GuestDisk)}
	}
	sort.Strings(layers)
	// Walk the chain: every layer is backed by the one before it, and
	// layer.01 by disk.img. Checked here, at assembly, rather than
	// discovered as a guest that will not start -- which is what the fixed
	// naming convention is for.
	for i, layer := range layers {
		want := fmt.Sprintf("layer.%02d.qcow2", i+1)
		if layer != want {
			return &Refusal{Reason: fmt.Sprintf("%s's guest layers are %v, which is not a chain: "+
				"%s is backed by %s and every layer after it by the one before, and a qcow2 records "+
				"its backing file as a path that has to resolve", reference, layers, want,
				previousInChain(i))}
		}
	}
	// The names say what the chain should be; the headers say what the
	// hypervisor will actually open. The guest boots with
	// backing_files=on, so a layer's backing pointer is followed as it is
	// written -- an absolute path, or a ../, reaches past the guest
	// directory into the container, which is privileged. So every pointer
	// has to be exactly the sibling the convention names, and the raw
	// base has to be raw, with nothing for a format probe to follow.
	if err := checkRawBase(reference, filepath.Join(guest, GuestDisk)); err != nil {
		return err
	}
	for i, layer := range layers {
		if err := checkBacking(reference, filepath.Join(guest, layer), previousInChain(i)); err != nil {
			return err
		}
	}
	return nil
}

// maxBackingFileSize is the qcow2 specification's own bound on a backing
// file name. Checked before qcow2.ReadHeader is let near a layer, because
// the header's size field is what ReadHeader allocates from.
const maxBackingFileSize = 1023

// incompatibleDataFile is qcow2's "external data file" feature bit: the
// guest's clusters live in a second file, named by path in a header
// extension -- a pointer out of the image exactly like a backing file.
const incompatibleDataFile = 1 << 2

// checkRawBase refuses a disk.img that is a qcow2 rather than the raw
// base the convention says it is. A raw base names nothing; a qcow2 one
// can name a backing file of its own, and layer.01's pointer to it may
// leave the format to be probed.
func checkRawBase(reference, path string) error {
	if qcow2.IsQcow2(path) {
		return &Refusal{Reason: fmt.Sprintf("%s's guest %s is a qcow2 image, not a raw disk: "+
			"the base of a guest's chain is raw, and only the layer.NN.qcow2 files above it are qcow2",
			reference, GuestDisk)}
	}
	return nil
}

// checkBacking refuses a layer whose header points anywhere but want,
// the sibling the naming convention puts beneath it.
func checkBacking(reference, path, want string) error {
	refuse := func(why string) error {
		return &Refusal{Reason: fmt.Sprintf("%s's guest layer %s %s, and it must be backed by %s, "+
			"the file beside it: the guest follows a layer's backing pointer as it is written",
			reference, filepath.Base(path), why, want)}
	}
	size, err := backingFileSize(path)
	if err != nil {
		return refuse(fmt.Sprintf("is not a qcow2 image (%v)", err))
	}
	if size > maxBackingFileSize {
		return refuse(fmt.Sprintf("claims a %d-byte backing file name", size))
	}
	hdr, err := qcow2.ReadHeader(path)
	if err != nil {
		return refuse(fmt.Sprintf("is not a qcow2 image (%v)", err))
	}
	if hdr.IncompatibleFeatures&incompatibleDataFile != 0 {
		return refuse("keeps its data in an external data file")
	}
	backing := hdr.BackingFile
	switch {
	case hdr.BackingFileOffset > 0 && size > 0 && len(backing) != int(size):
		return refuse("has a backing file name that cannot be read")
	case backing == "":
		return refuse("has no backing file")
	case filepath.IsAbs(backing):
		return refuse(fmt.Sprintf("is backed by the absolute path %q", backing))
	case backing != want:
		return refuse(fmt.Sprintf("is backed by %q", backing))
	}
	return nil
}

// backingFileSize reads the header's backing file name length without
// parsing anything else.
func backingFileSize(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var buf [20]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return 0, err
	}
	if binary.BigEndian.Uint32(buf[0:4]) != qcow2.Magic {
		return 0, errors.New("no qcow2 magic")
	}
	return binary.BigEndian.Uint32(buf[16:20]), nil
}

func previousInChain(i int) string {
	if i == 0 {
		return GuestDisk
	}
	return fmt.Sprintf("layer.%02d.qcow2", i)
}

// writeDockerfile is the assembly, and there is no RUN in it.
func (d *DockerImages) writeDockerfile(staged string) error {
	dockerfile := fmt.Sprintf(`# Assembled by pkg/lifecycle: this deployment's container half --
# cloud-hypervisor, kontur, the kernel and the initramfs -- with one
# repository's guest disk stacked onto it. No RUN: nothing from the
# repository's image is executed here or anywhere else on this host.
FROM %s
COPY guest/ %s/
`, d.StockGuest, GuestDir)
	return os.WriteFile(filepath.Join(staged, "Dockerfile"), []byte(dockerfile), 0o644)
}

// have reports whether the assembled image is already on this host.
// Errors are "no": an image inspect that cannot answer is a reason to
// assemble again, not to fail a dispatch over a docker hiccup.
func (d *DockerImages) have(ctx context.Context, local string) bool {
	return d.run(ctx, io.Discard, d.docker(), "image", "inspect", local) == nil
}

// assembledTag names an assembled image after its two inputs.
func assembledTag(stock, digest string) string {
	sum := sha256.Sum256([]byte(stock + "\x00" + digest))
	return hex.EncodeToString(sum[:6])
}

// findDigest reads the manifest's own digest out of a `docker manifest
// inspect --verbose` answer.
//
// Descriptor.digest specifically, and not simply the first "digest" in
// the document: a verbose answer also carries the config's digest and
// every layer's, and pinning an instance to a config blob would be a
// version that resolves to nothing. A manifest list comes back as an
// array of those objects, all of which describe the same index, so the
// first is as good as any.
func findDigest(output string) string {
	type descriptor struct {
		Digest string `json:"digest"`
	}
	type entry struct {
		Descriptor descriptor `json:"Descriptor"`
		Digest     string     `json:"digest"`
	}
	text := strings.TrimSpace(output)
	var single entry
	if err := json.Unmarshal([]byte(text), &single); err == nil {
		if single.Descriptor.Digest != "" {
			return single.Descriptor.Digest
		}
		return single.Digest
	}
	var many []entry
	if err := json.Unmarshal([]byte(text), &many); err == nil {
		for _, one := range many {
			if one.Descriptor.Digest != "" {
				return one.Descriptor.Digest
			}
			if one.Digest != "" {
				return one.Digest
			}
		}
	}
	return ""
}

func (d *DockerImages) docker() string {
	if d.Docker != "" {
		return d.Docker
	}
	return "docker"
}

func (d *DockerImages) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

func (d *DockerImages) run(ctx context.Context, out io.Writer, argv ...string) error {
	if d.Runner != nil {
		return d.Runner(ctx, out, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

func trim(s string) string { return strings.TrimSpace(s) }
