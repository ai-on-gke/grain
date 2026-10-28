// Package dockercli is the small amount of docker CLI driving that
// granule's image-producing commands share: running a build with its
// progress going to the caller's streams, asking an image about itself,
// and refusing an image reference that could not be written into a
// generated Dockerfile safely.
//
// It is the docker *CLI* rather than the daemon's API on purpose. The
// commands that use it (`granule guest pack`, `granule wrap`) run where
// a person or a CI job already has a working docker -- with its
// credentials, its proxy settings, its buildx builder and its
// registry configuration -- and the point is to inherit all of that
// rather than to reimplement any of it.
package dockercli

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// EnvBinary names the environment variable that chooses the container
// CLI granule drives in place of "docker": a path, or a name looked up
// on PATH ("podman", "nerdctl", a wrapper script). `granule -docker`
// sets it for the rest of the process.
const EnvBinary = "GRANULE_DOCKER"

// Binary is the one place granule decides which container CLI to exec
// when a caller has not named one: $GRANULE_DOCKER if it is set, and
// "docker" otherwise. Every package that execs docker falls back to it,
// so one setting reaches a VM's create and delete, a guest build and a
// Granulefile build alike -- and so do children that granule itself
// starts, since it is the environment rather than a flag.
func Binary() string {
	if b := strings.TrimSpace(os.Getenv(EnvBinary)); b != "" {
		return b
	}
	return "docker"
}

// CLI is the docker command line. Binary is resolved via PATH when it
// contains no slash; empty means Binary(), and tests point it at a fake.
type CLI struct{ Binary string }

func (c *CLI) bin() string {
	if c.Binary == "" {
		return Binary()
	}
	return c.Binary
}

// Run execs "docker <args...>", streaming both streams to the caller's
// writers -- a build's progress is on stderr, and packing a large rootfs
// is minutes of it, so it is shown as it happens rather than folded into
// an error at the end. The tail of stderr is kept for the error message
// anyway, since docker says what went wrong there too.
func (c *CLI) Run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var errTail bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &errTail)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, LastLines(errTail.String(), 20))
	}
	return nil
}

// Output runs docker and returns what it printed, for the calls whose
// answer is the point rather than their progress.
func (c *CLI) Output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var out, errTail bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errTail
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, LastLines(errTail.String(), 5))
	}
	return out.String(), nil
}

