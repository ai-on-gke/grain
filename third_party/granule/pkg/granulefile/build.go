package granulefile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/guestwrap"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// BuildOptions configures a Granulefile build.
type BuildOptions struct {
	// File is the path to the Granulefile.
	File string

	// Context is the build context directory. Defaults to ".".
	Context string

	// Tag is the image reference to give the build result (required for Build).
	Tag string

	// Target is the stage to build. Defaults to the last stage.
	Target string

	// GuestImage is the granule guest rootfs image. Defaults to guestwrap.DefaultGuestImage.
	GuestImage string

	// KonturImage is the granule base VM image. Defaults to vmspec.DockerImage.
	KonturImage string

	// PackerImage is the image used for guest disk packing. Defaults to guestpack.DefaultPackerImage.
	PackerImage string

	// Variant is the guest overlay variant ("debian", "alpine", or "auto").
	Variant string

	// DiskExtraMB is additional headroom in MiB for the guest disk.
	DiskExtraMB int

	// BuildArgs are the build's variables, as KEY=VALUE. They override
	// the file's global ARG defaults, both in the expansion granule does
	// for itself (§3.5) and in the build it runs, and are passed on to
	// docker as --build-arg.
	BuildArgs []string

	// FilesDir is the directory, relative to Context, that the support
	// files granule generates are written into and copied from. Empty
	// puts them at the root of the context, which is what a caller that
	// only wants to read the generated build wants; Build sets it to a
	// temporary directory it makes inside the context, so that a
	// generated file cannot collide with one of the author's.
	FilesDir string

	// ExtraBuildArgs are passed directly to docker build.
	ExtraBuildArgs []string

	// DockerBinary is the docker executable name/path. Defaults to
	// dockercli.Binary() ($GRANULE_DOCKER, else "docker").
	DockerBinary string

	// PrintDockerfile causes Build to print the generated Dockerfile without building.
	PrintDockerfile bool

	Stdout io.Writer
	Stderr io.Writer
}

// BuildPlan contains the planned build steps and generated artifacts.
type BuildPlan struct {
	File       *File
	Target     *Stage
	TargetKind Kind
	Dockerfile string
	Files      map[string][]byte
	Spec       containerspec.Spec
	Ports      []string

	// Boot is the guest phase the target stage ends in, or nil when the
	// whole of it is a docker build. When it is set, the Dockerfile
	// above builds a machine that has not yet had the stage's `RUN`s
	// run in it, and Build boots that machine, runs them and commits
	// the result as the output tag -- which is why a VM stage with a
	// `RUN` in it cannot be one Dockerfile handed to docker.
	Boot *BootPhase
}

type containerConfig struct {
	Entrypoint   []string
	Cmd          []string
	Env          []string
	WorkingDir   string
	User         string
	StopSignal   string
	ExposedPorts []string
	Volumes      []string
}

type wrapStageOptions struct {
	name       string
	restart    string
	exitAction string
	runtime    string
}

