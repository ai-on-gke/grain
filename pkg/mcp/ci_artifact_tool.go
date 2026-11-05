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

package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// maxCIArtifactDownloadBytes bounds what get_ci_artifact will fetch in
// one call. It is generous above a deployment's own default single-
// artifact bound (32 MiB, model.Config.CIArtifactFileMB) so an
// ordinarily-sized trace always fits, and it is a backstop rather than
// the real bound: the real one is whatever the deployment configured,
// enforced when the artifact was collected, and reported on
// ci.Artifact.Bytes before this tool downloads a single byte of it.
const maxCIArtifactDownloadBytes = 128 << 20

// GrainCIArtifactRunner is what get_ci_artifact needs beyond
// GrainCIRunner: a way to fetch one artifact's bytes. Kept as a separate
// interface, embedding GrainCIRunner, rather than a method added to
// GrainCIRunner itself -- the latter would force every existing test
// double of that wide interface to grow a method it has no answer for,
// for the sake of one new tool.
type GrainCIArtifactRunner interface {
	GrainCIRunner
	// DownloadGrainCIArtifact writes one artifact's bytes to w and
	// reports how many there were.
	DownloadGrainCIArtifact(ctx context.Context, ref ci.ArtifactRef, w io.Writer) (int64, error)
}

// NewGrainCIArtifactTools is get_ci_artifact, the tool that closes the
// gap left documented in grain_ci_wait_tool.go's renderGrainCIRun: a
// sandbox cannot reach this deployment's API, so a run told "the
// Playwright trace of this failure was kept" could do nothing with that
// but name it to a person. This tool fetches it and writes it into the
// sandbox's own workspace instead, bounded by root the way the sandbox
// tools are (sandbox_tools.go).
//
// root is the sandbox workspace this run writes everything else into,
// not a new grant of its own: a run already allowed to write anywhere
// under root with write_file is not given anything new by also being
// able to write a CI artifact there. An empty root means there is
// nowhere to write it, so the tool refuses every call rather than
// writing outside a workspace nobody confirmed exists -- which is also
// why pkg/cli/mcpserver.go only registers this tool when -sandbox-root
// was given. branch is this run's own branch, used to default run_id to the
// latest run on the branch before falling back to the whole repository.
func NewGrainCIArtifactTools(runner GrainCIArtifactRunner, scope CIRepoScope, root string, branch ...string) []Tool {
	var taskBranch string
	if len(branch) > 0 {
		taskBranch = strings.TrimSpace(branch[0])
	}
	return []Tool{
		{
			Name: "get_ci_artifact",
			Description: fmt.Sprintf(
				"Download one artifact a grain CI job kept -- a Playwright trace, a core dump, whatever the "+
					"job's own \"artifacts:\" declared -- and write it into your sandbox workspace, since a sandbox "+
					"cannot reach the files any other way. wait_for_checks and get_ci_log both name what a failed "+
					"job kept (\"Artifact trace: 2831 bytes, kept with this run\"); pass that name here. %s "+
					"Supply run_id (optional, defaults to latest on branch), job (or job_id, optional where the run "+
					"has only one job worth reading), and name (optional where the job kept only one artifact). "+
					"path is where to write it (optional; defaults to the artifact's own file name, at the "+
					"workspace's root). Every artifact is a gzipped tar -- extract it with run_command (\"tar xzf "+
					"<path>\") and read what comes out with read_file. Refused above %d bytes, which is far past a "+
					"deployment's own default artifact size.",
				scope.logScopeSentence(), maxCIArtifactDownloadBytes),
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string", "description": "Specific CI run ID (defaults to latest on branch)."},
					"job":    map[string]any{"type": "string", "description": "Specific job name or ID."},
					"job_id": map[string]any{"type": "string", "description": "Specific job name or ID."},
					"name":   map[string]any{"type": "string", "description": "The artifact's own name, as the job declared it."},
					"path":   map[string]any{"type": "string", "description": "Where to write it in the sandbox workspace (default: the artifact's own file name, at the workspace root)."},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				if strings.TrimSpace(root) == "" {
					return Result{Text: "This run has no sandbox workspace to write a CI artifact into.", IsError: true}
				}
				if !scope.mayReadAny() {
					return Result{Text: scope.refuseAll().Error(), IsError: true}
				}
				runID, _ := argString(args, "run_id")
				runID = strings.TrimSpace(runID)
				if runID == "" {
					defaultRepo := scope.defaultRepo()
					if defaultRepo == "" && !scope.AllRepos {
						return Result{Text: "Specify run_id (list_ci_runs reports the runs this run may read).", IsError: true}
					}
					var page ci.RunPage
					var err error
					if taskBranch != "" && strings.TrimSpace(scope.Repo) != "" {
						page, err = runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: defaultRepo, Ref: taskBranch, Descending: true}, 1)
					}
					if taskBranch == "" || strings.TrimSpace(scope.Repo) == "" || err != nil || len(page.Runs) == 0 {
						page, err = runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: defaultRepo, Descending: true}, 1)
					}
					if err != nil || len(page.Runs) == 0 {
						return Result{Text: "No grain CI runs are currently remembered for this repository.", IsError: true}
					}
					runID = page.Runs[0].ID
				}
				run, err := runner.GrainCIRun(ctx, runID)
				if err != nil {
					return Result{Text: fmt.Sprintf("reading grain CI run %s: %v", runID, err), IsError: true}
				}
				if !scope.permits(run.Repo) {
					return Result{Text: scope.refuseRun(run).Error(), IsError: true}
				}
				jobName, _ := argString(args, "job_id")
				if jobName == "" {
					jobName, _ = argString(args, "job")
				}
				job, err := grainCIJob(run, strings.TrimSpace(jobName))
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				name, _ := argString(args, "name")
				artifact, err := grainCIArtifact(run, *job, strings.TrimSpace(name))
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				if artifact.Bytes > maxCIArtifactDownloadBytes {
					return Result{Text: fmt.Sprintf(
						"Artifact %s of grain CI run %s job %s is %s, over the %s this tool will fetch. "+
							"A person can still get it with \"grain ci artifact %s %s %s\".",
						artifact.Name, run.ID, job.Name, humanArtifactBytes(artifact.Bytes),
						humanArtifactBytes(maxCIArtifactDownloadBytes), artifact.Run, artifact.Job, artifact.Name),
						IsError: true}
				}
				path, _ := argString(args, "path")
				path = strings.TrimSpace(path)
				if path == "" {
					path = artifact.Ref().Filename()
				}
				real, named, notice, err := resolveForWrite(root, path)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
					return Result{Text: fmt.Sprintf("creating %s: %v", filepath.Dir(path), err), IsError: true}
				}
				file, err := os.Create(real)
				if err != nil {
					return Result{Text: fmt.Sprintf("creating %s: %v", path, err), IsError: true}
				}
				written, err := runner.DownloadGrainCIArtifact(ctx, artifact.Ref(), file)
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					os.Remove(real)
					return Result{Text: fmt.Sprintf("downloading artifact %s of grain CI run %s job %s: %v",
						artifact.Name, run.ID, job.Name, err), IsError: true}
				}
				return Result{Text: noticed(notice, fmt.Sprintf(
					"Wrote artifact %s of grain CI run %s (%s) job %s to %s: %s. "+
						"It is a gzipped tar -- extract it with run_command (\"tar xzf %s\") and read what comes out with read_file.",
					artifact.Name, run.ID, run.Ref, job.Name, wroteName(path, named, notice), humanArtifactBytes(written), named))}
			},
		},
	}
}

