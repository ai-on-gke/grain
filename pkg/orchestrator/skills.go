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

package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"path"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	skillBaseMarker   = "grain-skill-base:"
	skillPathMarker   = "grain-skill-path:"
	skillEscapeMarker = "grain-skill-escape:"
	skillBlobMarker   = "grain-skill-blob:"

	// maxSkillReadBytes bounds how many bytes of each SKILL.md are read out of
	// git to extract its front matter and first paragraph (bodies never enter
	// the prompt).
	maxSkillReadBytes = 6144
	// skillReadBatchSize bounds how many SKILL.md blobs one run_command call
	// base64-encodes at once so stdout stays comfortably below run_command's
	// ~20 KB truncation limit.
	skillReadBatchSize = 2
)

// resolveSkillFolders resolves the deployment default, target repository config
// (operator + .grain/settings.yml), and task override into the ordered list of
// repository-root-relative folders to scan for */SKILL.md.
func resolveSkillFolders(ctx context.Context, store *model.Store, deployment *model.SkillsConfig, task model.Task) ([]string, error) {
	depCfg := deployment
	if depCfg.IsZero() && store != nil {
		mc, err := store.GetConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: reading deployment config for skills: %w", err)
		}
		if mc != nil {
			depCfg = mc.Skills
		}
	}
	var repoCfg *model.SkillsConfig
	if task.Target != nil && store != nil {
		rc, err := store.GetRepoConfig(ctx, *task.Target)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: reading %s's repo config for skills: %w", task.Target, err)
		}
		if rc != nil {
			repoCfg = rc.EffectiveSkills()
		}
	}
	return model.ResolveSkillFolders(depCfg, repoCfg, task.Skills), nil
}

// repoSkillDiscovery is what discoverRepoSkills found: the skills to index
// (at most intree.MaxRepoSkills), how many further SKILL.md candidates it
// left unlisted once that cap was reached, and the folders it searched, so
// the prompt can tell the agent that the index is not the whole set and
// where to look for the rest.
type repoSkillDiscovery struct {
	Skills   []intree.SkillIndexEntry
	Unlisted int
	Folders  []string
}

// discoverRepoSkills finds */SKILL.md one level deep under each configured
// folder at the task's base branch commit in prepared.Dir, rejecting symlinks
// and escaping paths, skipping malformed SKILL.md files with a run-log note,
// and returning at most intree.MaxRepoSkills entries. Once it has that many
// it stops reading: every SKILL.md is a git show in the sandbox, and a
// repository with thousands of skill directories would otherwise spend a
// tool call per pair of them before the agent's first turn. Candidates it
// did not read are counted in Unlisted (a malformed one among them is then
// not noted).
func discoverRepoSkills(ctx context.Context, tools []mcp.Tool, prepared checkout, task model.Task, folders []string, runID string, logf func(string, ...any)) repoSkillDiscovery {
	if logf == nil {
		logf = log.Printf
	}
	if len(folders) == 0 || prepared.Dir == "" || prepared.StateRepo || task.Target == nil {
		return repoSkillDiscovery{}
	}
	run, ok := runCommandTool(tools)
	if !ok {
		return repoSkillDiscovery{}
	}

	var cleanFolders []string
	for _, raw := range folders {
		cleaned, err := model.CleanSkillFolder(raw)
		if err != nil {
			logf("orchestrator: run %s: rejecting configured skill folder %q: %v", runID, raw, err)
			continue
		}
		if !slices.Contains(cleanFolders, cleaned) {
			cleanFolders = append(cleanFolders, cleaned)
		}
	}
	if len(cleanFolders) == 0 {
		return repoSkillDiscovery{}
	}

	explicitBase := ""
	if task.Base != "" {
		if !gitSafe.MatchString(task.Base) {
			return repoSkillDiscovery{}
		}
		explicitBase = task.Base
	}

	listScript := buildSkillTreeListScript(prepared.Dir, explicitBase, cleanFolders)
	res := run(ctx, map[string]any{"command": listScript})
	if res.IsError {
		return repoSkillDiscovery{}
	}

	var baseSHA string
	var candidates []string
	seenCandidates := map[string]bool{}
	for _, line := range strings.Split(res.Text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, skillBaseMarker):
			baseSHA = strings.TrimSpace(strings.TrimPrefix(line, skillBaseMarker))
		case strings.HasPrefix(line, skillEscapeMarker):
			rest := strings.TrimPrefix(line, skillEscapeMarker)
			escPath, why, _ := strings.Cut(rest, ":")
			logf("orchestrator: run %s: skipping repo skill path %s (%s)", runID, escPath, why)
		case strings.HasPrefix(line, skillPathMarker):
			relPath := strings.TrimSpace(strings.TrimPrefix(line, skillPathMarker))
			if relPath != "" && !seenCandidates[relPath] && isSafeRepoSkillFilePath(relPath) {
				seenCandidates[relPath] = true
				candidates = append(candidates, relPath)
			}
		}
	}
	if baseSHA == "" || !gitSafe.MatchString(baseSHA) || len(candidates) == 0 {
		return repoSkillDiscovery{}
	}

	var discovered []intree.SkillIndexEntry
	capped := 0
	for start := 0; start < len(candidates); start += skillReadBatchSize {
		if len(discovered) >= intree.MaxRepoSkills {
			capped += len(candidates) - start
			break
		}
		end := min(start+skillReadBatchSize, len(candidates))
		batch := candidates[start:end]
		blobs := readSkillBlobsAtBase(ctx, run, prepared.Dir, baseSHA, batch)
		for _, relPath := range batch {
			raw, ok := blobs[relPath]
			if !ok {
				logf("orchestrator: run %s: skipping repo skill %s: could not read from base %s", runID, relPath, baseSHA)
				continue
			}
			relDir := path.Dir(relPath)
			dirName := path.Base(relDir)
			meta, err := intree.ParseSkillMD(raw, dirName, intree.MaxSkillDescriptionChars)
			if err != nil {
				logf("orchestrator: run %s: skipping malformed repo skill %s: %v", runID, relPath, err)
				continue
			}
			if len(discovered) >= intree.MaxRepoSkills {
				capped++
				continue
			}
			sandboxPath := resolveSandboxPath(prepared.Root, path.Join(prepared.Dir, relPath))
			discovered = append(discovered, intree.SkillIndexEntry{
				Name:        meta.Name,
				Path:        sandboxPath,
				Source:      "repo: " + relDir,
				RepoRelDir:  relDir,
				Description: meta.Description,
			})
		}
	}
	if capped > 0 {
		logf("orchestrator: run %s: capped discovered repo skills at %d (%d more SKILL.md candidate(s) left unlisted)",
			runID, intree.MaxRepoSkills, capped)
	}
	return repoSkillDiscovery{Skills: discovered, Unlisted: capped, Folders: cleanFolders}
}

