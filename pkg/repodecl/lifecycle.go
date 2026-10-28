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
	"io/fs"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
)

// This file reads `.grain/lifecycle.yml`: how a repository says its own
// tasks move (docs/task-lifecycle-sandbox.md).
//
// It is deliberately not part of Load. Templates, suites and schedules
// become rows a sync keeps in step with a commit; a lifecycle becomes an
// image digest and a serving sandbox, resolved by whoever owns those,
// and folding it into model.RepoDeclarations would put a thing with a
// registry and a VM behind it into the same bag as three kinds of row.
// What it does share with Load is the format's whole manner: one
// decoder with KnownFields set, so a misspelt key is an error naming the
// key, and every field that is deliberately absent is a reserved entry
// whose refusal says why.

// LoadLifecycle reads the one lifecycle declaration a tree holds.
//
// It returns the declaration and the path it was read from. A repository
// with no `.grain/lifecycle.yml` declares no lifecycle and is not an
// error -- it gets the deployment's default, which is what every
// repository gets today; the zero LifecycleDeclaration reports
// Declared() false.
//
// repo is not read from the file, and that is the point: it decides
// which package namespace `image:` may name, and a file that could
// choose its own would be a file that could point at another
// repository's lifecycle image.
func LoadLifecycle(tree fs.FS, repo model.RepoRef) (model.LifecycleDeclaration, string, error) {
	found, err := lifecycleFile(tree)
	if err != nil {
		return model.LifecycleDeclaration{}, "", err
	}
	if found == nil {
		return model.LifecycleDeclaration{}, "", nil
	}
	declared, err := parseLifecycle(*found, repo)
	if err != nil {
		return model.LifecycleDeclaration{}, found.path, err
	}
	return declared, found.path, nil
}

