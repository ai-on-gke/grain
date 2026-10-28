package granulefile

import (
	"context"
	"strings"
	"testing"
)

func planOf(t *testing.T, src string) *BuildPlan {
	t.Helper()
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := PlanBuild(context.Background(), f, BuildOptions{Tag: "machine:test"})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}
	return plan
}

// A VM stage's RUNs are the guest phase and nothing else: they are not
// in the Dockerfile, because a docker build cannot reach /dev/kvm, and
// dropping them would be this file quietly building something else.
func TestARunInAVMStageBecomesTheGuestPhase(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
ENV WARM=yes
WORKDIR /srv
USER app
RUN ./warm.sh
`)
	if plan.Boot == nil {
		t.Fatal("the stage compiled to no guest phase, and it has a RUN in it")
	}
	if len(plan.Boot.Steps) != 1 {
		t.Fatalf("guest phase has %d steps, want the file's one RUN: %+v", len(plan.Boot.Steps), plan.Boot.Steps)
	}
	step := plan.Boot.Steps[0]
	if got, want := strings.Join(step.Argv, " "), "/bin/sh -c ./warm.sh"; got != want {
		t.Errorf("step argv = %q, want %q", got, want)
	}
	if step.User != "app" {
		t.Errorf("step user = %q, want the account USER named", step.User)
	}
	if step.Dir != "/srv" {
		t.Errorf("step dir = %q, want the directory WORKDIR set", step.Dir)
	}
	if got, want := strings.Join(step.Env, ","), "WARM=yes"; got != want {
		t.Errorf("step env = %q, want %q", got, want)
	}
	if strings.Contains(plan.Dockerfile, "warm.sh") {
		t.Errorf("the RUN reached the Dockerfile, where it would run on the build host:\n%s", plan.Dockerfile)
	}
	// WORKDIR is the one piece of that state the container stage also
	// needs: §6 has it create the directory, and it is what a relative
	// COPY destination below it resolves against.
	if !strings.Contains(plan.Dockerfile, "WORKDIR /srv") {
		t.Errorf("WORKDIR did not reach the guest root filesystem:\n%s", plan.Dockerfile)
	}
	// A VM stage's ENV is build-time only (§6), so it must not be
	// recorded on the machine.
	if strings.Contains(plan.Dockerfile, "WARM=yes") {
		t.Errorf("a VM stage's ENV was recorded on the machine:\n%s", plan.Dockerfile)
	}
}

// §6: an ARG in a VM stage is a build-time variable, in scope for the
// rest of the stage -- the same build-time state USER, WORKDIR and ENV
// form there. It needs no boot of its own, so a build that refused it as
// an unbuilt level 3 construct was telling the reader something wrong.
func TestAnArgInAVMStageIsBuildTimeState(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
ARG APP_VERSION=1.4.2
WORKDIR /srv/$APP_VERSION
COPY app /srv/$APP_VERSION/
RUN ./install.sh
`)
	if plan.Boot == nil || len(plan.Boot.Steps) != 1 {
		t.Fatalf("want the file's one RUN as the guest phase: %+v", plan.Boot)
	}
	step := plan.Boot.Steps[0]
	if got, want := strings.Join(step.Env, ","), "APP_VERSION=1.4.2"; got != want {
		t.Errorf("step env = %q, want %q: a RUN gets the stage's ARGs, as it does in a container build", got, want)
	}
	if step.Dir != "/srv/1.4.2" {
		t.Errorf("step dir = %q, want the WORKDIR expanded in the stage's own scope", step.Dir)
	}
	// The COPY beside it is docker's to expand, so the declaration has
	// to reach the container stage the copies go to as well.
	if !strings.Contains(plan.Dockerfile, "ARG APP_VERSION=1.4.2") {
		t.Errorf("the ARG did not reach the stage the copies go to:\n%s", plan.Dockerfile)
	}
}

