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

package repodecl

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// LoadSettings reads the one repository settings declaration tree holds.
//
// It returns the declared settings and the path it was read from. A repository
// with no settings file declares no settings and is not an error -- the zero
// DeclaredSettingsFile is returned with a nil pointer.
func LoadSettings(tree fs.FS, repo model.RepoRef) (*model.DeclaredSettingsFile, error) {
	found, err := settingsFile(tree)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, nil
	}
	declared, err := parseSettings(*found, repo)
	if err != nil {
		return nil, err
	}
	return &model.DeclaredSettingsFile{Path: found.path, Settings: declared}, nil
}

func settingsFile(tree fs.FS) (*file, error) {
	var found []file
	paths := []string{
		model.SettingsDeclarationPath,
		model.SettingsDeclarationAltPath,
		model.ConfigDeclarationPath,
		model.ConfigDeclarationAltPath,
	}
	for _, path := range paths {
		text, err := fs.ReadFile(tree, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		found = append(found, file{path: path, stem: "settings", text: text})
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return &found[0], nil
	default:
		names := make([]string, len(found))
		for i, f := range found {
			names[i] = f.path
		}
		return nil, fmt.Errorf("multiple settings files found (%s): a repository has at most one settings file (use %s)",
			strings.Join(names, ", "), model.SettingsDeclarationPath)
	}
}

// rawRepoSettings is the whole of what a repository may write about
// itself: two keys.
//
// Everything a repository may *not* write is in pkg/declschema
// alongside them, with the reason each one is refused, and is checked
// against a file's top-level keys before this struct is filled (decode
// -> refuse). So `capabilities: [gcp-key]` is answered with the sentence
// saying why a repository does not grant its own authority, rather than
// with "field not found" -- and a key that is neither accepted here nor
// refused there is still an error, because the decoder runs with
// KnownFields.
//
// One spelling per key, hyphenated, like every other declaration file.
// The snake_case and abbreviated synonyms this struct carried before
// grain/task-340 are gone: a format with two spellings of a key is one
// whose meaning depends on which the parser reads first, and the keys
// that matter here are the ones nobody may write at all, which have to
// be recognised under every spelling somebody might reach for.
type rawRepoSettings struct {
	PromptExtension *string             `yaml:"prompt-extension"`
	SetupCommand    *string             `yaml:"setup-command"`
	PortBranches    []string            `yaml:"port-branches"`
	PortTypes       map[string][]string `yaml:"port-types"`
}

// conventionalType is what a `port-types` entry may name: a
// conventional-commit type, lower-case.
var conventionalType = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// parseSettings reads the one settings file.
//
// A file with nothing in it declares nothing and is not an error. That
// is not a nicety: with two keys accepted and twelve refused, a
// `.grain/settings.yml` holding only a comment saying where the rest of
// them went is a perfectly reasonable file for a repository to keep, and
// yaml.Decode answers an empty document with io.EOF -- which, left
// alone, would fail the whole tree's declarations over a file that says
// nothing at all.
func parseSettings(f file, repo model.RepoRef) (model.RepoSettings, error) {
	var raw rawRepoSettings
	if err := decode(f, model.DeclaredSettings, &raw); err != nil {
		if errors.Is(err, io.EOF) {
			return model.RepoSettings{}, nil
		}
		return model.RepoSettings{}, err
	}
	var s model.RepoSettings
	if raw.PromptExtension != nil {
		s.PromptExtension = strings.TrimSpace(*raw.PromptExtension)
	}
	if raw.SetupCommand != nil {
		s.SetupCommand = strings.TrimSpace(*raw.SetupCommand)
	}
	for _, b := range raw.PortBranches {
		if b = strings.TrimSpace(b); b != "" {
			s.PortBranches = append(s.PortBranches, b)
		}
	}
	for dir, types := range raw.PortTypes {
		from, to, ok := strings.Cut(dir, "->")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" || from == to {
			return model.RepoSettings{}, fmt.Errorf("%s: port-types: %q is not a direction: write it <from>-><to>, "+
				"for example main->candidate", f.path, dir)
		}
		var kept []string
		for _, t := range types {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" || slices.Contains(kept, t) {
				continue
			}
			if !conventionalType.MatchString(t) {
				return model.RepoSettings{}, fmt.Errorf("%s: port-types: %s: %q is not a conventional-commit type "+
					"(fix, revert, feat, ...)", f.path, dir, t)
			}
			kept = append(kept, t)
		}
		if len(kept) == 0 {
			return model.RepoSettings{}, fmt.Errorf("%s: port-types: %s names no types: leave the direction out "+
				"to port every merge, or set port-branches to port none", f.path, dir)
		}
		if s.PortTypes == nil {
			s.PortTypes = map[string][]string{}
		}
		s.PortTypes[model.PortDirection(from, to)] = kept
	}
	return s, nil
}