// lifecycleFile finds the declaration, refusing the case where both
// spellings are present rather than picking one: two files, one
// identity, with whichever was read last deciding how a repository's
// tasks behave.
func lifecycleFile(tree fs.FS) (*file, error) {
	var found []file
	for _, path := range []string{model.LifecycleDeclarationPath, model.LifecycleDeclarationAltPath} {
		text, err := fs.ReadFile(tree, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		found = append(found, file{path: path, stem: "lifecycle", text: text})
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return &found[0], nil
	default:
		return nil, fmt.Errorf("%s: %s already declares this repository's lifecycle -- a repository has "+
			"exactly one answer to how its tasks move, so there is one file to write it in",
			model.LifecycleDeclarationAltPath, model.LifecycleDeclarationPath)
	}
}

type rawLifecycle struct {
	Name  string          `yaml:"name"`
	Image string          `yaml:"image"`
	Serve rawLifecycleSrv `yaml:"serve"`

	Build   reserved `yaml:"build"`
	Capable reserved `yaml:"capabilities"`
	Secrets reserved `yaml:"secrets"`
	Repo    reserved `yaml:"repo"`
	Target  reserved `yaml:"target"`
	Reads   reserved `yaml:"reads"`
	Writes  reserved `yaml:"writes"`
	Grants  reserved `yaml:"grants"`
}

type rawLifecycleSrv struct {
	Run              string              `yaml:"run"`
	WorkingDirectory string              `yaml:"working-directory"`
	Listen           string              `yaml:"listen"`
	Env              map[string]string   `yaml:"env"`
	ReadyTimeout     *int                `yaml:"ready-timeout-seconds"`
	RequestTimeout   *int                `yaml:"request-timeout-ms"`
	Sandbox          rawLifecycleSandbox `yaml:"sandbox"`

	Network  reserved `yaml:"network"`
	Checkout reserved `yaml:"checkout"`
	Capable  reserved `yaml:"capabilities"`
	Secrets  reserved `yaml:"secrets"`
}

type rawLifecycleSandbox struct {
	CPUs     int `yaml:"cpus"`
	MemoryMB int `yaml:"memory-mb"`

	Network reserved `yaml:"network"`
	DiskGB  reserved `yaml:"disk-gb"`
}

func parseLifecycle(f file, repo model.RepoRef) (model.LifecycleDeclaration, error) {
	var raw rawLifecycle
	if err := decodeInto(f, &raw); err != nil {
		return model.LifecycleDeclaration{}, err
	}
	if raw.Build.set {
		return model.LifecycleDeclaration{}, fmt.Errorf("%s: build: grain does not build a lifecycle image -- "+
			"the build is an ordinary `.grain/workflows` release job, which already has a sandbox, a log, a "+
			"live view and a publishing credential scoped to this repository's own packages. "+
			"See docs/task-lifecycle-sandbox.md, \"The build is a release job\"", f.path)
	}
	if err := refuseLifecycleReserved(f, map[string]*reserved{
		"capabilities":          &raw.Capable,
		"secrets":               &raw.Secrets,
		"repo":                  &raw.Repo,
		"target":                &raw.Target,
		"reads":                 &raw.Reads,
		"writes":                &raw.Writes,
		"grants":                &raw.Grants,
		"serve.network":         &raw.Serve.Network,
		"serve.checkout":        &raw.Serve.Checkout,
		"serve.capabilities":    &raw.Serve.Capable,
		"serve.secrets":         &raw.Serve.Secrets,
		"serve.sandbox.network": &raw.Serve.Sandbox.Network,
		"serve.sandbox.disk-gb": &raw.Serve.Sandbox.DiskGB,
	}); err != nil {
		return model.LifecycleDeclaration{}, err
	}

	run := strings.TrimSpace(raw.Serve.Run)
	if run == "" {
		return model.LifecycleDeclaration{}, fmt.Errorf("%s: a lifecycle needs serve.run -- it is the command "+
			"that starts the definition's HTTP server, and there is nothing else in this file that could start it",
			f.path)
	}

	image := strings.TrimSpace(raw.Image)
	if image != "" {
		if err := model.ValidateLifecycleImage(image, repo); err != nil {
			return model.LifecycleDeclaration{}, fmt.Errorf("%s: image: %w", f.path, err)
		}
	}

	listen := strings.TrimSpace(raw.Serve.Listen)
	if listen == "" {
		listen = model.DefaultLifecycleListen
	}
	if err := model.ValidateLifecycleListen(listen); err != nil {
		return model.LifecycleDeclaration{}, fmt.Errorf("%s: serve.listen: %w", f.path, err)
	}

	ready := model.DefaultLifecycleReadyTimeout
	if raw.Serve.ReadyTimeout != nil {
		if *raw.Serve.ReadyTimeout <= 0 {
			return model.LifecycleDeclaration{}, fmt.Errorf(
				"%s: serve.ready-timeout-seconds: %d is not a timeout -- leave it out for the default of %s",
				f.path, *raw.Serve.ReadyTimeout, model.DefaultLifecycleReadyTimeout)
		}
		ready = time.Duration(*raw.Serve.ReadyTimeout) * time.Second
	}

	request := model.DefaultLifecycleRequestTimeout
	if raw.Serve.RequestTimeout != nil {
		if *raw.Serve.RequestTimeout <= 0 {
			return model.LifecycleDeclaration{}, fmt.Errorf(
				"%s: serve.request-timeout-ms: %d is not a timeout -- leave it out for the default of %s",
				f.path, *raw.Serve.RequestTimeout, model.DefaultLifecycleRequestTimeout)
		}
		request = time.Duration(*raw.Serve.RequestTimeout) * time.Millisecond
		if request > model.MaxLifecycleRequestTimeout {
			return model.LifecycleDeclaration{}, fmt.Errorf(
				"%s: serve.request-timeout-ms: %d exceeds the %d ms ceiling -- a reducer is a pure function "+
					"of one state and one event, and grain holds that instance's lock until it answers",
				f.path, *raw.Serve.RequestTimeout, model.MaxLifecycleRequestTimeout.Milliseconds())
		}
	}

	if raw.Serve.Sandbox.CPUs < 0 || raw.Serve.Sandbox.MemoryMB < 0 {
		return model.LifecycleDeclaration{}, fmt.Errorf("%s: serve.sandbox: cpus and memory-mb cannot be negative", f.path)
	}
	for name := range raw.Serve.Env {
		if strings.TrimSpace(name) == "" {
			return model.LifecycleDeclaration{}, fmt.Errorf("%s: serve.env: an environment variable needs a name", f.path)
		}
		if name == model.LifecycleTokenEnv || name == model.LifecycleListenEnv {
			return model.LifecycleDeclaration{}, fmt.Errorf("%s: serve.env: %s is grain's -- it carries the "+
				"per-sandbox bearer token and the resolved listen address into the serve command, and a "+
				"declaration that set it would be overwriting the only thing authenticating its own reducer",
				f.path, name)
		}
	}

	return model.LifecycleDeclaration{
		Name:  strings.TrimSpace(raw.Name),
		Image: image,
		Serve: model.LifecycleServe{
			Run:              run,
			WorkingDirectory: strings.TrimSpace(raw.Serve.WorkingDirectory),
			Listen:           listen,
			Env:              raw.Serve.Env,
			ReadyTimeout:     ready,
			RequestTimeout:   request,
			Sandbox: model.LifecycleSandbox{
				CPUs:     raw.Serve.Sandbox.CPUs,
				MemoryMB: raw.Serve.Sandbox.MemoryMB,
			},
		},
	}, nil
}

// reserved is a field a declaration may not set, present in the raw
// struct only so that setting it is refused with its own reason.
//
// The three things grain's settings also hold -- templates, suites,
// schedules -- refuse their keys from pkg/declschema instead, one list
// for both contexts (parse.go's preamble). A lifecycle has no such
// second context: there is no lifecycle row, no lifecycle table and
// nothing of it in the settings export, so there is no other copy of
// this format for it to drift from, and its refusals stay where the
// fields they refuse are. It also refuses keys nested two levels down
// (`serve.sandbox.network`), which declschema's flat top-level keys do
// not describe.
type reserved struct{ set bool }

func (r *reserved) UnmarshalYAML(*yaml.Node) error {
	r.set = true
	return nil
}

// refuseLifecycleReserved is the one explanation this file's reserved
// fields share, said once. What a lifecycle declaration must not reach
// for is not what a template must not reach for, so the reason differs
// from declschema's.
func refuseLifecycleReserved(f file, fields map[string]*reserved) error {
	var named []string
	for name, field := range fields {
		if field.set {
			named = append(named, name)
		}
	}
	if len(named) == 0 {
		return nil
	}
	sort.Strings(named)
	return fmt.Errorf("%s: %s: a repository's lifecycle declaration says how its tasks move, never what the "+
		"reducer may touch. The serving sandbox has no network, no checkout, no credential and no capability, "+
		"and no file in a repository every dispatched agent can rewrite gets to widen that -- it is precisely "+
		"what makes it safe for the definition to live here at all",
		f.path, strings.Join(named, ", "))
}