// grainCIArtifact is which of a job's artifacts a call is about: the one
// named, or -- where none was -- the one worth fetching.
//
// Ambiguity is refused rather than guessed at, with the artifacts
// named, the same way grainCIJob refuses an unnamed job among several:
// a trace is not the core dump, and fetching the wrong one silently
// looks exactly like a successful read of the right one.
func grainCIArtifact(run ci.Run, job ci.JobRun, name string) (*ci.Artifact, error) {
	if name != "" {
		for i := range job.Artifacts {
			if job.Artifacts[i].Name == name {
				if !job.Artifacts[i].Stored {
					return nil, fmt.Errorf("artifact %q of grain CI run %s job %s was not kept: %s",
						name, run.ID, job.Name, job.Artifacts[i].Note)
				}
				return &job.Artifacts[i], nil
			}
		}
		return nil, fmt.Errorf("grain CI run %s job %s has no artifact %q. It kept: %s",
			run.ID, job.Name, name, grainCIArtifactList(job))
	}
	var stored []*ci.Artifact
	for i := range job.Artifacts {
		if job.Artifacts[i].Stored {
			stored = append(stored, &job.Artifacts[i])
		}
	}
	switch len(stored) {
	case 0:
		if len(job.Artifacts) == 0 {
			return nil, fmt.Errorf("grain CI run %s job %s kept no artifacts", run.ID, job.Name)
		}
		return nil, fmt.Errorf("grain CI run %s job %s declared artifacts but kept none: %s",
			run.ID, job.Name, grainCIArtifactList(job))
	case 1:
		return stored[0], nil
	default:
		return nil, fmt.Errorf("grain CI run %s job %s kept several artifacts and none of them is the "+
			"obvious one, so name one with name: %s", run.ID, job.Name, grainCIArtifactList(job))
	}
}

func grainCIArtifactList(job ci.JobRun) string {
	names := make([]string, 0, len(job.Artifacts))
	for _, a := range job.Artifacts {
		if a.Stored {
			names = append(names, fmt.Sprintf("%s (%s)", a.Name, humanArtifactBytes(a.Bytes)))
		} else {
			names = append(names, fmt.Sprintf("%s (not kept)", a.Name))
		}
	}
	return strings.Join(names, ", ")
}

// humanArtifactBytes is a byte count the way an Artifact's own Note
// already quotes one ("280MB, over the 32MB an artifact may be" --
// pkg/grit/artifacts/dirartifacts.go's humanBytes), kept as its own copy here rather
// than exported from pkg/ci for one caller: this tool's refusals read
// like the Note they are quoting back only if they use the same units.
func humanArtifactBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
