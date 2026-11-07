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

package deploy

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRegistrySandboxScriptsAndTerraform(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")

	var scripts []string
	err := filepath.WalkDir(sandboxDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".sh") {
			scripts = append(scripts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", sandboxDir, err)
	}
	if len(scripts) == 0 {
		t.Fatalf("no shell scripts found under %s", sandboxDir)
	}

	jqSecretArg := regexp.MustCompile(`\bjq\b[^\n]*--arg\s+secret\b`)
	curlInlineBearer := regexp.MustCompile(`-H\s+["']Authorization:\s*Bearer\s+\$`)

	for _, script := range scripts {
		if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", script, err, out)
		}
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("reading %s: %v", script, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if jqSecretArg.MatchString(line) {
				t.Errorf("%s:%d passes secret in jq argv (--arg secret); pipe via stdin instead: %s", script, i+1, trimmed)
			}
			if curlInlineBearer.MatchString(line) {
				t.Errorf("%s:%d passes bearer token in curl argv (-H \"Authorization: Bearer ...\"); use -H @<(...) instead: %s", script, i+1, trimmed)
			}
		}
	}

	if tfPath, err := exec.LookPath("terraform"); err == nil {
		if out, err := exec.Command(tfPath, "-chdir="+sandboxDir, "fmt", "-check", "-diff").CombinedOutput(); err != nil {
			t.Errorf("terraform fmt -check on %s failed: %v\n%s", sandboxDir, err, out)
		}
	}
}
