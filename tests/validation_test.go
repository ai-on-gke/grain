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

package tests

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	planvalidate "github.com/ai-on-gke/ai-factory/tool/cmd/tool/plan/validate"
	specvalidate "github.com/ai-on-gke/ai-factory/tool/cmd/tool/spec/validate"
)

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "spec.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (spec.yaml) starting from %s", dir)
		}
		dir = parent
	}
}

func TestSpecs(t *testing.T) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatal(err)
	}

	specsDir := filepath.Join(projectRoot, "specs")
	err = filepath.Walk(specsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		if info.Name() == "README.md" {
			return nil
		}

		relPath, err := filepath.Rel(specsDir, path)
		if err != nil {
			return err
		}

		t.Run(relPath, func(t *testing.T) {
			origWd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(projectRoot); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(origWd); err != nil {
					t.Fatal(err)
				}
			}()

			var out bytes.Buffer
			specvalidate.Cmd.SetOut(&out)
			specvalidate.Cmd.SetArgs([]string{relPath})

			if err := specvalidate.Cmd.Execute(); err != nil {
				t.Errorf("Validation failed for spec %s: %v\nOutput:\n%s", relPath, err, out.String())
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPlans(t *testing.T) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatal(err)
	}

	plansDir := filepath.Join(projectRoot, "plans")
	err = filepath.Walk(plansDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() != "plan.yaml" {
			return nil
		}

		planDir := filepath.Dir(path)
		planName, err := filepath.Rel(plansDir, planDir)
		if err != nil {
			return err
		}

		t.Run(planName, func(t *testing.T) {
			origWd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(projectRoot); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(origWd); err != nil {
					t.Fatal(err)
				}
			}()

			var out bytes.Buffer
			planvalidate.Cmd.SetOut(&out)
			planvalidate.Cmd.SetArgs([]string{planName})

			if err := planvalidate.Cmd.Execute(); err != nil {
				t.Errorf("Validation failed for plan %s: %v\nOutput:\n%s", planName, err, out.String())
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
