package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/safepath"
	"github.com/bwsalmon/grain/pkg/version"
)

// bakedHistoryMarker is the file scripts/export-source.sh leaves in the
// .git it bakes: "<revision> <commits>". Its presence is also what says
// a .git beside the tree is that shallow, credential-free repository and
// not an operator's own checkout (-upgrade-src-dir names one), whose
// .git is never handed to a sandbox.
const bakedHistoryMarker = ".git/grain-baked-history"

// bakedSource is the deployment's own copy of grain's source: what
// Config.GrainSourceDir names, and what read_grain_source reads.
type bakedSource struct {
	Dir      string
	Revision string
	// Commits is how many commits of shallow history its .git carries,
	// 0 when it has none a sandbox may be given.
	Commits int
}

// readBakedSource reports the baked copy at dir, and false when there is
// none -- an empty dir (a deployment with no baked copy and no
// -upgrade-src-dir) or one that is not a directory.
func readBakedSource(dir string) (bakedSource, bool) {
	if dir == "" {
		return bakedSource{}, false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return bakedSource{}, false
	}
	src := bakedSource{Dir: dir}
	if raw, err := os.ReadFile(filepath.Join(dir, bakedHistoryMarker)); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) == 2 && buildRevision.MatchString(fields[0]) {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				src.Revision, src.Commits = fields[0], n
			}
		}
	}
	if src.Revision == "" {
		if rev := version.Get().Revision; buildRevision.MatchString(rev) {
			src.Revision = rev
		}
	}
	return src, true
}

// bakedArchives caches each baked copy's archive by directory and
// revision, so a run pays for streaming it into its sandbox but never
// for building it: the tree only changes when the deployment does.
var bakedArchives = struct {
	sync.Mutex
	byKey map[string][]byte
}{byKey: map[string][]byte{}}

// bakedSourceArchive is src's tree as a gzipped tar, built once per
// process for each directory and revision. Its .git goes in only when
// src carries baked history (Commits > 0); anything but directories,
// regular files and symlinks is left out.
func bakedSourceArchive(src bakedSource) ([]byte, error) {
	key := src.Dir + "\x00" + src.Revision + "\x00" + strconv.Itoa(src.Commits)
	bakedArchives.Lock()
	defer bakedArchives.Unlock()
	if archive, ok := bakedArchives.byKey[key]; ok {
		return archive, nil
	}
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(src.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src.Dir, path)
		if err != nil || rel == "." {
			return err
		}
		if rel == ".git" && src.Commits == 0 {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("archiving %s: %w", src.Dir, err)
	}
	bakedArchives.byKey[key] = buf.Bytes()
	return buf.Bytes(), nil
}

// placeGrainSource puts the deployment's baked-in copy of grain's source
// at GrainSourceDir in a self-debug run's sandbox, read-only. It is the
// only source a sandbox is given: nothing here clones bwsalmon/grain or
// widens what the git proxy serves the run, so a self-debug task reaches
// no repository it does not name (model.CapabilityRepos).
//
// The copy is the one read_grain_source reads -- the tree this
// deployment was built from, with whatever shallow history the build
// baked in (scripts/export-source.sh) -- so the agent reads the code
// that is actually running rather than a branch that has moved on.
//
// The cheapest route each backend has:
//
//   - a host directory whose baked copy this process cannot write (an
//     image's or a root-installed host-mode tree) is a symlink to it;
//     nothing is copied, and the files' owner is what keeps it read-only;
//   - any other host directory gets an extracted copy;
//   - a container sandbox created for a self-debug run (Shape.GrainSource)
//     already has the tree mounted read-only from a volume filled once per
//     revision (ContainerSandboxes.grainSourceVolume); grain-src/ is a
//     symlink to it, and nothing is copied;
//   - any other sandbox this process can only run commands in (granule,
//     k8s, or a container whose volume could not be filled) has the
//     cached archive streamed into it and extracted there -- a few
//     seconds per run for grain's tree. Granule could share a host
//     directory only through a hypervisor-wide virtiofs setting
//     (CHV_FS), not per VM, so it keeps paying that.
//
// Never fatal: a deployment with no baked copy, or a placement that
// fails, leaves a run without grain-src/ and a prompt saying why
// (grainSourceSection), never a refused task.
func placeGrainSource(ctx context.Context, task model.Task, srcDir, sandboxRoot string, placer SandboxPlacer, tools []mcp.Tool, out *checkout) {
	if !hasGrant(task, model.SelfDebugCapability) {
		out.GrainSource = grainSourceAbsent
		return
	}
	src, ok := readBakedSource(srcDir)
	if !ok {
		out.GrainSource = grainSourceNotBaked
		return
	}
	out.GrainSourceRevision, out.GrainSourceCommits = src.Revision, src.Commits
	if err := placeBakedSource(ctx, src, sandboxRoot, placer, tools, out); err != nil {
		out.GrainSource = grainSourceUnavailable
		out.GrainSourceWhy = err.Error()
		log.Printf("orchestrator: task %s: placing grain's baked source: %v", task.ID, err)
		return
	}
	out.GrainSource = grainSourceReady
}

