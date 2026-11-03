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

package namespace

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// CoreModulePath is grain core's Go module, which a deployment's own
// module (the one src:// serves) either is or builds on.
const CoreModulePath = "github.com/ai-on-gke/grain"

// CoreSource finds core's source for a daemon whose src:// tree, srcDir,
// is a module that requires core rather than core itself. dir is where
// that source is, "" when none was found; mod is the requirement as
// module@version, "" when srcDir is core (or has no go.mod), in which
// case there is nothing to find.
//
// Looked for, in order: shipped, the first of candidates that is a
// directory (an image's baked-in copy, which like sourceDir's is the one
// guaranteed to be what the binary was built from); a local replace
// directive in srcDir's go.mod; and the Go module cache, where a
// source-built install's own `go build` left it.
func CoreSource(srcDir string, candidates ...string) (dir, mod string) {
	if strings.TrimSpace(srcDir) == "" {
		return "", ""
	}
	gomod := filepath.Join(srcDir, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", ""
	}
	// Parse rather than ParseLax, which drops replace directives; lax
	// only for a go.mod newer than this build's x/mod understands.
	f, err := modfile.Parse(gomod, data, nil)
	if err != nil {
		f, err = modfile.ParseLax(gomod, data, nil)
	}
	if err != nil || f.Module == nil || f.Module.Mod.Path == CoreModulePath {
		return "", ""
	}
	var version string
	for _, r := range f.Require {
		if r.Mod.Path == CoreModulePath {
			version = r.Mod.Version
		}
	}
	if version == "" {
		return "", ""
	}
	mod = CoreModulePath + "@" + version
	for _, c := range candidates {
		if isDir(c) {
			return c, mod
		}
	}
	for _, r := range f.Replace {
		if r.Old.Path != CoreModulePath || r.New.Version != "" {
			continue
		}
		local := r.New.Path
		if !filepath.IsAbs(local) {
			local = filepath.Join(srcDir, local)
		}
		if isDir(local) {
			return local, mod
		}
	}
	escPath, err1 := module.EscapePath(CoreModulePath)
	escVer, err2 := module.EscapeVersion(version)
	if err1 == nil && err2 == nil {
		for _, cache := range moduleCaches() {
			if d := filepath.Join(cache, filepath.FromSlash(escPath)+"@"+escVer); isDir(d) {
				return d, mod
			}
		}
	}
	return "", mod
}

// moduleCaches are where `go env GOMODCACHE` would say, worked out the
// way the go command does rather than by running it.
func moduleCaches() []string {
	if c := os.Getenv("GOMODCACHE"); c != "" {
		return []string{c}
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		if first := filepath.SplitList(gopath); len(first) > 0 && first[0] != "" {
			return []string{filepath.Join(first[0], "pkg", "mod")}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return []string{filepath.Join(home, "go", "pkg", "mod")}
	}
	return nil
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