func isSafeRepoSkillFilePath(relPath string) bool {
	if strings.HasPrefix(relPath, "/") || strings.ContainsRune(relPath, '\\') || strings.ContainsRune(relPath, 0) {
		return false
	}
	if path.Clean(relPath) != relPath || !strings.HasSuffix(relPath, "/SKILL.md") {
		return false
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == "" || seg == "." || seg == ".." || !gitSafe.MatchString(seg) {
			return false
		}
	}
	return true
}

func buildSkillTreeListScript(checkoutDir, explicitBase string, folders []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "if [ ! -d %s ]; then exit 0; fi\n", shellQuote(checkoutDir+"/.git"))
	fmt.Fprintf(&b, "cd %s\n", shellQuote(checkoutDir))
	b.WriteString("workreal=$(pwd -P)\n")
	b.WriteString("base=''\n")
	if explicitBase != "" {
		fmt.Fprintf(&b, "if git rev-parse --verify --quiet %s >/dev/null 2>&1; then\n  base=$(git rev-parse --verify %s)\nfi\n",
			shellQuote("refs/remotes/origin/"+explicitBase), shellQuote("refs/remotes/origin/"+explicitBase))
	} else {
		b.WriteString("for cand in refs/remotes/origin/HEAD refs/remotes/origin/main refs/remotes/origin/master; do\n")
		b.WriteString("  if git rev-parse --verify --quiet \"$cand\" >/dev/null 2>&1; then\n")
		b.WriteString("    base=$(git rev-parse --verify \"$cand\")\n    break\n  fi\n")
		b.WriteString("done\n")
		// No other remote branch is a stand-in for the default: any of
		// them may be another run's working branch, whose SKILL.md files
		// that run's agent wrote.
	}
	b.WriteString("if [ -z \"$base\" ]; then exit 0; fi\n")
	fmt.Fprintf(&b, "echo '%s'\"$base\"\n", skillBaseMarker)

	for _, folder := range folders {
		segments := strings.Split(folder, "/")
		b.WriteString("folder_ok=1\n")
		var prefix string
		for _, seg := range segments {
			if prefix == "" {
				prefix = seg
			} else {
				prefix = prefix + "/" + seg
			}
			qPrefix := shellQuote(prefix)
			fmt.Fprintf(&b, "if [ \"$folder_ok\" = 1 ]; then\n")
			fmt.Fprintf(&b, "  if [ -L %s ]; then\n    echo '%s%s:symlink in checkout'\n    folder_ok=0\n  else\n",
				qPrefix, skillEscapeMarker, prefix)
			fmt.Fprintf(&b, "    entry=$(git ls-tree \"$base\" -- %s)\n", qPrefix)
			fmt.Fprintf(&b, "    if [ -z \"$entry\" ]; then\n      folder_ok=0\n    else\n")
			fmt.Fprintf(&b, "      mode=${entry%%%% *}\n")
			fmt.Fprintf(&b, "      if [ \"$mode\" = '120000' ]; then\n        echo '%s%s:symlink in base branch'\n        folder_ok=0\n      elif [ \"$mode\" != '040000' ]; then\n        folder_ok=0\n      fi\n",
				skillEscapeMarker, prefix)
			fmt.Fprintf(&b, "    fi\n  fi\nfi\n")
		}
		qFolder := shellQuote(folder)
		fmt.Fprintf(&b, "if [ \"$folder_ok\" = 1 ]; then\n")
		fmt.Fprintf(&b, "  git ls-tree \"$base\" -- %s | while IFS= read -r line; do\n", shellQuote(folder+"/"))
		b.WriteString("    [ -z \"$line\" ] && continue\n")
		b.WriteString("    cmode=${line%% *}\n")
		b.WriteString("    cpath=${line#*	}\n")
		fmt.Fprintf(&b, "    if [ \"$cmode\" = '120000' ]; then\n      echo '%s'\"$cpath\"':symlink in base branch'\n      continue\n    fi\n", skillEscapeMarker)
		b.WriteString("    [ \"$cmode\" != '040000' ] && continue\n")
		fmt.Fprintf(&b, "    if [ -L \"$cpath\" ]; then\n      echo '%s'\"$cpath\"':symlink in checkout'\n      continue\n    fi\n", skillEscapeMarker)
		b.WriteString("    skpath=\"$cpath/SKILL.md\"\n")
		b.WriteString("    skentry=$(git ls-tree \"$base\" -- \"$skpath\")\n")
		b.WriteString("    [ -z \"$skentry\" ] && continue\n")
		b.WriteString("    skmode=${skentry%% *}\n")
		fmt.Fprintf(&b, "    if [ \"$skmode\" = '120000' ]; then\n      echo '%s'\"$skpath\"':symlink in base branch'\n      continue\n    fi\n", skillEscapeMarker)
		b.WriteString("    if [ \"$skmode\" != '100644' ] && [ \"$skmode\" != '100755' ]; then continue; fi\n")
		fmt.Fprintf(&b, "    if [ -L \"$skpath\" ]; then\n      echo '%s'\"$skpath\"':symlink in checkout'\n      continue\n    fi\n", skillEscapeMarker)
		fmt.Fprintf(&b, "    resolved=$(realpath -m \"$skpath\" 2>/dev/null || true)\n")
		fmt.Fprintf(&b, "    case \"$resolved\" in\n      \"$workreal/\"*) echo '%s'\"$skpath\" ;;\n      *) echo '%s'\"$skpath\"':escapes repository root' ;;\n    esac\n",
			skillPathMarker, skillEscapeMarker)
		b.WriteString("  done\n")
		_ = qFolder
		b.WriteString("fi\n")
	}
	return b.String()
}

