package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const granulefileSrc = `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS redis_vm
RUN apt-get update && apt-get install -y redis-server
VM PACK
VM SET memory-mb=2048
`

// A bare `granule granulefile check` reads the Granule where it is
// run, says what the file would build, and builds none of it.
func TestGranuleCheck_Accepts(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Granule": granulefileSrc})

	code, out := runCLI(t, "granulefile", "check", dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"ok (granulefile/1.0-draft",
		"stage 0  redis_vm",
		"converted by VM PACK on line 3",
		"produces a granule VM image",
		"nothing was built",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

// A check says what each stage is and what it would build, which is the
// question an author has once the file is valid -- including for the two
// shapes that are easy to lose track of: a stage that carries a name for
// its container half and another for the machine made of it, and one
// that starts as a container stage from a machine's disk (§4.1, §4.2).
func TestGranuleCheck_SaysWhatEachStageIs(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Granule": `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs
RUN apt-get update && apt-get install -y redis-server
VM PACK AS redis_vm

VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked
COPY --from=rootfs /etc/redis/redis.conf /etc/redis.conf
VM PACK AS updated_vm
`})

	code, out := runCLI(t, "granulefile", "check", dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"rootfs, redis_vm",
		"container -> VM",
		"VM UNPACK FROM ghcr.io/example/my-vm:latest",
		"unpacked, updated_vm",
		"target: updated_vm, which produces a granule VM image",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}

	// And a -target naming the container half of such a stage produces a
	// container image, which is the other half of §4.1's rule.
	code, out = runCLI(t, "granulefile", "check", "-target", "rootfs", dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "target: rootfs, which produces an OCI container image") {
		t.Errorf("the container half of a converted stage does not build a container image:\n%s", out)
	}
}

// Granule first, then Dockerfile: the name selects nothing about what
// is built (§3.1), but a file called Granule is the one that was
// written for granule.
func TestGranuleCheck_ResolvesTheFile(t *testing.T) {
	both := writeFiles(t, map[string]string{
		"Granule":    granulefileSrc,
		"Dockerfile": "FROM alpine:latest\nCMD [\"/app\"]\nVM PACK\n",
	})
	if code, out := runCLI(t, "granulefile", "check", both); code != 0 {
		t.Fatalf("exit code = %d, want the Granule to be the one read:\n%s", code, out)
	}

	only := writeFiles(t, map[string]string{"Dockerfile": "FROM alpine:latest\nRUN apk add --no-cache redis\n"})
	if code, out := runCLI(t, "granulefile", "check", only); code != 0 {
		t.Fatalf("exit code = %d, want the Dockerfile to be read where there is no Granule:\n%s", code, out)
	}

	empty := t.TempDir()
	code, out := runCLI(t, "granulefile", "check", empty)
	if code == 0 || !strings.Contains(out, "no Granule and no Dockerfile") {
		t.Errorf("exit code = %d, want a complaint naming both files:\n%s", code, out)
	}
}

// Every problem in the file, each naming the file, the line and the
// instruction, and nothing exiting zero.
func TestGranuleCheck_ReportsEveryProblem(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"Granule": "FROM alpine:latest\nVM PACK\nHEALTHCHECK CMD /app --check\nEXPOSE 80\n",
	})
	file := filepath.Join(dir, "Granule")

	code, out := runCLI(t, "granulefile", "check", "-f", file)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{file + ":3: HEALTHCHECK", file + ":4: EXPOSE", "(K9, §6)", "docs/granulefile.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

// "-f -" reads the file from standard input, which is what an editor or
// a pre-commit hook has rather than a path.
func TestGranuleCheck_ReadsStandardInput(t *testing.T) {
	code, out := runCLIWithStdin(t, granulefileSrc, "granulefile", "check", "-f", "-")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "<stdin>: ok") {
		t.Errorf("output does not name the input:\n%s", out)
	}
}

// -level says which conformance level to answer as, and a construct
// above it is unimplemented at that level rather than forbidden (§10.3).
// The default is the whole language, because a check builds nothing.
func TestGranuleCheck_Level(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"Granule": "VM FROM ghcr.io/example/vm:1\nRUN systemctl enable nginx\n",
	})

	if code, out := runCLI(t, "granulefile", "check", dir); code != 0 {
		t.Fatalf("exit code = %d, want 0 at the default level:\n%s", code, out)
	}

	code, out := runCLI(t, "granulefile", "check", "-level", "2", dir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 at level 2:\n%s", code, out)
	}
	for _, want := range []string{"unimplemented", "level 3 (VM stages)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}

	if code, out := runCLI(t, "granulefile", "check", "-level", "9", dir); code == 0 || !strings.Contains(out, "the levels are 1") {
		t.Errorf("exit code = %d, want a complaint about the level:\n%s", code, out)
	}
}

// A -target the file does not have is refused rather than silently
// building the last stage.
func TestGranuleCheck_Target(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Granule": granulefileSrc})

	if code, out := runCLI(t, "granulefile", "check", "-target", "redis_vm", dir); code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	code, out := runCLI(t, "granulefile", "check", "-target", "nowhere", dir)
	if code == 0 || !strings.Contains(out, `no stage called "nowhere"`) {
		t.Errorf("exit code = %d, want a complaint naming the target:\n%s", code, out)
	}
}

func TestGranule_UnknownSubcommand(t *testing.T) {
	if code, out := runCLI(t, "granulefile", "unknown"); code == 0 || !strings.Contains(out, `unknown subcommand "unknown"`) {
		t.Errorf("exit code = %d, want a complaint naming the subcommand:\n%s", code, out)
	}
	if code, out := runCLI(t, "granulefile"); code == 0 || !strings.Contains(out, "expected a subcommand") {
		t.Errorf("exit code = %d, want a complaint about the missing subcommand:\n%s", code, out)
	}
}

func TestGranuleBuild_Print(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Granule": `FROM alpine:3.20 AS app
ENTRYPOINT ["/bin/echo"]
CMD ["hello"]
VM WRAP AS app_vm
`})

	code, out := runCLI(t, "granulefile", "build", "-print", dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"FROM alpine:3.20 AS app",
		"COPY --from=app / /var/lib/granule/container/rootfs",
		"granule-wrap@app_vm.service",
		`LABEL dev.granule.granulefile.kind="wrap"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

func TestGranuleBuild_Print_Pack(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Granule": `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs
RUN apt-get update && apt-get install -y redis-server
VM PACK --variant=debian AS redis_vm
VM SET memory-mb=2048 cpus=2
`})

	code, out := runCLI(t, "granulefile", "build", "-print", dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs",
		"COPY --from=rootfs / /rootfs",
		`dev.granule.granulefile.kind="pack"`,
		`dev.granule.granulefile.stage="redis_vm"`,
		`dev.granule.guest.packed-from="ghcr.io/gke-labs/granule-guest-rootfs:debian12"`,
		`ENV CHV_MEMORY_MB="2048"`,
		`ENV CHV_CPUS="2"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

// TestGranulefile_AllMainUseCases tests all 9 canonical Granulefile use cases (§14.1-§14.9)
func TestGranulefile_AllMainUseCases(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantText string
	}{
		{
			name: "§14.1 A Dockerfile is a Granule",
			content: `FROM alpine:3.20
RUN apk add --no-cache curl
CMD ["curl", "https://example.com"]
`,
			wantText: "produces an OCI container image",
		},
		{
			name: "§14.2 Wrapping a container image in a machine",
			content: `FROM node:20-alpine AS app
WORKDIR /srv/app
COPY . .
ENTRYPOINT ["node", "server.js"]
VM WRAP
`,
			wantText: "converted by VM WRAP",
		},
		{
			name: "§14.3 Packing a rootfs into a machine",
			content: `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs
RUN apt-get update && apt-get install -y nginx
VM PACK
VM SET cpus=4 memory-mb=4096
`,
			wantText: "converted by VM PACK",
		},
		{
			name: "§14.4 Deriving a machine from a machine",
			content: `VM FROM ghcr.io/example/my-vm:1.0 AS derived
RUN apt-get update && apt-get install -y postgresql
VM SET disk-size-mb=20480
`,
			wantText: "VM FROM ghcr.io/example/my-vm:1.0",
		},
		{
			name: "§14.5 Updating a VM image offline without KVM",
			content: `VM UNPACK FROM ghcr.io/example/my-vm:1.0 AS rootfs
RUN echo "offline-update" > /etc/version
VM PACK
`,
			wantText: "VM UNPACK FROM ghcr.io/example/my-vm:1.0",
		},
		{
			name: "§14.6 Packing directly from an earlier stage",
			content: `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs_builder
RUN apt-get update && apt-get install -y redis-server
VM PACK FROM rootfs_builder AS redis_vm
USER redis
RUN redis-server --test-config
USER root
RUN systemctl enable redis-server
VOLUME /var/lib/redis
VM SET memory-mb=2048 cpus=2
`,
			wantText: "VM PACK FROM rootfs_builder",
		},
		{
			name: "§14.7 Composing machines",
			content: `VM FROM ghcr.io/example/base-vm:latest AS base
RUN /usr/local/bin/generate-assets
FROM nginx:alpine AS frontend
COPY --from=base /var/www /usr/share/nginx/html
VM WRAP
`,
			wantText: "produces a granule VM image",
		},
		{
			name: "§14.8 Snapshotting a warmed VM for instant resume",
			content: `VM FROM ghcr.io/example/python-ml-vm:latest AS warmed_vm
RUN python3 -c "import model; model.preload_weights()"
VM SNAPSHOT
VM SET memory-mb=4096
`,
			wantText: "warmed_vm",
		},
		{
			name: "§14.9 Native entrypoint and command in a packed stage",
			content: `FROM ghcr.io/gke-labs/granule-guest-rootfs:alpine AS rootfs
RUN apk add --no-cache myapp
ENTRYPOINT ["/usr/bin/myapp"]
CMD ["--config", "/etc/myapp.conf"]
VM PACK
`,
			wantText: "converted by VM PACK",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"Granule": tc.content})
			code, out := runCLI(t, "granulefile", "check", dir)
			if code != 0 {
				t.Fatalf("check exit code = %d, want 0:\n%s", code, out)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output does not contain %q:\n%s", tc.wantText, out)
			}
		})
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runCLIWithStdin(t *testing.T, stdin string, args ...string) (code int, out string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = Run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}