func placeBakedSource(ctx context.Context, src bakedSource, sandboxRoot string, placer SandboxPlacer, tools []mcp.Tool, out *checkout) error {
	if sandboxRoot != "" {
		dest := filepath.Join(sandboxRoot, GrainSourceDir)
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		// A tree this process could write is one a run in a sandbox on
		// the same account could write through a symlink, which would be
		// every later run's grain-src/ too. Only one it cannot is linked.
		if unix.Access(src.Dir, unix.W_OK) != nil {
			if err := os.Symlink(src.Dir, dest); err != nil {
				return err
			}
			// git refuses a repository another account owns until it is
			// named in safe.directory -- which only the run's own global
			// config can say.
			if src.Commits > 0 {
				trustGrainSource(ctx, tools, src.Dir)
			}
			return nil
		}
		archive, err := bakedSourceArchive(src)
		if err != nil {
			return err
		}
		if err := extractArchive(archive, dest); err != nil {
			return err
		}
		return readOnlyTree(dest)
	}
	commander, ok := placer.(SandboxCommander)
	if !ok {
		return fmt.Errorf("this sandbox has no route to copy it in")
	}
	dest := GrainSourceDir
	if out.Root != "" {
		dest = strings.TrimRight(out.Root, "/") + "/" + GrainSourceDir
	}
	// A tree the backend mounted is used only when it is this copy's own
	// revision: one from another build would be the wrong code.
	if mounter, ok := placer.(GrainSourceMounter); ok {
		if path, revision, ok := mounter.MountedGrainSource(); ok && revision == src.Revision {
			script := fmt.Sprintf("rm -rf '%[2]s' && ln -s '%[1]s' '%[2]s'", path, dest)
			if _, stderr, code := commander.RunInSandbox(ctx, []string{"sh", "-c", script}, ""); code != 0 {
				return fmt.Errorf("linking the mounted copy exited %d: %s", code, strings.TrimSpace(stderr))
			}
			// Root owns the mounted tree, so git refuses it until it is
			// trusted -- as the linked host tree above is.
			if src.Commits > 0 {
				trustGrainSource(ctx, tools, path)
			}
			return nil
		}
	}
	archive, err := bakedSourceArchive(src)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
rm -rf '%[1]s'
mkdir -p '%[1]s'
tar -xzf - -C '%[1]s'
find '%[1]s' -type f -exec chmod a-w {} +`, dest)
	if _, stderr, code := commander.RunInSandbox(ctx, []string{"sh", "-c", script}, string(archive)); code != 0 {
		return fmt.Errorf("extracting it in the sandbox exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// trustGrainSource adds dir to the sandbox account's safe.directory, once.
func trustGrainSource(ctx context.Context, tools []mcp.Tool, dir string) {
	run, ok := runCommandTool(tools)
	if !ok {
		return
	}
	run(ctx, map[string]any{"command": fmt.Sprintf(
		"git config --global --get-all safe.directory 2>/dev/null | grep -qxF '%[1]s' || git config --global --add safe.directory '%[1]s'", dir)})
}

// extractArchive unpacks a bakedSourceArchive into dest, refusing any
// entry that would land outside it.
func extractArchive(archive []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := safepath.Within(dest, filepath.Join(dest, filepath.FromSlash(hdr.Name)))
		if err != nil {
			return fmt.Errorf("archive entry %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeSymlink:
			err = os.Symlink(hdr.Linkname, path)
		case tar.TypeReg:
			var f *os.File
			if f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o755|0o600); err == nil {
				_, err = io.Copy(f, tr)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
		}
		if err != nil {
			return err
		}
	}
}

// readOnlyTree takes the write bits off every file under dir. Not off
// the directories: a host sandbox is removed with os.RemoveAll on
// Release, which cannot empty a directory it may not write.
func readOnlyTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()&^0o222)
	})
}