func readSkillBlobsAtBase(ctx context.Context, run func(context.Context, map[string]any) mcp.Result, checkoutDir, baseSHA string, paths []string) map[string][]byte {
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "cd %s\n", shellQuote(checkoutDir))
	for i, p := range paths {
		spec := baseSHA + ":" + p
		fmt.Fprintf(&b, "b64=$(git show %s | head -c %d | base64 | tr -d '\\n')\n", shellQuote(spec), maxSkillReadBytes)
		fmt.Fprintf(&b, "echo '%s%d:'\"$b64\"\n", skillBlobMarker, i)
	}
	res := run(ctx, map[string]any{"command": b.String()})
	if res.IsError {
		return nil
	}
	out := make(map[string][]byte, len(paths))
	for _, line := range strings.Split(res.Text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, skillBlobMarker) {
			continue
		}
		rest := strings.TrimPrefix(line, skillBlobMarker)
		idxStr, b64, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err != nil || idx < 0 || idx >= len(paths) {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			continue
		}
		out[paths[idx]] = decoded
	}
	return out
}

// mergePromptSkills merges in-process capability skill sections and
// repo-discovered skills into one prompt index section. Skills supplied by a
// capability server (pass prompt sections or placements under SkillsDir) are
// left in their attributed blocks and only reserve their names; pass prompt
// sections are returned unchanged. When repoSkills is empty, capability
// sections are returned unchanged too.
func mergePromptSkills(
	inProcessSections []string,
	passPromptSections string,
	materialized []model.Materialized,
	discovery repoSkillDiscovery,
	repoLabel string,
	runID string,
	logf func(string, ...any),
) (keptInProcess []string, unifiedSkillsSection string, keptPassSections string) {
	repoSkills := discovery.Skills
	if len(repoSkills) == 0 {
		return inProcessSections, "", passPromptSections
	}
	if logf == nil {
		logf = log.Printf
	}

	var capSkills []intree.SkillIndexEntry
	seenCap := map[string]bool{}
	addCap := func(entries ...intree.SkillIndexEntry) {
		for _, e := range entries {
			if e.Name == "" || seenCap[e.Name] {
				continue
			}
			seenCap[e.Name] = true
			capSkills = append(capSkills, e)
		}
	}

	for _, sec := range inProcessSections {
		if entries, ok := intree.ParseCapabilitySkillSection(sec); ok {
			addCap(entries...)
			continue
		}
		keptInProcess = append(keptInProcess, sec)
	}

	// Skills a capability server supplied -- its mint_skill prompt section
	// and any SKILL.md it placed under SkillsDir -- stay in that server's
	// own attributed <capability> block (capabilities.spec.md §5.4.2):
	// lifting their names and descriptions into the index would present a
	// server's text as grain's. They only reserve their names, so a repo
	// skill of the same name is listed under its repository path.
	var reserved []string
	_, passCapSkills := stripCapabilitySkillBlocks(passPromptSections)
	for _, e := range passCapSkills {
		reserved = append(reserved, e.Name)
	}
	for _, m := range materialized {
		for _, pl := range m.Materialization.Placements {
			clean := path.Clean(pl.Path)
			prefix := intree.SkillsDir + "/"
			if !strings.HasPrefix(clean, prefix) || path.Base(clean) != "SKILL.md" {
				continue
			}
			dir := path.Dir(strings.TrimPrefix(clean, prefix))
			if dir == "." || strings.Contains(dir, "/") {
				continue
			}
			reserved = append(reserved, dir)
		}
	}

	merged, omitted := intree.FormatSkillsIndexReserving(capSkills, reserved, repoSkills, repoLabel, intree.MaxSkillsIndexBytes)
	if omitted > 0 {
		logf("orchestrator: run %s: omitted %d repo skill(s) to stay within the %d-byte skills index limit",
			runID, omitted, intree.MaxSkillsIndexBytes)
	}
	merged += unlistedRepoSkillsNote(omitted+discovery.Unlisted, discovery.Folders)
	return keptInProcess, merged, passPromptSections
}