// ExposedPorts is what an image's own config declares with EXPOSE, in
// docker's own spelling ("8080/tcp").
//
// Asked by every command that rebuilds an image around somebody else's
// machine -- a pack, a rebase -- because the result is built on the
// granule image rather than on theirs, so none of their config comes
// across on its own. What the answer means is the caller's business:
// here it is a lookup, and nothing about it opens a port.
func (c *CLI) ExposedPorts(ctx context.Context, ref string) ([]string, error) {
	out, err := c.Output(ctx, "image", "inspect", "-f", "{{range $port, $_ := .Config.ExposedPorts}}{{$port}} {{end}}", ref)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// Volumes is what an image's own config declares with VOLUME.
func (c *CLI) Volumes(ctx context.Context, ref string) ([]string, error) {
	out, err := c.Output(ctx, "image", "inspect", "-f", "{{range $vol, $_ := .Config.Volumes}}{{$vol}} {{end}}", ref)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// ConfigEnv is what an image's own config records with ENV, one
// "KEY=value" per entry.
//
// For granule that is where a machine's runtime settings live: a
// Granule's VM SET writes them there (docs/granulefile.md §5.5) so that
// nothing has to read a Granule to boot the result. Reading them is
// asking the image how it wants to be booted; whether to honour that is
// the caller's, the same way it is for ExposedPorts above.
func (c *CLI) ConfigEnv(ctx context.Context, ref string) ([]string, error) {
	out, err := c.Output(ctx, "image", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", ref)
	if err != nil {
		return nil, err
	}
	var env []string
	for _, line := range strings.Split(out, "\n") {
		// Not Fields: a value may have spaces in it, which is why each
		// entry is printed on a line of its own.
		if line = strings.TrimSpace(line); line != "" {
			env = append(env, line)
		}
	}
	return env, nil
}

// Label returns the value of a specific label from an image's config, or empty string.
func (c *CLI) Label(ctx context.Context, ref, label string) (string, error) {
	out, err := c.Output(ctx, "image", "inspect", "-f", fmt.Sprintf("{{index .Config.Labels %q}}", label), ref)
	if err != nil {
		return "", err
	}
	res := strings.TrimSpace(out)
	if res == "<no value>" {
		return "", nil
	}
	return res, nil
}

// ImageSize is an image's own reported size in bytes.
func (c *CLI) ImageSize(ctx context.Context, ref string) (int64, error) {
	out, err := c.Output(ctx, "image", "inspect", "-f", "{{.Size}}", ref)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// Files opens an image for reading *without starting anything from it*:
// `docker create` makes a container and runs nothing, and every read
// below is `docker cp`, which copies out of a container that has never
// been started.
//
// That is the property the commands using this are built on -- a pack, a
// rebase and a verify all look inside an image somebody else authored
// before deciding what to do with it, on the machine that runs the
// daemon. Close removes the container; one reader answers any number of
// questions.
func (c *CLI) Files(ctx context.Context, ref string) (*ImageFiles, error) {
	var out bytes.Buffer
	// The command is named explicitly and is never run: an image with no
	// CMD and no ENTRYPOINT cannot be `docker create`d without one, and a
	// guest image somebody else built need not have either. Naming
	// something that says what this is, rather than borrowing the
	// image's own, keeps a stray container in `docker ps -a` legible.
	if err := c.Run(ctx, &out, io.Discard, "create", ref, "granule-reads-this-image-and-runs-nothing"); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(out.String())
	if id == "" {
		return nil, fmt.Errorf("docker create %s said nothing that looks like a container id", ref)
	}
	return &ImageFiles{cli: c, ref: ref, id: id}, nil
}

// ImageFiles reads paths out of one image. See Files.
type ImageFiles struct {
	cli *CLI
	ref string
	id  string
}

// Close removes the container the reads went through. Its own context is
// Background so that a reader whose caller has been cancelled still
// cleans up after itself.
func (f *ImageFiles) Close() {
	_ = f.cli.Run(context.Background(), io.Discard, io.Discard, "rm", "-f", f.id)
}

// Has reports whether path in the image is a regular file.
//
// `docker cp <id>:<path> -` writes a tar, and only the first header is
// read: the question is about a multi-gigabyte disk image, and the answer
// is in the first 512 bytes of it. The copy is cancelled as soon as that
// header is in hand, so nothing else crosses the socket.
func (f *ImageFiles) Has(ctx context.Context, path string) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, f.cli.bin(), "cp", f.id+":"+path, "-")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("docker cp %s:%s: %w", f.id, path, err)
	}
	hdr, rerr := tar.NewReader(stdout).Next()
	cancel()
	_ = cmd.Wait()
	if rerr != nil {
		// "No such container:path" is docker's answer for a path the
		// image does not carry, which is an answer rather than a
		// failure -- the guest kernel pair is absent from most guests.
		if NotFound(errBuf.String()) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s out of %s: %w: %s", path, f.ref, rerr, strings.TrimSpace(errBuf.String()))
	}
	return hdr.Typeflag == tar.TypeReg, nil
}

// CopyOut copies a path out of the image into a directory on this
// machine, again without starting anything from it.
func (f *ImageFiles) CopyOut(ctx context.Context, from, dest string) error {
	return f.cli.Run(ctx, io.Discard, io.Discard, "cp", f.id+":"+from, dest)
}

// HasFile is Has for a caller with one question to ask: it opens the
// image, asks, and removes the container again.
func (c *CLI) HasFile(ctx context.Context, ref, path string) (bool, error) {
	files, err := c.Files(ctx, ref)
	if err != nil {
		return false, err
	}
	defer files.Close()
	return files.Has(ctx, path)
}

// NotFound recognizes docker's several ways of saying a path is not
// there. All of them are the same answer to the question being asked.
//
// The list is this long because the wording is the daemon's and changes
// with it: Docker 29.8.1 answers "Could not find the file <path> in
// container <id>", which none of the older three match. Getting this
// wrong is not a cosmetic failure -- a guest that brings no kernel of
// its own is the *ordinary* guest, so an unrecognized phrasing turns
// every cheap rebase on that daemon into an error about reading a file
// nobody expected to be there.
func NotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no such container:path") ||
		strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "not found in") ||
		strings.Contains(s, "could not find the file")
}

