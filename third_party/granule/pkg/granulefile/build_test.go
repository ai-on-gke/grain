package granulefile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/guestpack"
)

func TestPlanBuild_InStageVMWrap(t *testing.T) {
	src := `FROM alpine:3.20 AS workload
ENV FOO=bar BAZ="hello world"
WORKDIR /srv
USER 1000:1000
ENTRYPOINT ["/entry.sh"]
CMD ["serve", "--port", "8080"]
EXPOSE 8080/tcp 53/udp
VM WRAP --restart=on-failure --exit-action=poweroff redis AS redis_vm
VM SET memory-mb=2048 cpus=2 disk-size-mb=10240 extent-size-mb=128
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "redis-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "redis_vm" {
		t.Errorf("Target.Label = %q, want redis_vm", got)
	}

	// Verify Spec
	spec := plan.Spec
	if spec.Rootfs != containerspec.RootfsDir {
		t.Errorf("Spec.Rootfs = %q, want %q", spec.Rootfs, containerspec.RootfsDir)
	}
	if got, want := strings.Join(spec.Entrypoint, " "), "/entry.sh"; got != want {
		t.Errorf("Spec.Entrypoint = %q, want %q", got, want)
	}
	if got, want := strings.Join(spec.Cmd, " "), "serve --port 8080"; got != want {
		t.Errorf("Spec.Cmd = %q, want %q", got, want)
	}
	if got, want := spec.WorkingDir, "/srv"; got != want {
		t.Errorf("Spec.WorkingDir = %q, want %q", got, want)
	}
	if got, want := spec.User, "1000:1000"; got != want {
		t.Errorf("Spec.User = %q, want %q", got, want)
	}
	if !spec.HaltOnExit {
		t.Errorf("Spec.HaltOnExit = false, want true for --exit-action=poweroff")
	}

	// Verify generated Dockerfile
	df := plan.Dockerfile
	if !strings.Contains(df, "FROM alpine:3.20 AS workload") {
		t.Errorf("Dockerfile missing container stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=workload / "+containerspec.RootfsDir) {
		t.Errorf("Dockerfile missing rootfs COPY:\n%s", df)
	}
	if !strings.Contains(df, "granule-wrap@redis.service") {
		t.Errorf("Dockerfile missing granule-wrap service symlink for service name redis:\n%s", df)
	}
	// And only that one: granule-container runs the same container.json,
	// so a guest that started both would run the workload twice.
	if strings.Contains(df, "ln -sf /etc/systemd/system/granule-container.service") {
		t.Errorf("Dockerfile enables granule-container.service beside the wrap's own service, so the guest would run the workload twice:\n%s", df)
	}
	if strings.Contains(df, "ln -sf /etc/init.d/granule-container") {
		t.Errorf("Dockerfile enables the granule-container OpenRC service beside the wrap's own, so the guest would run the workload twice:\n%s", df)
	}
	// Nor is a granule-container unit written at all: the pack stages
	// below lay granule's guest overlay down unconditionally and that
	// overlay owns both files, so a copy from here never reaches the
	// packed disk.
	if strings.Contains(df, "/etc/systemd/system/granule-container.service") ||
		strings.Contains(df, "granule-container_") {
		t.Errorf("Dockerfile writes a granule-container unit the pack stages below overwrite:\n%s", df)
	}
	// The rm -f that disable a granule-container the guest image arrived
	// with stay: who enabled it is a different question from who
	// supplies it.
	if !strings.Contains(df, "rm -f /etc/systemd/system/multi-user.target.wants/granule-container.service") ||
		!strings.Contains(df, "rm -f /etc/runlevels/default/granule-container") {
		t.Errorf("Dockerfile no longer disables a granule-container the guest image arrived with:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.wrapped-image="workload"`) {
		t.Errorf("Dockerfile missing wrapped-image label:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.granulefile.kind="wrap"`) {
		t.Errorf("Dockerfile missing kind=wrap label:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.granulefile.stage="redis_vm"`) {
		t.Errorf("Dockerfile missing stage label:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_MEMORY_MB="2048"`) {
		t.Errorf("Dockerfile missing CHV_MEMORY_MB env:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_CPUS="2"`) {
		t.Errorf("Dockerfile missing CHV_CPUS env:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_EXTENT_SIZE_MB="128"`) {
		t.Errorf("Dockerfile missing CHV_EXTENT_SIZE_MB env:\n%s", df)
	}
	if !strings.Contains(df, "EXPOSE 53/udp 8080/tcp") {
		t.Errorf("Dockerfile missing EXPOSE ports:\n%s", df)
	}

	// Verify context files
	specFileContent, ok := plan.Files["container_0.json"]
	if !ok {
		t.Fatalf("missing container_0.json in context files: %v", plan.Files)
	}
	var unmarshaledSpec containerspec.Spec
	if err := json.Unmarshal(specFileContent, &unmarshaledSpec); err != nil {
		t.Fatalf("unmarshaling generated container_0.json: %v", err)
	}
	if unmarshaledSpec.WorkingDir != "/srv" {
		t.Errorf("unmarshaled spec WorkingDir = %q, want /srv", unmarshaledSpec.WorkingDir)
	}

	unitContent, ok := plan.Files["granule-wrap_0@.service"]
	if !ok {
		t.Fatalf("missing granule-wrap_0@.service in context files")
	}
	if !strings.Contains(string(unitContent), "Restart=on-failure") {
		t.Errorf("unit file missing Restart=on-failure:\n%s", string(unitContent))
	}
}

func TestPlanBuild_VMWrapFromEarlierStage(t *testing.T) {
	src := `FROM golang:1.22 AS builder
WORKDIR /src
RUN echo "building"

FROM alpine:3.20 AS runner
WORKDIR /app
COPY --from=builder /src /app
ENTRYPOINT ["/app/server"]

VM WRAP FROM runner --name=myservice AS server_vm
VM SET cpus=4
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "server-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "server_vm" {
		t.Errorf("Target.Label = %q, want server_vm", got)
	}

	// Verify Spec inherited from runner stage
	spec := plan.Spec
	if got, want := strings.Join(spec.Entrypoint, " "), "/app/server"; got != want {
		t.Errorf("Spec.Entrypoint = %q, want %q", got, want)
	}
	if got, want := spec.WorkingDir, "/app"; got != want {
		t.Errorf("Spec.WorkingDir = %q, want %q", got, want)
	}
	// Default exit-action is ignore -> HaltOnExit = false
	if spec.HaltOnExit {
		t.Errorf("Spec.HaltOnExit = true, want false by default (ignore)")
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "COPY --from=runner / "+containerspec.RootfsDir) {
		t.Errorf("Dockerfile missing COPY --from=runner:\n%s", df)
	}
	if !strings.Contains(df, "granule-wrap@myservice.service") {
		t.Errorf("Dockerfile missing granule-wrap@myservice.service symlink:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.wrapped-image="runner"`) {
		t.Errorf("Dockerfile missing wrapped-image label:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_CPUS="4"`) {
		t.Errorf("Dockerfile missing CHV_CPUS env:\n%s", df)
	}

	// Default restart policy for VM WRAP is always
	unitContent := string(plan.Files["granule-wrap_2@.service"])
	if !strings.Contains(unitContent, "Restart=always") {
		t.Errorf("unit file missing Restart=always:\n%s", unitContent)
	}
}

func TestPlanBuild_TargetContainerStage(t *testing.T) {
	src := `FROM golang:1.22 AS builder
WORKDIR /src
RUN echo "building"

FROM alpine:3.20 AS runner
WORKDIR /app
COPY --from=builder /src /app
ENTRYPOINT ["/app/server"]

VM WRAP FROM runner AS server_vm
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Target: "runner",
		Tag:    "runner:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != Container {
		t.Errorf("TargetKind = %v, want Container", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "runner" {
		t.Errorf("Target.Label = %q, want runner", got)
	}

	df := plan.Dockerfile
	if strings.Contains(df, "granule-wrap") {
		t.Errorf("Dockerfile should not contain wrap stages when target is container stage:\n%s", df)
	}
}

func TestPlanBuild_VMUnpackContainerTarget(t *testing.T) {
	src := `VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked
RUN rm -f /etc/machine-id
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "unpacked-container:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != Container {
		t.Errorf("TargetKind = %v, want Container", plan.TargetKind)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "FROM "+guestpack.DefaultPackerImage+" AS granule_unpack_0") {
		t.Errorf("Dockerfile missing unpack stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=ghcr.io/example/my-vm:latest /var/lib/granule/guest/disk.img /disk.img") {
		t.Errorf("Dockerfile missing disk.img copy:\n%s", df)
	}
	if !strings.Contains(df, "debugfs -R 'rdump / /rootfs' /disk.img") {
		t.Errorf("Dockerfile missing debugfs rdump command:\n%s", df)
	}
	if !strings.Contains(df, "FROM scratch AS unpacked") {
		t.Errorf("Dockerfile missing scratch container stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=granule_unpack_0 /rootfs/ /") {
		t.Errorf("Dockerfile missing copy from unpack stage:\n%s", df)
	}
	if !strings.Contains(df, "RUN rm -f /etc/machine-id") {
		t.Errorf("Dockerfile missing RUN instruction:\n%s", df)
	}
}

func TestPlanBuild_VMUnpackAndVMPack(t *testing.T) {
	src := `VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked
COPY updated-config.yaml /etc/service/config.yaml
VM PACK --variant=debian --disk-extra-mb=100 AS updated_vm
VM SET memory-mb=4096 cpus=4
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag:         "updated-vm:latest",
		KonturImage: "ghcr.io/gke-labs/granule-base-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "updated_vm" {
		t.Errorf("Target.Label = %q, want updated_vm", got)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "FROM "+guestpack.DefaultPackerImage+" AS granule_unpack_0") {
		t.Errorf("Dockerfile missing unpack stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=ghcr.io/example/my-vm:latest /var/lib/granule/guest/disk.img /disk.img") {
		t.Errorf("Dockerfile missing disk.img copy:\n%s", df)
	}
	if !strings.Contains(df, "FROM scratch AS unpacked") {
		t.Errorf("Dockerfile missing scratch stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=granule_unpack_0 /rootfs/ /") {
		t.Errorf("Dockerfile missing rootfs copy:\n%s", df)
	}
	if !strings.Contains(df, "COPY updated-config.yaml /etc/service/config.yaml") {
		t.Errorf("Dockerfile missing copy config:\n%s", df)
	}
	if !strings.Contains(df, "FROM "+guestpack.DefaultPackerImage+" AS granule_pack_0") {
		t.Errorf("Dockerfile missing pack stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=unpacked / /rootfs") {
		t.Errorf("Dockerfile missing copy from unpacked container stage to /rootfs in pack stage:\n%s", df)
	}
	if !strings.Contains(df, "FROM ghcr.io/gke-labs/granule-base-vm:latest AS updated_vm") {
		t.Errorf("Dockerfile missing final VM stage header:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.kind="pack"`) {
		t.Errorf("Dockerfile missing kind=pack label:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.stage="updated_vm"`) {
		t.Errorf("Dockerfile missing stage label:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_MEMORY_MB="4096"`) {
		t.Errorf("Dockerfile missing CHV_MEMORY_MB env:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_CPUS="4"`) {
		t.Errorf("Dockerfile missing CHV_CPUS env:\n%s", df)
	}
}

func TestPlanBuild_VMPackFromEarlierStage(t *testing.T) {
	src := `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs_builder
RUN apt-get update && apt-get install -y redis-server
EXPOSE 6379/tcp

VM PACK FROM rootfs_builder AS redis_vm
VM SET memory-mb=2048 cpus=2
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag:         "redis-vm:latest",
		KonturImage: "ghcr.io/gke-labs/granule-base-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "redis_vm" {
		t.Errorf("Target.Label = %q, want redis_vm", got)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs_builder") {
		t.Errorf("Dockerfile missing rootfs_builder stage:\n%s", df)
	}
	if !strings.Contains(df, "FROM "+guestpack.DefaultPackerImage+" AS granule_pack_1") {
		t.Errorf("Dockerfile missing pack stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=rootfs_builder / /rootfs") {
		t.Errorf("Dockerfile missing copy from rootfs_builder:\n%s", df)
	}
	if !strings.Contains(df, "FROM ghcr.io/gke-labs/granule-base-vm:latest AS redis_vm") {
		t.Errorf("Dockerfile missing final VM stage header:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.granulefile.kind="pack"`) {
		t.Errorf("Dockerfile missing kind=pack label:\n%s", df)
	}
}

func TestPlanBuild_VMUnpackAndVMWrap(t *testing.T) {
	src := `VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked
COPY new-binary /usr/local/bin/my-binary
ENTRYPOINT ["/usr/local/bin/my-binary"]
VM WRAP AS wrapped_vm
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "wrapped-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "FROM "+guestpack.DefaultPackerImage+" AS granule_unpack_0") {
		t.Errorf("Dockerfile missing unpack stage:\n%s", df)
	}
	if !strings.Contains(df, "FROM scratch AS unpacked") {
		t.Errorf("Dockerfile missing scratch container stage:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=unpacked / "+containerspec.RootfsDir) {
		t.Errorf("Dockerfile missing copy from unpacked to guest rootfs:\n%s", df)
	}
	if !strings.Contains(df, `LABEL dev.granule.granulefile.kind="wrap"`) {
		t.Errorf("Dockerfile missing kind=wrap label:\n%s", df)
	}
}

func TestPlanBuild_InStageVMPack(t *testing.T) {
	src := `FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs
RUN apt-get update && apt-get install -y redis-server
VM PACK --variant=debian --disk-extra-mb=64 AS redis_vm
VM SET memory-mb=4096 cpus=4 disk-size-mb=10240
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "redis-pack-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "redis_vm" {
		t.Errorf("Target.Label = %q, want redis_vm", got)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs") {
		t.Errorf("Dockerfile missing container stage header:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=rootfs / /rootfs") {
		t.Errorf("Dockerfile missing pack COPY from rootfs:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.kind="pack"`) {
		t.Errorf("Dockerfile missing kind=pack label:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.stage="redis_vm"`) {
		t.Errorf("Dockerfile missing stage label:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.version="granulefile/1.0-draft"`) {
		t.Errorf("Dockerfile missing version label:\n%s", df)
	}
	if strings.Contains(df, "dev.granule.wrapped-image") {
		t.Errorf("Dockerfile should not contain wrapped-image label for VM PACK:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_MEMORY_MB="4096"`) {
		t.Errorf("Dockerfile missing CHV_MEMORY_MB env:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_CPUS="4"`) {
		t.Errorf("Dockerfile missing CHV_CPUS env:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.guest.packed-from="ghcr.io/gke-labs/granule-guest-rootfs:debian12"`) {
		t.Errorf("Dockerfile missing packed-from label:\n%s", df)
	}
}

func TestPlanBuild_VMPackFromExternalImage(t *testing.T) {
	src := `VM PACK FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 --variant=debian AS debian_vm
VM SET cpus=4
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag: "debian-vm:latest",
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}

	if plan.TargetKind != VM {
		t.Errorf("TargetKind = %v, want VM", plan.TargetKind)
	}
	if got := plan.Target.Label(); got != "debian_vm" {
		t.Errorf("Target.Label = %q, want debian_vm", got)
	}

	df := plan.Dockerfile
	if !strings.Contains(df, "COPY --from=ghcr.io/gke-labs/granule-guest-rootfs:debian12 / /rootfs") {
		t.Errorf("Dockerfile missing COPY --from=ghcr.io/...:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.kind="pack"`) {
		t.Errorf("Dockerfile missing kind=pack label:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.granulefile.stage="debian_vm"`) {
		t.Errorf("Dockerfile missing stage label:\n%s", df)
	}
	if !strings.Contains(df, `dev.granule.guest.packed-from="ghcr.io/gke-labs/granule-guest-rootfs:debian12"`) {
		t.Errorf("Dockerfile missing packed-from label:\n%s", df)
	}
	if !strings.Contains(df, `ENV CHV_CPUS="4"`) {
		t.Errorf("Dockerfile missing CHV_CPUS env:\n%s", df)
	}
}

// A quoted ENV value is one variable, not one word of one. It reached
// the workload's environment cut off at the first space until the
// lexer's own field splitting was used to read it.
func TestPlanBuild_QuotedEnvValueSurvives(t *testing.T) {
	src := `FROM alpine:3.20
ENV GREETING="hello from the wrapped workload" HOME=/srv
CMD ["/bin/sh", "-c", "sleep 3600"]
VM WRAP AS quoted_vm
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := PlanBuild(context.Background(), f, BuildOptions{Tag: "quoted:test"})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}
	got, ok := plan.Spec.LookupEnv("GREETING")
	if !ok {
		t.Fatalf("the workload records no GREETING: %v", plan.Spec.Env)
	}
	if want := "hello from the wrapped workload"; got != want {
		t.Errorf("GREETING = %q, want %q", got, want)
	}
	if got, _ := plan.Spec.LookupEnv("HOME"); got != "/srv" {
		t.Errorf("HOME = %q, want /srv", got)
	}
}

func TestPlanBuild_VMSetExtentSizeOverride(t *testing.T) {
	src := `ARG ROOTFS=ghcr.io/gke-labs/granule-guest-rootfs:debian12
FROM ${ROOTFS}
VM PACK AS override_vm
VM SET extent-size-mb=64
VM SET extent-size-mb=128
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := PlanBuild(context.Background(), f, BuildOptions{Tag: "override:test"})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}
	if !strings.Contains(plan.Dockerfile, `ENV CHV_EXTENT_SIZE_MB="128"`) {
		t.Errorf("Dockerfile missing overridden CHV_EXTENT_SIZE_MB env:\n%s", plan.Dockerfile)
	}
	if strings.Contains(plan.Dockerfile, `ENV CHV_EXTENT_SIZE_MB="64"`) {
		t.Errorf("Dockerfile still contains stale CHV_EXTENT_SIZE_MB=64 env:\n%s", plan.Dockerfile)
	}
}