// unlistedRepoSkillsNote is the sentence, in grain's own voice and so
// outside the <repo-skills> block, that tells the agent the index is not
// every skill the repository has: without it an agent handed 46 of 58
// skills has no way to know the other 12 exist.
func unlistedRepoSkillsNote(unlisted int, folders []string) string {
	if unlisted <= 0 {
		return ""
	}
	where := "the repository's skill folders"
	if len(folders) > 0 {
		quoted := make([]string, len(folders))
		for i, f := range folders {
			quoted[i] = f + "/"
		}
		where = strings.Join(quoted, ", ")
	}
	return fmt.Sprintf("\n%d more repository skill(s) are not listed above: the index keeps at most %d skills and %d bytes. "+
		"Look for */SKILL.md under %s in the checkout if none of these fits.",
		unlisted, intree.MaxRepoSkills, intree.MaxSkillsIndexBytes, where)
}

// stripCapabilitySkillBlocks finds any <capability ...> blocks in
// passPromptSections whose body is a mint_skill prompt section, returning the
// other blocks and the skills those name. mergePromptSkills uses only the
// skill names: the blocks themselves stay in the prompt with their
// attribution.
func stripCapabilitySkillBlocks(passPromptSections string) (string, []intree.SkillIndexEntry) {
	if strings.TrimSpace(passPromptSections) == "" {
		return passPromptSections, nil
	}
	var kept []string
	var extracted []intree.SkillIndexEntry
	rest := passPromptSections

	for rest != "" {
		start := strings.Index(rest, "<capability ")
		if start < 0 {
			if tail := strings.TrimSpace(rest); tail != "" {
				kept = append(kept, tail)
			}
			break
		}
		if before := strings.TrimSpace(rest[:start]); before != "" {
			kept = append(kept, before)
		}
		rest = rest[start:]
		endTag := "</capability>"
		end := strings.Index(rest, endTag)
		if end < 0 {
			kept = append(kept, strings.TrimSpace(rest))
			break
		}
		block := rest[:end+len(endTag)]
		rest = rest[end+len(endTag):]

		const attribution = "The following was supplied by a capability server, not by the task's author."
		if idx := strings.Index(block, attribution); idx >= 0 {
			inner := strings.TrimSpace(block[idx+len(attribution) : len(block)-len(endTag)])
			if entries, ok := intree.ParseCapabilitySkillSection(inner); ok {
				extracted = append(extracted, entries...)
				continue
			}
		}
		kept = append(kept, block)
	}
	return strings.Join(kept, "\n\n"), extracted
}