// BuildCouldNotFind reports whether a failed build says that path --
// something the generated build expects one of its inputs to carry -- is
// what it could not find. text is the error a failed Run returns: the
// command, docker's own message and the last lines of the build log.
//
// The question is asked of one line at a time, and that is the whole
// point of this function. buildkit prints the steps it ran into that
// log, so a build that copies from a path has that path in the tail
// whatever it later fails on; looking for the phrase anywhere and the
// path anywhere blames the path for any failure that happens to say
// "not found". That is not hypothetical -- a pack whose packing step
// produced no output failed on
//
//	"/out/var/lib/granule/guest": not found
//
// and was reported as the granule image not carrying
// guestimage.PartsDir, which it did carry and which was two log lines
// above, in a step that had succeeded.
//
// Docker says both halves in one breath when an input really is
// missing, and the line that merely names a step carries no phrase:
//
//	#6 [pack 3/5] COPY --from=granule:latest /usr/local/share/granule/guest-parts/ /parts/
//	#6 DONE 0.2s
//	...
//	ERROR: failed to solve: failed to compute cache key: failed to calculate checksum of ref x::y: "/out/var/lib/granule/guest": not found
//
// One line is what tells those apart. A daemon whose wording puts the
// two on separate lines gets no explanation added and the caller sees
// docker's own error, which is the failure worth having: the wording is
// the daemon's and changes with it (see NotFound), so the matching has
// to fail towards saying nothing rather than towards blaming an image
// that is fine.
func BuildCouldNotFind(text, path string) bool {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, path) {
			continue
		}
		l := strings.ToLower(line)
		if strings.Contains(l, "not found") || strings.Contains(l, "no such file") {
			return true
		}
	}
	return false
}

// BuildCouldNotResolve reports whether a failed build says that ref --
// an image the generated build names as an input -- is the reference it
// could not resolve: a repository that does not exist, one this machine
// has no credentials for, a tag that was never pushed, a registry that
// does not answer. text is the error a failed Run returns, the same as
// above.
//
// A different question from BuildCouldNotFind, and deliberately not
// sharing its wording. "not found" and "no such file" are what docker
// says about a *path* an image does not carry; an unresolvable reference
// says neither on a modern daemon, so asking the path question about a
// reference gets no answer at all. Docker 29.8.1, asked for a repository
// that does not exist and then for one this machine has no token for:
//
//	#2 ERROR: pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed
//	ERROR: failed to build: failed to solve: no-such-guest:nope: failed to resolve source metadata for docker.io/library/no-such-guest:nope: pull access denied, ...
//
//	#3 ERROR: failed to authorize: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=...: 403 Forbidden
//	------
//	 > [internal] load metadata for ghcr.io/gke-labs/granule-guest-rootfs:latest:
//	------
//
// Those are the two shapes matched below. The first names the reference
// on the same line as the failure. The second does not name it anywhere
// near the failure: the message is about a token, and the only line that
// says which image the token was for is the one buildkit prints to head
// the failing vertex's block -- which is why "load metadata for" counts
// only with that block's "> " in front of it. Buildkit prints a
// `[internal] load metadata for <ref>` line for *every* image a build
// pulls, resolved or not, so the bare vertex line means nothing; the
// block header is printed for the vertex that failed and no other.
//
// Asked of one line at a time, for the reason BuildCouldNotFind is: an
// image the build names is in the log tail whatever the build later
// fails on. A wrap that got as far as running a shell in the guest and
// failed there prints the guest's own reference six times in the last
// twenty lines, once on a "> " line of its own -- and that failure is
// not a registry's answer about anything.
func BuildCouldNotResolve(text, ref string) bool {
	repo := repository(ref)
	for _, line := range strings.Split(text, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(line, ref) {
			if strings.Contains(l, "failed to resolve source metadata for") ||
				strings.Contains(l, "failed to resolve reference") ||
				(strings.Contains(l, "load metadata for") && strings.HasPrefix(strings.TrimSpace(line), ">")) {
				return true
			}
		}
		// The deprecated classic builder (DOCKER_BUILDKIT=0), which says
		// it in one line naming the repository without the tag:
		//
		//	pull access denied for no-such-guest, repository does not exist or may require 'docker login'
		//
		// Matched on the repository *and* the comma that follows it, so
		// this stays as line-scoped as the rest: buildkit's own wording
		// for the same failure carries no reference at all, and would
		// otherwise be read as an answer about whichever image was asked
		// about.
		if repo != "" && strings.Contains(line, "pull access denied for "+repo+",") {
			return true
		}
	}
	return false
}

