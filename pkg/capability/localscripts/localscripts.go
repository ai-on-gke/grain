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

// Package localscripts migrates "local-scripts:<name>" declarations and
// grants onto the host-script capability server (docs/local-scripts.md).
package localscripts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/localexec"
)

// CapabilityPrefix starts every local-scripts capability ID.
const CapabilityPrefix = "local-scripts:"

// DeclarationFile is a declaration's file name inside its folder.
const DeclarationFile = "local-scripts.yaml"

// StateRepoDir is the state repository's directory of declarations.
const StateRepoDir = "local-scripts"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Name returns the declaration a capability ID names, and whether id is
// a local-scripts capability with a well-formed name at all.
func Name(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, CapabilityPrefix)
	if !ok || !namePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

// CapabilityName is the capability that grants the declaration name.
func CapabilityName(name string) string { return CapabilityPrefix + name }

// Config says where declarations are found.
type Config struct {
	// StateDir is the node host's checkout of the deployment's state
	// repository; "" when this host has none.
	StateDir string
	// Host maps a declaration name to the absolute path of a
	// declaration file a host-managed installation keeps. It wins over
	// the state repository's declaration of the same name.
	Host map[string]string
}

// Path is the declaration file for name: Host's when it names one,
// otherwise the state repository's. It is an error for neither to hold
// a file.
func (c Config) Path(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a local-scripts declaration name", name)
	}
	if p, ok := c.Host[name]; ok && p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("local-scripts %q (%s): %w", name, p, err)
		}
		return p, nil
	}
	if c.StateDir != "" {
		p := filepath.Join(c.StateDir, StateRepoDir, name, DeclarationFile)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no local-scripts declaration %q is configured on this host", name)
}

// ParseHost parses one -local-scripts NAME=/absolute/path/local-scripts.yaml
// flag into Host.
func (c *Config) ParseHost(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || !namePattern.MatchString(name) {
		return fmt.Errorf("-local-scripts %q: want NAME=/absolute/path/%s", value, DeclarationFile)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("-local-scripts %q: %q is not an absolute path", value, path)
	}
	if c.Host == nil {
		c.Host = map[string]string{}
	}
	c.Host[name] = path
	return nil
}

// Credentials maps each secret name a declaration references to its
// resolved value.
type Credentials map[string]string

// ScriptEnv turns a snapshot's env entries (variable -> secret) into
// localexec.Config.Env (script -> variable -> value). A secret creds
// does not hold is an error: the script would run without it.
func ScriptEnv(scripts []localexec.Snapshotted, creds Credentials) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	var errs []error
	for _, s := range scripts {
		for variable, secret := range s.Env {
			value, ok := creds[secret]
			if !ok {
				errs = append(errs, fmt.Errorf("script %s: secret %q (for %s) was not delivered", s.Name, secret, variable))
				continue
			}
			if out[s.Name] == nil {
				out[s.Name] = map[string]string{}
			}
			out[s.Name][variable] = value
		}
	}
	return out, errors.Join(errs...)
}

// Open snapshots the declaration at each path and returns one Executor
// for each, serving its scripts with creds delivered per script.
func Open(paths []string, creds Credentials, approver localexec.Approver) ([]*localexec.Executor, error) {
	var out []*localexec.Executor
	closeAll := func() {
		for _, e := range out {
			e.Close()
		}
	}
	seen := map[string]string{}
	for _, path := range paths {
		d, err := localexec.Load(path)
		if err != nil {
			closeAll()
			return nil, err
		}
		scripts, err := d.Snapshot()
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("local scripts %s: %w", path, err)
		}
		for _, s := range scripts {
			if other, ok := seen[s.Tool]; ok {
				closeAll()
				return nil, fmt.Errorf("local scripts: %s is served by both %s and %s", s.Tool, other, path)
			}
			seen[s.Tool] = path
		}
		env, err := ScriptEnv(scripts, creds)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("local scripts %s: %w", path, err)
		}
		out = append(out, localexec.New(localexec.Config{
			Scripts:   scripts,
			Workdir:   d.WorkdirPath(),
			Environ:   localexec.BaseEnviron(os.Environ()),
			Env:       env,
			OutputCap: d.Cap(),
			Approver:  approver,
		}))
	}
	return out, nil
}
