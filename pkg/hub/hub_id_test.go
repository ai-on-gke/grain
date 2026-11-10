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

package hub

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hubenv"
)

// compute.hub_id (and, for a release, compute.registry_id) is the label
// value the gce provider and the reconciler stamp on and require of every
// VM and disk, which they read as ComputeConfig.RegistryID. `grain-hub
// serve` and `grain-hub validate` decode the YAML themselves rather than
// through ParseConfig, so settling the two spellings only in ParseConfig
// left hub_id ignored by the running hub: it labelled new VMs
// "grain-registry" and no longer recognised the ones labelled with its id.
func TestHubIDReachesComputeOnEveryConfigLoadPath(t *testing.T) {
	loaders := map[string]func(t *testing.T, yaml string) *Config{
		"ParseConfig": func(t *testing.T, yaml string) *Config {
			cfg, err := ParseConfig([]byte(yaml))
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			return cfg
		},
		"validate (ParseConfigWithResolver)": func(t *testing.T, yaml string) *Config {
			cfg, err := ParseConfigWithResolver([]byte(yaml), nil)
			if err != nil {
				t.Fatalf("ParseConfigWithResolver: %v", err)
			}
			return cfg
		},
		"serve (strict decode + ApplyDefaults)": func(t *testing.T, yaml string) *Config {
			var cfg Config
			if err := yamlStrictDecoder(os.ExpandEnv(yaml)).Decode(&cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			cfg.ApplyDefaults()
			return &cfg
		},
	}
	cases := []struct {
		name, lines, want string
		warnings          int
	}{
		{"hub_id", "  hub_id: prod-a\n", "prod-a", 0},
		{"registry_id", "  registry_id: prod-old\n", "prod-old", 1},
		{"both", "  registry_id: prod-old\n  hub_id: prod-a\n", "prod-a", 0},
	}
	prevLog := hubenv.Log
	t.Cleanup(func() { hubenv.Log = prevLog; hubenv.ResetForTest() })
	for loader, load := range loaders {
		for _, tc := range cases {
			t.Run(loader+"/"+tc.name, func(t *testing.T) {
				var buf bytes.Buffer
				hubenv.ResetForTest()
				hubenv.Log = func() io.Writer { return &buf }
				yaml := strings.Replace(validTestYAML(), "  provider: gce\n", "  provider: gce\n"+tc.lines, 1)
				cfg := load(t, yaml)
				if got := cfg.Compute.RegistryID; got != tc.want {
					t.Fatalf("Compute.RegistryID = %q, want %q", got, tc.want)
				}
				if n := strings.Count(buf.String(), "compute.registry_id is deprecated"); n != tc.warnings {
					t.Fatalf("got %d compute.registry_id deprecation warnings, want %d:\n%s", n, tc.warnings, buf.String())
				}
			})
		}
	}
}