// PullCouldNotResolve reports whether a failed `docker pull` or `docker
// create` failed because the reference would not resolve, rather than
// because of something the daemon, the disk or the container did.
//
// It is the same question BuildCouldNotResolve asks, of the commands
// that reach a registry without building anything -- which is where
// granule meets it most often, since every command here looks inside the
// granule image before it generates a build (see the Files comment
// above). There is no reference to match against and none is needed:
// unlike a build, which pulls every image its Dockerfile names, these
// commands are about exactly one image, so all of what they print is
// about that one. What they print is not the build log's wording either
// (Docker 29.8.1, reproduced 19 Sep 2026):
//
//	docker create ghcr.io/gke-labs/granule:latest granule-reads-this-image-and-runs-nothing
//	Unable to find image 'ghcr.io/gke-labs/granule:latest' locally
//	Error response from daemon: error from registry: denied
//
//	docker pull busybox:no-such-tag-123
//	Error response from daemon: failed to resolve reference "docker.io/library/busybox:no-such-tag-123": docker.io/library/busybox:no-such-tag-123: not found
//
//	docker pull no-such-guest:nope
//	Error response from daemon: pull access denied for no-such-guest, repository does not exist or may require 'docker login'
//
// Matched on those three registry-side phrases rather than on "denied"
// or "not found" anywhere: this decides whether a paragraph about
// registries and credentials is printed, and a create that failed for a
// reason of the daemon's is better left to say so itself.
func PullCouldNotResolve(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "error from registry") ||
		strings.Contains(t, "pull access denied") ||
		strings.Contains(t, "failed to resolve reference")
}

// repository is a reference without the tag or digest that selects one
// image out of it: "ghcr.io/gke-labs/granule-guest-rootfs:latest" is a
// repository and a tag, and the colon before the tag is told from the
// one in a "registry:5000/..." host by the slash after it.
func repository(ref string) string {
	base, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base = base[:i]
	}
	return base
}

// LastLines keeps the end of a build log, which is where the failure is.
func LastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// HumanSize is a byte count as an image size is normally quoted.
func HumanSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(bytes)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// refPattern is what an image reference may contain to be interpolated
// into a generated Dockerfile.
//
// Deliberately narrower than what a registry would accept. Every
// reference granule's image-producing commands take reaches docker by
// being written into a Dockerfile, so a reference carrying a newline is a
// reference that can append instructions to that file -- a `RUN` of its
// own, in a build that exists precisely because nothing from an untrusted
// image should run. The characters below are the ones a reference
// legitimately needs (registry host and port, repository path, tag,
// digest); anything else is refused rather than escaped, since no real
// reference needs it.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

// ValidateRef refuses a reference that cannot be written into a generated
// Dockerfile safely.
func ValidateRef(ref string) error {
	if !refPattern.MatchString(ref) {
		return fmt.Errorf("%q is not a usable image reference", ref)
	}
	return nil
}

// portPattern is what an exposed port may look like, and it is here for
// the same reason refPattern is: a port read out of an image somebody
// else authored is written into a generated Dockerfile, so a "port"
// carrying a newline is a value that can append instructions to a build
// whose whole point is that nothing of that image runs.
//
// Ranges are included because docker accepts them in EXPOSE
// ("8080-8090/tcp") and an image is free to declare one.
var portPattern = regexp.MustCompile(`^([0-9]{1,5})(-[0-9]{1,5})?(/(tcp|udp|sctp))?$`)

// NormalizePort checks one port declaration and gives it docker's own
// spelling, which names the protocol even where the declaration did not
// -- so that what a command carries into an image reads the same however
// the image it came from wrote it.
func NormalizePort(port string) (string, error) {
	port = strings.TrimSpace(port)
	m := portPattern.FindStringSubmatch(port)
	if m == nil {
		return "", fmt.Errorf("%q is not a usable port declaration: expected a port, optionally a range, optionally with /tcp, /udp or /sctp", port)
	}
	var nums []int
	for _, n := range []string{m[1], strings.TrimPrefix(m[2], "-")} {
		if n == "" {
			continue
		}
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 || v > 65535 {
			return "", fmt.Errorf("%q is not a usable port declaration: %s is not a port number", port, n)
		}
		nums = append(nums, v)
	}
	if len(nums) == 2 && nums[0] > nums[1] {
		return "", fmt.Errorf("%q is not a usable port declaration: start port %d is greater than end port %d", port, nums[0], nums[1])
	}
	if m[3] == "" {
		return port + "/tcp", nil
	}
	return port, nil
}

// NormalizeVolumePath checks one guest volume mount point declaration,
// ensuring it is a valid, cleaned absolute path.
func NormalizeVolumePath(vol string) (string, error) {
	vol = strings.TrimSpace(vol)
	if vol == "" {
		return "", fmt.Errorf("volume mount point is empty")
	}
	if !strings.HasPrefix(vol, "/") {
		return "", fmt.Errorf("%q is not an absolute path: guest volume mount points must start with '/'", vol)
	}
	if strings.ContainsAny(vol, "\n\r\t\"'") {
		return "", fmt.Errorf("%q contains invalid characters for a volume path", vol)
	}
	cleaned := path.Clean(vol)
	if cleaned == "/" {
		return "", fmt.Errorf("volume mount point cannot be root '/'")
	}
	return cleaned, nil
}
