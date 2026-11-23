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

package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// With no CA variables the script is the one every job had before
// container jobs were given any, byte for byte: it is what an Actions
// job's cache key is computed over.
func TestActScriptForContainerWithoutCAIsUnchanged(t *testing.T) {
	if got, want := ActScriptForContainer(".github/workflows/ci.yml", "test", "push", "", nil), ActScript(".github/workflows/ci.yml", "test", "push"); got != want {
		t.Errorf("no caEnv, no text:\n%s\nwant:\n%s", got, want)
	}
	text := "name: CI\non: push\njobs: {}\n"
	if got, want := ActScriptForContainer(".github/workflows/ci.yml", "test", "push", text, nil), ActScriptForText(".github/workflows/ci.yml", "test", "push", text); got != want {
		t.Errorf("no caEnv, with text:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(ActScript(".github/workflows/ci.yml", "test", "push"), GrainCACertPath) {
		t.Error("a job with no container is given the CA")
	}
}

func TestActScriptForContainerPassesOnlyKnownCAVariables(t *testing.T) {
	got := ActScriptForContainer(".github/workflows/ci.yml", "test", "push", "", []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "LD_PRELOAD"})
	for _, want := range []string{
		"if [ -r '" + GrainCACertPath + "' ]",
		"docker volume create act-toolcache",
		"--env 'SSL_CERT_FILE=" + ContainerCADir + "/ca-certificates.crt'",
		"--env 'NODE_EXTRA_CA_CERTS=" + ContainerCADir + "/grain-ca.crt'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"LD_PRELOAD", "REQUESTS_CA_BUNDLE"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("script names %s, which it was not asked for or does not know:\n%s", unwanted, got)
		}
	}
	// The variables are added before act is exec'd, never after.
	if strings.Index(got, "--env 'SSL_CERT_FILE") > strings.Index(got, "exec \"$@\"") {
		t.Errorf("CA variables come after exec:\n%s", got)
	}
}

// The script run for real, against a stand-in act and docker: with the
// guest holding grain's root CA, the guest's store lands in act's tool
// cache volume and act is pointed at it; without one, act is run as it
// always was and nothing is copied.
func TestActScriptForContainerCopiesTheGuestsTrustStore(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	for _, withCA := range []bool{true, false} {
		root := t.TempDir()
		guestCA := filepath.Join(root, "grain-ca.crt")
		bundle := filepath.Join(root, "ca-certificates.crt")
		guestDir := filepath.Join(root, "guest-ca")
		volume := filepath.Join(root, "volume")
		bin := filepath.Join(root, "bin")
		args := filepath.Join(root, "act-args")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if withCA {
			if err := os.WriteFile(guestCA, []byte("GRAIN ROOT\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(bundle, []byte("PUBLIC ROOTS\nGRAIN ROOT\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"act":    "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + args + "\n",
			"docker": "#!/bin/sh\nif [ \"$2\" = inspect ]; then echo " + volume + "; fi\n",
			"id":     "#!/bin/sh\necho 0\n",
		} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		script := ActScriptForContainer(".github/workflows/ci.yml", "test", "push", "", ContainerCAEnv)
		script = strings.NewReplacer(
			"'"+GrainCACertPath+"'", "'"+guestCA+"'",
			"'"+guestCABundle+"'", "'"+bundle+"'",
			"'"+ContainerCADir+"'", "'"+guestDir+"'",
		).Replace(script)
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("withCA=%v: script failed: %v\n%s\n%s", withCA, err, out, script)
		}
		got, err := os.ReadFile(args)
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(filepath.Join(volume, ".grain-ca", "ca-certificates.crt"))
		if !withCA {
			if strings.Contains(string(got), "SSL_CERT_FILE") || statErr == nil {
				t.Errorf("with no grain CA the job was given one anyway:\n%s", got)
			}
			continue
		}
		for _, want := range []string{
			"SSL_CERT_FILE=" + ContainerCADir + "/ca-certificates.crt",
			"REQUESTS_CA_BUNDLE=" + ContainerCADir + "/ca-certificates.crt",
			"CURL_CA_BUNDLE=" + ContainerCADir + "/ca-certificates.crt",
			"NODE_EXTRA_CA_CERTS=" + ContainerCADir + "/grain-ca.crt",
		} {
			if !strings.Contains(string(got), want+"\n") {
				t.Errorf("act was not given %s:\n%s", want, got)
			}
		}
		for _, dir := range []string{filepath.Join(volume, ".grain-ca"), guestDir} {
			for file, want := range map[string]string{"ca-certificates.crt": "PUBLIC ROOTS\nGRAIN ROOT\n", "grain-ca.crt": "GRAIN ROOT\n"} {
				if b, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(b) != want {
					t.Errorf("%s/%s = %q, %v; want %q", dir, file, b, err, want)
				}
			}
		}
	}
}
