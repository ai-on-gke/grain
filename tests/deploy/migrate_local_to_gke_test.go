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
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// scripts/migrate-local-to-gke.sh carries its own suite as shell, beside
// it, because what it drives is shell against stubbed kubectl and
// systemctl: scripts/migrate-local-to-gke_test.sh. This is what runs it
// in CI.
func TestMigrateLocalToGKEShellSuite(t *testing.T) {
	for _, tool := range []string{"bash", "tar", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "migrate-local-to-gke_test.sh"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scripts/migrate-local-to-gke_test.sh: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "passed") || strings.Contains(string(out), "\nFAIL ") {
		t.Fatalf("scripts/migrate-local-to-gke_test.sh did not report every case passing:\n%s", out)
	}
	t.Logf("scripts/migrate-local-to-gke_test.sh:\n%s", out)
}

// renderedMigrationManifest is what the migration applies, decoded the
// way kubectl would: every document has to parse.
func renderedMigrationManifest(t *testing.T) ([]k8sObject, []*yaml.Node) {
	t.Helper()
	root := repoRoot(t)
	scratch := t.TempDir()
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "migrate-local-to-gke.sh"), "--print-manifest",
		"--image=ghcr.io/bwsalmon/grain/grain:v9.9.9", "--sandbox-image=ghcr.io/bwsalmon/grain/guest:v9.9.9",
		"--target-repo=ai-on-gke/grain", "--node-selector=cloud.google.com/gke-nodepool=granule",
		"--data-dir="+scratch)
	cmd.Env = append(os.Environ(), "GRAIN_ETC_DIR="+scratch, "GRAIN_SYSTEMD_DIR="+scratch)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("migrate-local-to-gke.sh --print-manifest: %v", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	var objects []k8sObject
	var nodes []*yaml.Node
	for i := 0; ; i++ {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the rendered manifest's document %d does not parse: %v\n%s", i, err, out)
		}
		if node.Kind == 0 {
			continue
		}
		var obj k8sObject
		if err := node.Decode(&obj); err != nil {
			t.Fatalf("document %d is not a Kubernetes object: %v", i, err)
		}
		objects = append(objects, obj)
		nodes = append(nodes, &node)
	}
	return objects, nodes
}

// The manifest the migration applies is grain-granule.yaml with its
// CHANGEMEs filled in and the shared hostd/model-server token wired to
// the flags that take it -- and still the same objects, so nothing the
// manifest's own tests hold it to is lost in the rendering.
func TestMigrateLocalToGKERendersAControllerHostdCanDialInTo(t *testing.T) {
	objects, nodes := renderedMigrationManifest(t)
	shipped, _ := manifestDocsForFile(t, "grain-granule.yaml")
	if len(objects) != len(shipped) {
		t.Fatalf("rendered %d objects, grain-granule.yaml ships %d", len(objects), len(shipped))
	}
	for i := range objects {
		if objects[i] != shipped[i] {
			t.Errorf("object %d is %+v, grain-granule.yaml's is %+v", i, objects[i], shipped[i])
		}
	}

	dep := grainDeploymentFromDocs(t, objects, nodes)
	c := grainContainer(t, dep)
	if c.Image != "ghcr.io/bwsalmon/grain/grain:v9.9.9" {
		t.Errorf("the controller runs %q, not --image", c.Image)
	}
	flags := daemonArgs(t, c)
	for name, want := range map[string]string{
		"k8s-granule-image":   "ghcr.io/bwsalmon/grain/guest:v9.9.9",
		"default-target-repo": "ai-on-gke/grain",
		"k8s-node-selector":   "cloud.google.com/gke-nodepool=granule",
		"hostd-token-file":    "/etc/grain-secrets/hostd-token",
		"executor-addr":       ":4243",
		"executor-token-file": "/etc/grain-secrets/hostd-token",
	} {
		if got := flags[name]; got != want {
			t.Errorf("-%s=%q, want %q", name, got, want)
		}
	}
	for name, value := range flags {
		if strings.Contains(value, "CHANGEME") {
			t.Errorf("-%s is still %q", name, value)
		}
	}

	mounted := false
	for _, m := range c.VolumeMounts {
		if m.Name == "migration" && m.MountPath == "/etc/grain-secrets" {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("the controller does not mount the migration Secret at /etc/grain-secrets: %+v", c.VolumeMounts)
	}
	for _, ic := range dep.Spec.Template.Spec.InitContainers {
		for _, m := range ic.VolumeMounts {
			if m.Name == "migration" {
				t.Errorf("init container %s mounts the migration Secret too", ic.Name)
			}
		}
	}

	var secretVolumes struct {
		Spec struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						Name   string `yaml:"name"`
						Secret struct {
							SecretName string `yaml:"secretName"`
						} `yaml:"secret"`
					} `yaml:"volumes"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	for i, obj := range objects {
		if obj.Kind == "Deployment" {
			if err := nodes[i].Decode(&secretVolumes); err != nil {
				t.Fatal(err)
			}
		}
	}
	found := false
	for _, v := range secretVolumes.Spec.Template.Spec.Volumes {
		if v.Name == "migration" && v.Secret.SecretName == "grain-migration" {
			found = true
		}
	}
	if !found {
		t.Errorf("no migration volume from Secret/grain-migration: %+v", secretVolumes.Spec.Template.Spec.Volumes)
	}
}

// Every daemon flag the rendered manifest adds is one `grain daemon`
// takes: a misspelled one is a pod that exits at start.
func TestMigrateLocalToGKEPassesFlagsTheDaemonTakes(t *testing.T) {
	help := read(t, "pkg", "cli", "testdata", "daemon_help.golden")
	for _, flag := range []string{"-hostd-token-file", "-executor-addr", "-executor-token-file", "-k8s-granule-image",
		"-default-target-repo", "-k8s-node-selector"} {
		if !strings.Contains(help, "  "+flag+" ") && !strings.Contains(help, "  "+flag+"\n") {
			t.Errorf("grain daemon -h lists no %s", flag)
		}
	}
	script := read(t, "scripts", "migrate-local-to-gke.sh")
	hostd := read(t, "pkg", "cli", "hostd.go")
	for _, flag := range []string{"server", "token-file"} {
		if !strings.Contains(script, " -"+flag+" ") {
			t.Errorf("the hostd drop-in passes no -%s", flag)
		}
		if !strings.Contains(hostd, `fs.String("`+flag+`"`) {
			t.Errorf("grain hostd takes no -%s", flag)
		}
	}
}
