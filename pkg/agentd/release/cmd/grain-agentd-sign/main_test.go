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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignCLIKeygenRotateSignAndVerify(t *testing.T) {
	dir := t.TempDir()
	privA := filepath.Join(dir, "a.key")
	pubA := filepath.Join(dir, "a.pub")
	privB := filepath.Join(dir, "b.key")
	pubB := filepath.Join(dir, "b.pub")
	chain := filepath.Join(dir, "rotations.json")
	bin := filepath.Join(dir, "grain-agentd-linux-amd64")
	mf := bin + ".manifest.json"

	if err := os.WriteFile(bin, []byte("static-binary-payload"), 0o755); err != nil {
		t.Fatal(err)
	}

	mustRun := func(args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("run(%v) = %d\nstdout: %s\nstderr: %s", args, code, out.String(), errOut.String())
		}
		return out.String()
	}

	mustRun("keygen", "-private-key", privA, "-public-key", pubA)
	mustRun("keygen", "-private-key", privB, "-public-key", pubB)
	mustRun("rotate", "-old-private-key", privA, "-new-public-key", pubB, "-chain", chain)
	mustRun("manifest", "-binary", bin, "-platform", "linux/amd64", "-version", "v3.5.0", "-out", mf)
	mustRun("sign", "-manifest", mf, "-signing-key", privB, "-rotations", chain)
	out := mustRun("verify", "-manifest", mf, "-binary", bin, "-public-key", pubA)
	if !strings.Contains(out, "verified") {
		t.Errorf("verify output = %q", out)
	}
}