// An ENV of the same name overrides an ARG wherever it is written, which
// is docker's rule for the pair (§6: as in a container stage).
func TestAnEnvOverridesAVMStageArgOfTheSameName(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
ENV MODE=release
ARG MODE=debug
RUN ./build.sh
`)
	if plan.Boot == nil || len(plan.Boot.Steps) != 1 {
		t.Fatalf("want the file's one RUN as the guest phase: %+v", plan.Boot)
	}
	if got, want := strings.Join(plan.Boot.Steps[0].Env, ","), "MODE=release"; got != want {
		t.Errorf("step env = %q, want %q", got, want)
	}
}

// A build argument the caller gave wins over the default the file wrote,
// wherever the declaration is (§3.5) -- and it has to win in granule's
// own expansion as well, since docker is applying the same --build-arg to
// the half of the stage it builds.
func TestABuildArgOverridesAVMStageArgDefault(t *testing.T) {
	f, err := Parse("Granulefile", `FROM rootfs:latest
VM PACK AS machine
ARG APP_VERSION=1.4.2
WORKDIR /srv/$APP_VERSION
RUN ./install.sh
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := PlanBuild(context.Background(), f, BuildOptions{
		Tag:       "machine:test",
		BuildArgs: []string{"APP_VERSION=2.0.0"},
	})
	if err != nil {
		t.Fatalf("PlanBuild: %v", err)
	}
	if plan.Boot == nil || len(plan.Boot.Steps) != 1 {
		t.Fatalf("want the file's one RUN as the guest phase: %+v", plan.Boot)
	}
	step := plan.Boot.Steps[0]
	if got, want := strings.Join(step.Env, ","), "APP_VERSION=2.0.0"; got != want {
		t.Errorf("step env = %q, want %q: the file's default shadowed the caller's build argument", got, want)
	}
	if step.Dir != "/srv/2.0.0" {
		t.Errorf("step dir = %q, want the directory the caller's build argument names", step.Dir)
	}
}

// `ARG NAME` with no default is how docker brings a global into a stage,
// and how this file says that one of the build's variables is used here.
func TestAVMStageArgWithNoDefaultTakesTheGlobal(t *testing.T) {
	plan := planOf(t, `ARG APP_VERSION=1.4.2
FROM rootfs:latest
VM PACK AS machine
ARG APP_VERSION
RUN ./install.sh
`)
	if plan.Boot == nil || len(plan.Boot.Steps) != 1 {
		t.Fatalf("want the file's one RUN as the guest phase: %+v", plan.Boot)
	}
	if got, want := strings.Join(plan.Boot.Steps[0].Env, ","), "APP_VERSION=1.4.2"; got != want {
		t.Errorf("step env = %q, want %q", got, want)
	}
}

// "In scope for the rest of the stage" is the whole of the scope: an ARG
// written in one VM stage is not in the next one's, and the build's own
// variables are not written over by either.
func TestAVMStageArgDoesNotReachTheNextStage(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS first
ARG STAMP=one

FROM rootfs:latest
VM PACK AS second
WORKDIR /srv/$STAMP
RUN ./install.sh
`)
	if plan.Boot == nil || len(plan.Boot.Steps) != 1 {
		t.Fatalf("want the file's one RUN as the guest phase: %+v", plan.Boot)
	}
	if got := plan.Boot.Steps[0].Dir; got != "/srv/" {
		t.Errorf("step dir = %q, want /srv/: the first stage's ARG leaked into the second's scope", got)
	}
	if got := strings.Join(plan.Boot.Steps[0].Env, ","); got != "" {
		t.Errorf("step env = %q, want none: the first stage's ARG leaked into the second's environment", got)
	}
}

// SHELL decides what a shell-form RUN is handed to inside the guest;
// an exec-form RUN is handed to nothing at all.
func TestShellDecidesWhatAShellFormRunIsHandedTo(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN one | two
RUN ["/usr/bin/three", "--flag"]
`)
	if plan.Boot == nil || len(plan.Boot.Steps) != 2 {
		t.Fatalf("want two steps, got %+v", plan.Boot)
	}
	if got, want := strings.Join(plan.Boot.Steps[0].Argv, " "), "/bin/bash -o pipefail -c one | two"; got != want {
		t.Errorf("shell-form step = %q, want %q", got, want)
	}
	if got, want := strings.Join(plan.Boot.Steps[1].Argv, " "), "/usr/bin/three --flag"; got != want {
		t.Errorf("exec-form step = %q, want %q", got, want)
	}
}