// PlanBuild validates the file and produces the generated multi-stage Dockerfile
// and support files needed to build the target stage.
func PlanBuild(ctx context.Context, f *File, opts BuildOptions) (*BuildPlan, error) {
	if err := Validate(f, Options{Level: Level3, Target: opts.Target}); err != nil {
		return nil, err
	}

	targetStage, targetKind, ok := f.Target(opts.Target)
	if !ok {
		if opts.Target != "" {
			return nil, fmt.Errorf("no stage called %q in %s", opts.Target, f.Path)
		}
		if len(f.Stages) == 0 {
			return nil, fmt.Errorf("%s has no stages", f.Path)
		}
		targetStage = f.Stages[len(f.Stages)-1]
		targetKind = targetStage.Kind()
	}

	guestImage := opts.GuestImage
	if guestImage == "" {
		guestImage = guestwrap.DefaultGuestImage
	}
	konturImage := opts.KonturImage
	if konturImage == "" {
		konturImage = vmspec.DockerImage
	}
	packerImage := opts.PackerImage
	if packerImage == "" {
		packerImage = guestpack.DefaultPackerImage
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(`# Generated from %s by Granulefile builder (%s)`, f.Path, Version)
	w(``)

	for _, g := range f.Globals {
		w("%s", formatInstruction(g))
	}
	if len(f.Globals) > 0 {
		w(``)
	}

	// The scope granule expands its own reads of this file in (§3.5),
	// and the names the caller's build arguments supplied, which a
	// stage's own ARG must not write over.
	vars, overriddenArgs := buildVars(f, opts.BuildArgs)

	contextFiles := make(map[string][]byte)
	// Where a generated support file is, as the build has to name it:
	// Build stages them in a directory of its own inside the context so
	// that they cannot collide with the author's own files.
	files := func(name string) string {
		if opts.FilesDir == "" {
			return name
		}
		return path.Join(opts.FilesDir, name)
	}
	var finalSpec containerspec.Spec
	var finalPorts []string
	// The guest phase the stage being built ends in, if it has one: the
	// Dockerfile above stops at a packed machine, and this is what has
	// to happen inside it before the build has produced what the file
	// describes.
	var boot *BootPhase

	// Track stages up to target stage.
	stageConfigs := make(map[string]containerConfig)

	for i := 0; i <= targetStage.Index; i++ {
		stage := f.Stages[i]
		isTarget := (i == targetStage.Index)

		if stage.Kind() == Container {
			if stage.Header.IsVM() && stage.Header.Verb == "UNPACK" {
				w("%s", generateUnpackStage(packerImage, expand(stage.Ref, vars), stage.Index))
				stageName := stage.Name
				if stageName == "" {
					stageName = fmt.Sprintf("stage_%d", stage.Index)
				}
				w("FROM scratch AS %s", stageName)
				w("COPY --from=granule_unpack_%d /rootfs/ /", stage.Index)
			} else {
				w("%s", formatInstruction(stage.Header))
			}
			for _, in := range stage.Instructions {
				w("%s", formatInstruction(in))
			}
			w(``)

			cfg := extractContainerConfigFromInstructions(stage.Instructions)
			for _, n := range stage.Names() {
				stageConfigs[n.Name] = cfg
			}
			if stage.Name == "" {
				stageConfigs[fmt.Sprintf("%d", stage.Index)] = cfg
			}
			if isTarget {
				finalPorts = cfg.ExposedPorts
			}
			continue
		}

		// Stage is a VM stage: VM PACK, VM PACK FROM, VM WRAP, or VM WRAP FROM
		tag := opts.Tag
		if tag == "" {
			tag = "unused"
		}

		if stage.Header.IsVM() && stage.Header.Verb == "PACK" {
			// VM PACK FROM <ref>
			ref, packOpts := parsePackHeader(stage.Header)
			ref = expand(ref, vars)
			vmStageName := stage.Name
			var cfg containerConfig
			if prevCfg, ok := stageConfigs[ref]; ok {
				cfg = prevCfg
			} else if opts.DockerBinary != "" {
				if inspected, err := inspectImageConfig(ctx, opts.DockerBinary, ref); err == nil {
					cfg = inspected
				}
			}

			parts, err := planVMStage(f, stage, stage.Instructions, vars, overriddenArgs, isTarget)
			if err != nil {
				return nil, err
			}

			// A COPY in this stage writes into the guest root filesystem
			// (§6), which here is the image being packed: a stage of our
			// own on top of it is where the copies land, before the pack.
			packFrom := ref
			if len(parts.offline) > 0 {
				packFrom = fmt.Sprintf("granule_vmstage_%d", stage.Index)
				w("FROM %s AS %s", ref, packFrom)
				for _, in := range parts.offline {
					w("%s", formatInstruction(in))
				}
				w(``)
			}

			variant := packOpts.variant
			if variant == "" {
				variant = opts.Variant
			}

			packedFrom := expand(resolvePackedFrom(f, stage.Index, ref), vars)
			packOptsGuest := guestpack.Options{
				From:         packFrom,
				PackedFrom:   packedFrom,
				Tag:          tag,
				KonturImage:  konturImage,
				PackerImage:  packerImage,
				Variant:      variant,
				DiskExtraMB:  opts.DiskExtraMB + parts.extraDiskMB + packOpts.diskExtraMB,
				ExposedPorts: cfg.ExposedPorts,
				Volumes:      append(slices.Clone(cfg.Volumes), parts.volumes...),
			}
			spec, specFile, specJSON, err := packWorkload(cfg, packedFrom, stage.Index)
			if err != nil {
				return nil, err
			}
			if spec != nil {
				contextFiles[specFile] = specJSON
				packOptsGuest.Spec = spec
				packOptsGuest.SpecFile = files(specFile)
			}
			packStages, err := guestpack.PackStages(packOptsGuest)
			if err != nil {
				return nil, err
			}

			packStages = renamePackStage(packStages, packerImage, stage.Index)
			packStages = renameFinalStage(packStages, konturImage, vmStageName)

			w("%s", strings.TrimSpace(packStages))
			w(`LABEL dev.granule.granulefile.kind="pack" \`)
			w(`      dev.granule.granulefile.stage="%s" \`, stage.Label())
			w(`      dev.granule.granulefile.version="%s"`, Version)
			for _, in := range parts.labels {
				w("%s", formatInstruction(in))
			}

			writeSettings(w, parts.settings)
			w(``)

			if isTarget {
				finalPorts = cfg.ExposedPorts
				boot = withBase(parts.boot, packedFrom)
			}
			continue
		}

		if stage.Header.IsVM() && stage.Header.Verb == "FROM" {
			// VM FROM <ref>: inherit the machine, and take this build's
			// own VMM half (§5.1).
			//
			// Built as an unpack and a repack: the guest root filesystem
			// comes out of the referenced machine's disk, the stage's
			// offline instructions are applied to it as container
			// instructions, and it is packed again onto the granule
			// image this build is running out of -- which is what "the
			// VMM half of the result is the implementation's own"
			// means, and what `granule guest rebase -rootfs` does to an
			// image somebody else built. A stage with a RUN in it is
			// packed the same way and then booted, which is the boot
			// phase Build runs after this Dockerfile.
			ref := expand(stage.Ref, vars)
			// The stage's own name goes on the guest root filesystem
			// rather than on the machine built from it, because that is
			// what a later stage copying out of this one reads (§8): a
			// VM stage's `COPY --from=` is a read of the guest's
			// filesystem, and the machine's own filesystem is the one
			// with the disk image sitting in it as a file.
			containerStageName := stage.Name
			if containerStageName == "" {
				containerStageName = fmt.Sprintf("granule_inherited_%d", stage.Index)
			}

			parts, err := planVMStage(f, stage, stage.Instructions, vars, overriddenArgs, isTarget)
			if err != nil {
				return nil, err
			}

			w("%s", generateUnpackStage(packerImage, ref, stage.Index))
			w("FROM scratch AS %s", containerStageName)
			w("COPY --from=granule_unpack_%d /rootfs/ /", stage.Index)
			for _, in := range parts.offline {
				w("%s", formatInstruction(in))
			}
			w(``)

			packStages, err := guestpack.PackStages(guestpack.Options{
				From:        containerStageName,
				PackedFrom:  ref,
				Tag:         tag,
				KonturImage: konturImage,
				PackerImage: packerImage,
				Variant:     opts.Variant,
				DiskExtraMB: opts.DiskExtraMB + parts.extraDiskMB,
				Volumes:     parts.volumes,
			})
			if err != nil {
				return nil, err
			}
			packStages = renamePackStage(packStages, packerImage, stage.Index)

			w("%s", strings.TrimSpace(packStages))
			w(`LABEL dev.granule.granulefile.kind="from" \`)
			w(`      dev.granule.granulefile.stage="%s" \`, stage.Label())
			w(`      dev.granule.granulefile.version="%s"`, Version)
			for _, in := range parts.labels {
				w("%s", formatInstruction(in))
			}

			writeSettings(w, parts.settings)
			w(``)
			if isTarget {
				boot = withBase(parts.boot, ref)
			}
			continue
		}

		if conv := stage.Converter(); conv != nil && conv.Verb == "PACK" {
			// Container stage converted by in-stage VM PACK
			containerStageName := stage.Name
			if containerStageName == "" {
				containerStageName = fmt.Sprintf("workload_stage_%d", stage.Index)
			}
			vmStageName := stage.ConvertedName
			if vmStageName == "" {
				vmStageName = stage.Name
			}

			if stage.Header.IsVM() && stage.Header.Verb == "UNPACK" {
				w("%s", generateUnpackStage(packerImage, expand(stage.Ref, vars), stage.Index))
				w("FROM scratch AS %s", containerStageName)
				w("COPY --from=granule_unpack_%d /rootfs/ /", stage.Index)
			} else {
				w("FROM %s AS %s", stage.Ref, containerStageName)
			}

			var containerInstrs []Instruction
			var vmInstrs []Instruction
			pastConv := false
			for _, in := range stage.Instructions {
				if in.IsConverter() {
					pastConv = true
					continue
				}
				if !pastConv {
					containerInstrs = append(containerInstrs, in)
					w("%s", formatInstruction(in))
					continue
				}
				vmInstrs = append(vmInstrs, in)
			}

			parts, err := planVMStage(f, stage, vmInstrs, vars, overriddenArgs, isTarget)
			if err != nil {
				return nil, err
			}
			// Below the converter this is a VM stage, and a COPY in one
			// writes into the guest root filesystem (§6) -- which is
			// this very stage, before it is packed.
			for _, in := range parts.offline {
				w("%s", formatInstruction(in))
			}
			w(``)

			if stage.ConvertedName != "" && stage.Name != "" && stage.Name != stage.ConvertedName && !isTarget {
				w("FROM %s AS %s", containerStageName, stage.ConvertedName)
				w(``)
				vmStageName = fmt.Sprintf("granule_vm_%d", stage.Index)
			}

			cfg := extractContainerConfigFromInstructions(containerInstrs)
			packOpts := parsePackConverter(*conv)

			variant := packOpts.variant
			if variant == "" {
				variant = opts.Variant
			}

			packedFrom := expand(resolvePackedFrom(f, stage.Index, stage.Ref), vars)
			packOptsGuest := guestpack.Options{
				From:         containerStageName,
				PackedFrom:   packedFrom,
				Tag:          tag,
				KonturImage:  konturImage,
				PackerImage:  packerImage,
				Variant:      variant,
				DiskExtraMB:  opts.DiskExtraMB + parts.extraDiskMB + packOpts.diskExtraMB,
				ExposedPorts: cfg.ExposedPorts,
				Volumes:      append(slices.Clone(cfg.Volumes), parts.volumes...),
			}
			spec, specFile, specJSON, err := packWorkload(cfg, packedFrom, stage.Index)
			if err != nil {
				return nil, err
			}
			if spec != nil {
				contextFiles[specFile] = specJSON
				packOptsGuest.Spec = spec
				packOptsGuest.SpecFile = files(specFile)
			}
			packStages, err := guestpack.PackStages(packOptsGuest)
			if err != nil {
				return nil, err
			}

			packStages = renamePackStage(packStages, packerImage, stage.Index)
			packStages = renameFinalStage(packStages, konturImage, vmStageName)

			w("%s", strings.TrimSpace(packStages))
			w(`LABEL dev.granule.granulefile.kind="pack" \`)
			w(`      dev.granule.granulefile.stage="%s" \`, stage.Label())
			w(`      dev.granule.granulefile.version="%s"`, Version)
			for _, in := range parts.labels {
				w("%s", formatInstruction(in))
			}

			writeSettings(w, parts.settings)
			w(``)

			if isTarget {
				finalPorts = cfg.ExposedPorts
				boot = withBase(parts.boot, packedFrom)
			}
			continue
		}

		// Stage is a VM stage: VM WRAP or VM WRAP FROM
		var containerStageRef string
		var wrapOpts wrapStageOptions
		var vmStageName string
		var parts vmStageParts
		var cfg containerConfig
		var wrapBase string

		if stage.Header.IsVM() && stage.Header.Verb == "WRAP" {
			// VM WRAP FROM <ref>
			var ref string
			ref, wrapOpts = parseWrapHeader(stage.Header)
			ref = expand(ref, vars)
			containerStageRef = ref
			vmStageName = stage.Name

			if prevCfg, ok := stageConfigs[ref]; ok {
				cfg = prevCfg
			} else if opts.DockerBinary != "" {
				if inspected, err := inspectImageConfig(ctx, opts.DockerBinary, ref); err == nil {
					cfg = inspected
				}
			}

			var err error
			parts, err = planVMStage(f, stage, stage.Instructions, vars, overriddenArgs, isTarget)
			if err != nil {
				return nil, err
			}
			wrapBase = ref
		} else if conv := stage.Converter(); conv != nil && conv.Verb == "WRAP" {
			// Container stage converted by in-stage VM WRAP
			wrapOpts = parseWrapConverter(*conv)
			containerStageName := stage.Name
			if containerStageName == "" {
				containerStageName = fmt.Sprintf("workload_stage_%d", stage.Index)
			}
			containerStageRef = containerStageName
			vmStageName = stage.ConvertedName
			if vmStageName == "" {
				vmStageName = stage.Name
			}

			if stage.Header.IsVM() && stage.Header.Verb == "UNPACK" {
				w("%s", generateUnpackStage(packerImage, expand(stage.Ref, vars), stage.Index))
				w("FROM scratch AS %s", containerStageName)
				w("COPY --from=granule_unpack_%d /rootfs/ /", stage.Index)
			} else {
				// Output container stage instructions up to converter
				w("FROM %s AS %s", stage.Ref, containerStageName)
			}
			var containerInstrs []Instruction
			var vmInstrs []Instruction
			pastConv := false
			for _, in := range stage.Instructions {
				if in.IsConverter() {
					pastConv = true
					continue
				}
				if !pastConv {
					containerInstrs = append(containerInstrs, in)
					w("%s", formatInstruction(in))
					continue
				}
				vmInstrs = append(vmInstrs, in)
			}
			w(``)

			cfg = extractContainerConfigFromInstructions(containerInstrs)
			// Below a VM WRAP the stage is the machine rather than the
			// workload (§5.2), so a COPY there goes into the guest
			// filesystem the wrapped container runs inside.
			var err error
			parts, err = planVMStage(f, stage, vmInstrs, vars, overriddenArgs, isTarget)
			if err != nil {
				return nil, err
			}
			wrapBase = expand(stage.Ref, vars)
		} else {
			return nil, fmt.Errorf("unsupported VM stage type at %s", stage.Label())
		}

		serviceName := wrapOpts.name
		if serviceName == "" {
			if vmStageName != "" {
				serviceName = vmStageName
			} else if stage.Name != "" {
				serviceName = stage.Name
			} else {
				serviceName = "app"
			}
		}

		restart := wrapOpts.restart
		if restart == "" {
			restart = "always"
		}

		haltOnExit := (wrapOpts.exitAction == "poweroff")

		spec := containerspec.Spec{
			Name:       serviceName,
			Image:      containerStageRef,
			Rootfs:     containerspec.RootfsDir,
			Entrypoint: cfg.Entrypoint,
			Cmd:        cfg.Cmd,
			Env:        cfg.Env,
			WorkingDir: cfg.WorkingDir,
			User:       cfg.User,
			Volumes:    cfg.Volumes,
			StopSignal: cfg.StopSignal,
			HaltOnExit: haltOnExit,
		}
		if _, ok := spec.LookupEnv("PATH"); !ok {
			spec.Env = append(spec.Env, "PATH="+containerspec.DefaultPath)
		}

		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("%s cannot be wrapped: %w", stage.Label(), err)
		}

		specJSON, err := spec.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshaling container spec: %w", err)
		}

		specFileName := fmt.Sprintf("container_%d.json", stage.Index)
		systemdUnitFileName := fmt.Sprintf("granule-wrap_%d@.service", stage.Index)
		openrcScriptFileName := fmt.Sprintf("granule-wrap_%d.openrc", stage.Index)

		systemdWrap := fmt.Sprintf(`[Unit]
Description=Run the wrapped container workload (%%i)
Documentation=file:///etc/granule/container.json
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/granule-container
StandardOutput=journal+console
StandardError=journal+console
Restart=%s
KillMode=mixed
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`, restart)

		openrcContent := `#!/sbin/openrc-run
description="Run the container this image wraps"
command="/usr/local/bin/granule-container"
command_background="yes"
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/dev/console"
error_log="/dev/console"
retry="TERM/30/KILL/5"

depend() {
	need localmount
	after net
}
`
		contextFiles[specFileName] = specJSON
		contextFiles[systemdUnitFileName] = []byte(systemdWrap)
		contextFiles[openrcScriptFileName] = []byte(openrcContent)

		wrappedGuestStage := fmt.Sprintf("wrapped_guest_%d", stage.Index)
		w(`FROM %s AS %s`, guestImage, wrappedGuestStage)
		w(`COPY --from=%s %s %s`, konturImage, guestimage.PartsRunner, containerspec.RunnerPath)
		w(`COPY %s %s`, files(specFileName), containerspec.ConfigPath)
		// granule-container's own unit is not written here: the packing
		// stages below lay granule's guest overlay down unconditionally
		// and that overlay owns /etc/systemd/system/granule-container.service
		// and /etc/init.d/granule-container, so a copy from this build
		// would be overwritten before the disk is packed.
		w(`COPY %s /etc/systemd/system/granule-wrap@.service`, files(systemdUnitFileName))
		w(`COPY %s /etc/init.d/granule-wrap`, files(openrcScriptFileName))
		// One service is enabled and not both: granule-container and
		// granule-wrap@<name> run the same container.json, so a guest
		// that started both would run the wrapped workload twice --
		// two copies racing for whatever the first one bound. The unit
		// this build wrote is the one that starts it, since that is the
		// one carrying this VM WRAP's restart policy; the granule-container
		// the packing lays down stays in the image unenabled, for
		// anything that refers to it by name. The two rm -f below are
		// about a guest image that arrived with granule-container
		// already enabled, which is a different question from who
		// supplies the unit.
		w(`RUN set -eu; \`)
		w(`    chmod 0755 %s /etc/init.d/granule-wrap; \`, containerspec.RunnerPath)
		w(`    if [ -d /etc/systemd ]; then \`)
		w(`      mkdir -p /etc/systemd/system/multi-user.target.wants; \`)
		w(`      rm -f /etc/systemd/system/multi-user.target.wants/granule-container.service; \`)
		w(`      ln -sf /etc/systemd/system/granule-wrap@.service \`)
		w(`        /etc/systemd/system/multi-user.target.wants/granule-wrap@%s.service; \`, serviceName)
		w(`    fi; \`)
		w(`    if [ -d /etc/runlevels ]; then \`)
		w(`      mkdir -p /etc/runlevels/default; \`)
		w(`      rm -f /etc/runlevels/default/granule-container; \`)
		w(`      ln -sf /etc/init.d/granule-wrap \`)
		w(`        /etc/runlevels/default/granule-wrap; \`)
		w(`    fi; \`)
		w(`    [ -d /etc/systemd ] || [ -d /etc/runlevels ] || \`)
		w(`      { echo "%s runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }`, guestImage)
		w(`COPY --from=%s / %s`, containerStageRef, containerspec.RootfsDir)
		for _, in := range parts.offline {
			w("%s", formatInstruction(in))
		}
		w(``)

		// Pack stages
		packOpts := guestpack.Options{
			From:        wrappedGuestStage,
			PackedFrom:  guestImage,
			Tag:         tag,
			KonturImage: konturImage,
			PackerImage: packerImage,
			Variant:     opts.Variant,
			DiskExtraMB: opts.DiskExtraMB + parts.extraDiskMB,
			Volumes:     parts.volumes,
		}
		packStages, err := guestpack.PackStages(packOpts)
		if err != nil {
			return nil, err
		}

		packStages = renamePackStage(packStages, packerImage, stage.Index)
		packStages = renameFinalStage(packStages, konturImage, vmStageName)

		w("%s", strings.TrimSpace(packStages))

		if len(cfg.ExposedPorts) > 0 {
			w(`EXPOSE %s`, strings.Join(cfg.ExposedPorts, " "))
		}
		w(`LABEL dev.granule.wrapped-image="%s"`, containerStageRef)
		w(`LABEL dev.granule.granulefile.kind="wrap"`)
		w(`LABEL dev.granule.granulefile.stage="%s"`, stage.Label())
		w(`LABEL dev.granule.granulefile.version="%s"`, Version)
		for _, in := range parts.labels {
			w("%s", formatInstruction(in))
		}

		writeSettings(w, parts.settings)
		w(``)

		if isTarget {
			finalSpec = spec
			finalPorts = cfg.ExposedPorts
			boot = withBase(parts.boot, wrapBase)
		}
	}

	return &BuildPlan{
		File:       f,
		Target:     targetStage,
		TargetKind: targetKind,
		Dockerfile: b.String(),
		Files:      contextFiles,
		Spec:       finalSpec,
		Ports:      finalPorts,
		Boot:       boot,
	}, nil
}

// writeSettings records a stage's VM SETs on the image it produces, as
// the runtime environment variables SettingEnv names (§5.5). Sorted, so
// that the same file compiles to the same build every time.
func writeSettings(w func(string, ...any), settings map[string]string) {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w(`ENV %s="%s"`, k, settings[k])
	}
}

// withBase records on a boot phase what the stage it belongs to started
// from, which is the provenance the committed machine carries.
func withBase(phase *BootPhase, base string) *BootPhase {
	if phase == nil {
		return nil
	}
	phase.Base = base
	return phase
}

// Build compiles a Granulefile and executes the underlying build.
func Build(ctx context.Context, path, source string, opts BuildOptions) error {
	f, err := Check(path, source, Options{Level: Level3, Target: opts.Target})
	if err != nil {
		return err
	}

	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	if opts.PrintDockerfile {
		plan, err := PlanBuild(ctx, f, opts)
		if err != nil {
			return err
		}
		fmt.Fprint(opts.Stdout, plan.Dockerfile)
		if plan.Boot != nil {
			fmt.Fprint(opts.Stdout, "\n"+plan.Boot.Describe())
		}
		return nil
	}

	if opts.Tag == "" {
		return fmt.Errorf("an output tag (-t) is required")
	}

	buildContext := opts.Context
	if buildContext == "" {
		buildContext = "."
	}

	// The build context is the author's, and granule's generated files
	// go in a directory of their own inside it.
	//
	// It has to be theirs: a COPY in their file names a path in their
	// context, and a build run against a context of granule's own
	// making stopped at the first one of those -- reporting a file the
	// author had written as missing, which is every file in §14 that
	// copies anything. Their context is also where a .dockerignore is,
	// and how big the context is, and which symlinks are in it: all of
	// that is docker's to decide, exactly as it would be for the
	// Dockerfile this file is a superset of.
	//
	// A directory of granule's own inside it, rather than files beside
	// theirs, because the names below are generated and an author's
	// container_0.json is not granule's to overwrite. It is removed
	// when the build finishes, whether or not the build worked.
	filesDir, err := os.MkdirTemp(buildContext, ".granule-build-")
	if err != nil {
		return fmt.Errorf("granule stages the files it generates inside the build context, and %s could not be written to: %w", buildContext, err)
	}
	defer os.RemoveAll(filesDir)
	opts.FilesDir = filepath.Base(filesDir)

	plan, err := PlanBuild(ctx, f, opts)
	if err != nil {
		return err
	}

	dockerfilePath := filepath.Join(filesDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(plan.Dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	for name, content := range plan.Files {
		if err := os.WriteFile(filepath.Join(filesDir, name), content, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}

	d := &dockercli.CLI{Binary: opts.DockerBinary}
	fmt.Fprintf(opts.Stdout, "building %s (target: %s, kind: %s)\n", f.Path, plan.Target.Label(), plan.TargetKind)

	// A stage with a `RUN` in it (§6) or a `VM SNAPSHOT` (§5.6) is not
	// one build: docker cannot reach /dev/kvm, so what the Dockerfile
	// produces is the machine before any of that has happened, and the
	// boot below turns it into the one the file describes. It is built
	// under a tag of its own so that a failure in the boot leaves the
	// output tag holding whatever it held before, rather than a machine
	// missing half its file.
	built := opts.Tag
	if plan.Boot != nil {
		built = bootTag(opts.Tag)
		defer removeImage(context.WithoutCancel(ctx), opts, built)
	}

	args := []string{"build", "--file", dockerfilePath, "--tag", built}
	// The file's own variables, as the author set them: granule expanded
	// the ones it had to read for itself with these (§3.5), and the
	// builder below has to see the same values or the FROM it resolves
	// is not the one granule packed.
	for _, arg := range opts.BuildArgs {
		args = append(args, "--build-arg", arg)
	}
	args = append(args, opts.ExtraBuildArgs...)
	args = append(args, buildContext)

	if err := d.Run(ctx, opts.Stdout, opts.Stderr, args...); err != nil {
		return err
	}

	if plan.Boot != nil {
		if err := runBootPhase(ctx, plan.Boot, built, opts); err != nil {
			return err
		}
	}

	fmt.Fprintf(opts.Stdout, "built %s as %s\n", plan.Target.Label(), opts.Tag)
	if size, err := d.ImageSize(ctx, opts.Tag); err == nil {
		fmt.Fprintf(opts.Stdout, "%s is %s; `docker run --privileged %s` boots it\n", opts.Tag, dockercli.HumanSize(size), opts.Tag)
	}
	return nil
}

func parseWrapHeader(in Instruction) (ref string, opts wrapStageOptions) {
	args := fields(in.Args)
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if strings.EqualFold(arg, "FROM") {
			continue
		}
		if strings.HasPrefix(arg, "--name=") {
			opts.name = strings.TrimPrefix(arg, "--name=")
		} else if strings.HasPrefix(arg, "--restart=") {
			opts.restart = strings.TrimPrefix(arg, "--restart=")
		} else if strings.HasPrefix(arg, "--exit-action=") {
			opts.exitAction = strings.TrimPrefix(arg, "--exit-action=")
		} else if strings.HasPrefix(arg, "--runtime=") {
			opts.runtime = strings.TrimPrefix(arg, "--runtime=")
		} else if strings.EqualFold(arg, "AS") {
			if len(args) > 0 {
				args = args[1:] // skip AS name
			}
		} else if ref == "" {
			ref = arg
		} else if opts.name == "" && !strings.HasPrefix(arg, "--") {
			opts.name = arg
		}
	}
	return ref, opts
}

func parseWrapConverter(in Instruction) (opts wrapStageOptions) {
	args := fields(in.Args)
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if strings.HasPrefix(arg, "--name=") {
			opts.name = strings.TrimPrefix(arg, "--name=")
		} else if strings.HasPrefix(arg, "--restart=") {
			opts.restart = strings.TrimPrefix(arg, "--restart=")
		} else if strings.HasPrefix(arg, "--exit-action=") {
			opts.exitAction = strings.TrimPrefix(arg, "--exit-action=")
		} else if strings.HasPrefix(arg, "--runtime=") {
			opts.runtime = strings.TrimPrefix(arg, "--runtime=")
		} else if strings.EqualFold(arg, "AS") {
			if len(args) > 0 {
				args = args[1:] // skip AS name
			}
		} else if opts.name == "" && !strings.HasPrefix(arg, "--") {
			opts.name = arg
		}
	}
	return opts
}

type packStageOptions struct {
	variant     string
	diskExtraMB int
}

func parsePackHeader(in Instruction) (ref string, opts packStageOptions) {
	args := fields(in.Args)
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if strings.EqualFold(arg, "FROM") {
			continue
		}
		if strings.HasPrefix(arg, "--variant=") {
			opts.variant = strings.TrimPrefix(arg, "--variant=")
		} else if strings.HasPrefix(arg, "--disk-extra-mb=") {
			opts.diskExtraMB, _ = strconv.Atoi(strings.TrimPrefix(arg, "--disk-extra-mb="))
		} else if strings.EqualFold(arg, "AS") {
			if len(args) > 0 {
				args = args[1:] // skip AS name
			}
		} else if ref == "" && !strings.HasPrefix(arg, "--") {
			ref = arg
		}
	}
	return ref, opts
}

func parsePackConverter(in Instruction) (opts packStageOptions) {
	args := fields(in.Args)
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if strings.HasPrefix(arg, "--variant=") {
			opts.variant = strings.TrimPrefix(arg, "--variant=")
		} else if strings.HasPrefix(arg, "--disk-extra-mb=") {
			opts.diskExtraMB, _ = strconv.Atoi(strings.TrimPrefix(arg, "--disk-extra-mb="))
		} else if strings.EqualFold(arg, "AS") {
			if len(args) > 0 {
				args = args[1:] // skip AS name
			}
		}
	}
	return opts
}

func generateUnpackStage(packerImage, ref string, stageIndex int) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	unpackStageName := fmt.Sprintf("granule_unpack_%d", stageIndex)
	w(`FROM %s AS %s`, packerImage, unpackStageName)
	w(`RUN if ! command -v debugfs >/dev/null 2>&1; then \`)
	w(`      if command -v apt-get >/dev/null 2>&1; then \`)
	w(`        apt-get update && apt-get install -y --no-install-recommends e2fsprogs && rm -rf /var/lib/apt/lists/*; \`)
	w(`      elif command -v apk >/dev/null 2>&1; then \`)
	w(`        apk add --no-cache e2fsprogs; \`)
	w(`      else \`)
	w(`        echo "the packer image has neither debugfs nor a package manager to install e2fsprogs with" >&2; exit 1; \`)
	w(`      fi; \`)
	w(`    fi`)
	w(`COPY --from=%s %s /disk.img`, ref, guestimage.DiskImage)
	w(`RUN mkdir -p /rootfs && \`)
	w(`    debugfs -R 'rdump / /rootfs' /disk.img && \`)
	w(`    rm -rf /rootfs/lost+found`)
	w(``)
	return b.String()
}

func renamePackStage(packStages, packerImage string, stageIndex int) string {
	targetPackStage := fmt.Sprintf("granule_pack_%d", stageIndex)
	packStages = strings.Replace(packStages, "FROM "+packerImage+" AS pack", "FROM "+packerImage+" AS "+targetPackStage, 1)
	packStages = strings.ReplaceAll(packStages, "COPY --from=pack ", "COPY --from="+targetPackStage+" ")
	return packStages
}

func renameFinalStage(packStages, konturImage, vmStageName string) string {
	if vmStageName == "" {
		return packStages
	}
	targetHeader := fmt.Sprintf("FROM %s", konturImage)
	replHeader := fmt.Sprintf("FROM %s AS %s", konturImage, vmStageName)
	if idx := strings.LastIndex(packStages, targetHeader); idx != -1 {
		return packStages[:idx] + replHeader + packStages[idx+len(targetHeader):]
	}
	return packStages
}

// packWorkload is the workload a packed machine boots into: the
// ENTRYPOINT and CMD the stage above the converter declared, recorded
// into the guest as /etc/granule/container.json (§7, §14.9).
//
// Without this a file's ENTRYPOINT reached the rootfs image's config
// and stopped there -- packing builds a disk, and nothing reads a
// disk's OCI config -- so the machine booted to an init with no
// workload and the file's last two lines did nothing.
func packWorkload(cfg containerConfig, from string, index int) (*containerspec.Spec, string, []byte, error) {
	if len(cfg.Entrypoint) == 0 && len(cfg.Cmd) == 0 {
		return nil, "", nil, nil
	}
	spec := containerspec.Spec{
		Image:      from,
		Rootfs:     "/",
		Entrypoint: cfg.Entrypoint,
		Cmd:        cfg.Cmd,
		Env:        cfg.Env,
		WorkingDir: cfg.WorkingDir,
		User:       cfg.User,
		Volumes:    cfg.Volumes,
		StopSignal: cfg.StopSignal,
		// A packed machine's workload is the machine's reason to be up:
		// the same default `granule guest pack` gives one.
		HaltOnExit: true,
	}
	if _, ok := spec.LookupEnv("PATH"); !ok {
		spec.Env = append(spec.Env, "PATH="+containerspec.DefaultPath)
	}
	if err := spec.Validate(); err != nil {
		return nil, "", nil, fmt.Errorf("the workload this stage declares cannot be recorded: %w", err)
	}
	data, err := spec.Marshal()
	if err != nil {
		return nil, "", nil, fmt.Errorf("marshaling the workload configuration: %w", err)
	}
	return &spec, fmt.Sprintf("container_%d.json", index), data, nil
}

// unbuiltInVMStage is the §10.3 diagnostic for an instruction the
// language allows in a VM stage and this tree cannot build yet: it says
// unimplemented, names the level it belongs to, and says what to write
// instead, so a reader knows whether to change their file or to wait.
func unbuiltInVMStage(in Instruction) Violation {
	reason := fmt.Sprintf("unimplemented: %s in a VM stage is a %s construct (§6), and granule builds %s today. It is refused rather than dropped: a machine that came up without it would be this file quietly building something else",
		strings.ToUpper(in.Name), Level3, Level2)
	switch {
	case strings.EqualFold(in.Name, "RUN"):
		reason = fmt.Sprintf("unimplemented: RUN in a VM stage executes the command inside the booted guest (%s, §6), and granule builds %s today -- nothing here boots a guest per instruction. Write it above the converter, where it runs offline in the container stage that becomes the guest root filesystem, or provision the machine after this build with `granule guest build -setup`, which is the mechanism a VM-stage RUN will be built on",
			Level3, Level2)
	case in.IsVM() && in.Verb == "SNAPSHOT":
		reason = fmt.Sprintf("unimplemented: VM SNAPSHOT snapshots the running guest at the end of the stage (%s, §5.6), which needs the guest booted under KVM during the build, and granule builds %s today. The machine cold-boots instead, so the image would not be the one this file describes",
			Level3, Level2)
	}
	return Violation{
		Line:        in.Line,
		Instruction: in.String(),
		Level:       Level3,
		Rule:        "§10.3",
		Reason:      reason,
	}
}

// buildVars is the scope granule expands its own reads of the file in
// (§3.5): the global ARGs' defaults, overridden by the caller's build
// arguments. The second result is the names those build arguments
// supplied, which is what a stage-local ARG has to know: a `-build-arg`
// the caller gave wins over the default written in the file, wherever
// the declaration is, and the value here is already theirs.
//
// It is the scope a FROM sees, which is the one that matters at this
// level -- every ref granule resolves for itself is a stage header's. A
// stage-local ENV is not in it, because granule reads nothing from
// inside a stage that it has to resolve; the underlying builder expands
// that in the instructions it is handed, as it always did.
//
// A VM stage's own ARGs are the exception, and they widen a copy of this
// map rather than this map: planVMStage expands several of that stage's
// instructions for itself (WORKDIR, USER, VOLUME, VM SET) and hands the
// declarations to the RUNs below them, and §6 scopes them to the stage
// they are written in rather than to the build.
func buildVars(f *File, buildArgs []string) (map[string]string, map[string]bool) {
	vars := make(map[string]string)
	overridden := make(map[string]bool)
	for _, g := range f.Globals {
		if !strings.EqualFold(g.Name, "ARG") {
			continue
		}
		for _, arg := range fields(g.Args) {
			k, v, ok := strings.Cut(arg, "=")
			if !ok {
				// "ARG NAME" with no default: declared, and worth
				// nothing until a build argument supplies it.
				vars[arg] = ""
				continue
			}
			vars[k] = unquote(v)
		}
	}
	for _, kv := range buildArgs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			// --build-arg NAME, which docker reads out of the
			// environment.
			if env, found := os.LookupEnv(kv); found {
				vars[kv] = env
				overridden[kv] = true
			}
			continue
		}
		vars[k] = v
		overridden[k] = true
	}
	return vars, overridden
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// expand applies Dockerfile's variable expansion -- $VAR, ${VAR} and
// ${VAR:-default} -- to the places granule reads a reference out of a
// file for itself: the image a pack packs, the machine an unpack
// unpacks, the provenance a packed image records.
//
// Without it a file that names its base in a global ARG -- which the
// specification allows anywhere Dockerfile does (§3.5), and which is
// how anything parameterized over a base is written -- cannot be built
// at all: granule hands "${ROOTFS}" on as though it were a reference,
// and the failure names a variable rather than an image.
func expand(s string, vars map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			// "$$" is an escaped dollar, and stays one.
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			inner := s[i+2 : i+2+end]
			name, fallback := inner, ""
			hasFallback := false
			if k, v, ok := strings.Cut(inner, ":-"); ok {
				name, fallback, hasFallback = k, v, true
			} else if k, v, ok := strings.Cut(inner, ":+"); ok {
				// ${VAR:+value}: the value when the variable is set.
				if got, found := vars[k]; found && got != "" {
					b.WriteString(v)
				}
				i += 2 + end + 1
				continue
			}
			value, found := vars[name]
			switch {
			case found && value != "":
				b.WriteString(value)
			case hasFallback:
				b.WriteString(fallback)
			}
			i += 2 + end + 1
			continue
		}
		// $NAME, up to the first character a name cannot contain.
		j := i + 1
		for j < len(s) && (s[j] == '_' || (s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			i++
			continue
		}
		b.WriteString(vars[s[i+1:j]])
		i = j
	}
	return b.String()
}

func formatInstruction(in Instruction) string {
	s := in.String()
	if in.Args != "" {
		return s + " " + in.Args
	}
	return s
}

func extractContainerConfigFromInstructions(instrs []Instruction) containerConfig {
	var cfg containerConfig
	for _, in := range instrs {
		args := in.Args
		switch strings.ToUpper(in.Name) {
		case "ENTRYPOINT":
			cfg.Entrypoint = parseStringArrayOrShell(args)
			cfg.Cmd = nil
		case "CMD":
			cfg.Cmd = parseStringArrayOrShell(args)
		case "ENV":
			cfg.Env = mergeEnv(cfg.Env, parseEnvArgs(args))
		case "WORKDIR":
			cfg.WorkingDir = strings.TrimSpace(args)
		case "USER":
			cfg.User = strings.TrimSpace(args)
		case "STOPSIGNAL":
			cfg.StopSignal = strings.TrimSpace(args)
		case "VOLUME":
			cfg.Volumes = append(cfg.Volumes, parseVolumeArgs(args)...)
		case "EXPOSE":
			ports := strings.Fields(args)
			for _, p := range ports {
				if norm, err := dockercli.NormalizePort(p); err == nil {
					cfg.ExposedPorts = append(cfg.ExposedPorts, norm)
				} else {
					cfg.ExposedPorts = append(cfg.ExposedPorts, p)
				}
			}
		}
	}
	if len(cfg.ExposedPorts) > 0 {
		sort.Strings(cfg.ExposedPorts)
		cfg.ExposedPorts = slices.Compact(cfg.ExposedPorts)
	}
	return cfg
}

// parseVolumeArgs reads a VOLUME's mount points, in either of the two
// forms Dockerfile takes: a JSON array or a space-separated list.
func parseVolumeArgs(args string) []string {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			return arr
		}
	}
	return strings.Fields(trimmed)
}

func parseStringArrayOrShell(args string) []string {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			return arr
		}
	}
	return []string{"/bin/sh", "-c", trimmed}
}

func parseEnvArgs(args string) []string {
	// Handles ENV KEY=VALUE KEY2=VALUE2 or ENV KEY VALUE.
	//
	// Split with the lexer's own quote-aware fields rather than
	// strings.Fields: `ENV GREETING="hello world"` is one variable, and
	// splitting it on whitespace recorded GREETING="hello and dropped
	// the rest of the sentence into the workload's environment as
	// nothing at all.
	trimmed := strings.TrimSpace(args)
	parts := fields(trimmed)
	if strings.Contains(trimmed, "=") {
		var out []string
		for _, f := range parts {
			if strings.Contains(f, "=") {
				out = append(out, f)
			}
		}
		return out
	}
	if len(parts) >= 2 {
		return []string{parts[0] + "=" + strings.Join(parts[1:], " ")}
	}
	return nil
}

func mergeEnv(base, additions []string) []string {
	index := make(map[string]int, len(base)+len(additions))
	out := make([]string, 0, len(base)+len(additions))
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		index[k] = len(out)
		out = append(out, e)
	}
	for _, e := range additions {
		k, _, _ := strings.Cut(e, "=")
		if i, ok := index[k]; ok {
			out[i] = e
			continue
		}
		index[k] = len(out)
		out = append(out, e)
	}
	return out
}

func inspectImageConfig(ctx context.Context, dockerBinary, ref string) (containerConfig, error) {
	d := &dockercli.CLI{Binary: dockerBinary}
	var stdout, stderr bytes.Buffer
	if err := d.Run(ctx, &stdout, &stderr, "image", "inspect", "-f", "{{json .Config}}", ref); err != nil {
		return containerConfig{}, err
	}
	var ic struct {
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd"`
		Env          []string            `json:"Env"`
		WorkingDir   string              `json:"WorkingDir"`
		User         string              `json:"User"`
		StopSignal   string              `json:"StopSignal"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &ic); err != nil {
		return containerConfig{}, err
	}
	var ports []string
	for p := range ic.ExposedPorts {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	return containerConfig{
		Entrypoint:   ic.Entrypoint,
		Cmd:          ic.Cmd,
		Env:          ic.Env,
		WorkingDir:   ic.WorkingDir,
		User:         ic.User,
		StopSignal:   ic.StopSignal,
		ExposedPorts: ports,
	}, nil
}

func resolvePackedFrom(f *File, currentIndex int, ref string) string {
	for i := 0; i < currentIndex; i++ {
		s := f.Stages[i]
		for _, n := range s.Names() {
			if strings.EqualFold(n.Name, ref) {
				return resolvePackedFrom(f, i, s.Ref)
			}
		}
		if ref == fmt.Sprintf("%d", i) {
			return resolvePackedFrom(f, i, s.Ref)
		}
	}
	return ref
}