// §5.6: `VM SET snapshot=on` is VM SNAPSHOT written as a setting, and
// it is the build that carries it out. Recording the setting's own
// value as CHV_SNAPSHOT_PATH -- which is what a build did before the
// snapshot was built -- produces a machine that refuses to boot, that
// variable being a path.
func TestVMSetSnapshotIsTheSnapshotRatherThanAVariable(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
VM SET snapshot=on memory-mb=512
`)
	if plan.Boot == nil || !plan.Boot.Snapshot {
		t.Fatalf("snapshot=on compiled to no snapshot: %+v", plan.Boot)
	}
	if strings.Contains(plan.Dockerfile, "CHV_SNAPSHOT_PATH") {
		t.Errorf("the machine records CHV_SNAPSHOT_PATH, which is a path and would refuse to boot as \"on\":\n%s", plan.Dockerfile)
	}
	if !strings.Contains(plan.Dockerfile, `CHV_MEMORY_MB="512"`) {
		t.Errorf("the settings beside it were not recorded:\n%s", plan.Dockerfile)
	}
}

// A VOLUME in a VM stage is the machine's own mount point declaration
// (§6), which has to reach the image somebody runs.
func TestAVolumeInAVMStageReachesTheMachine(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
VOLUME /var/lib/data
`)
	if plan.Boot != nil {
		t.Errorf("a VOLUME asked for a guest phase, and nothing about it needs a boot: %+v", plan.Boot)
	}
	if !strings.Contains(plan.Dockerfile, "/var/lib/data") {
		t.Errorf("the mount point did not reach the machine:\n%s", plan.Dockerfile)
	}
}

// A boot produces an image rather than a stage the rest of a Dockerfile
// can refer to, so a stage needing one that is followed by others is
// refused by name rather than dropped (§10.3).
func TestARunInAStageThatIsNotTheTargetIsRefused(t *testing.T) {
	src := `FROM rootfs:latest
VM PACK AS first
RUN ./warm.sh

FROM alpine:3.20
COPY --from=first /etc/os-release /
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = PlanBuild(context.Background(), f, BuildOptions{Tag: "machine:test"})
	if err == nil {
		t.Fatal("the earlier stage's RUN was dropped rather than refused")
	}
	for _, want := range []string{"unimplemented", "RUN", "§10.3", "-target first"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

func TestTheTagABootBuildsUnderKeepsTheRegistryPort(t *testing.T) {
	for _, tc := range []struct{ tag, want string }{
		{"vm:test", "vm:granule-preboot"},
		{"ghcr.io/example/vm:latest", "ghcr.io/example/vm:granule-preboot"},
		{"localhost:5000/vm", "localhost:5000/vm:granule-preboot"},
		{"vm", "vm:granule-preboot"},
	} {
		if got := bootTag(tc.tag); got != tc.want {
			t.Errorf("bootTag(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// A restore replays the snapshot's own memory size rather than the
// image's, so the size the build boots at is the size the machine has
// forever. A `VM SET memory-mb=` next to a `VM SNAPSHOT` therefore has
// to reach the boot, or the build records a figure the machine could
// never honour.
func TestASnapshotBootsAtTheSizeTheFileAsksFor(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
VM SET memory-mb=2048 memory-max-mb=4096
VM SNAPSHOT
`)
	if plan.Boot == nil || !plan.Boot.Snapshot {
		t.Fatalf("VM SNAPSHOT compiled to no snapshot: %+v", plan.Boot)
	}
	if plan.Boot.MemoryMB != 2048 || plan.Boot.MemoryMaxMB != 4096 {
		t.Errorf("guest phase sizes the machine %d/%d MiB, want the file's 2048/4096",
			plan.Boot.MemoryMB, plan.Boot.MemoryMaxMB)
	}
	if !strings.Contains(plan.Dockerfile, `CHV_MEMORY_MB="2048"`) {
		t.Errorf("the machine does not record the setting either:\n%s", plan.Dockerfile)
	}
}

// A stage that sets no memory leaves the figure to whatever boots it,
// which for a snapshot build is guestbuild's own deliberately small
// default.
func TestAStageThatSetsNoMemoryAsksForNone(t *testing.T) {
	plan := planOf(t, `FROM rootfs:latest
VM PACK AS machine
VM SNAPSHOT
`)
	if plan.Boot == nil {
		t.Fatal("VM SNAPSHOT compiled to no guest phase")
	}
	if plan.Boot.MemoryMB != 0 || plan.Boot.MemoryMaxMB != 0 {
		t.Errorf("guest phase sizes the machine %d/%d MiB, and the file said nothing",
			plan.Boot.MemoryMB, plan.Boot.MemoryMaxMB)
	}
}
