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

package intree

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
)

// hostScriptHelperEnv makes this test binary serve the host-script server
// over stdio, with its data directory in hostScriptHelperData, the way
// hostd starts `grain capability host-script` for a run: host entry.
// grainHelperEnv makes it the sandbox's grain instead, answering
// `grain host-script` the way cmd/grain-sandbox does.
const (
	hostScriptHelperEnv  = "GRAIN_INTREE_TEST_HOST_SCRIPT"
	hostScriptHelperData = "GRAIN_INTREE_TEST_HOST_SCRIPT_DATA"
	grainHelperEnv       = "GRAIN_INTREE_TEST_GRAIN"
)

// testMainHelpers are the other helper modes of this test binary, by the
// variable that selects each, registered by files a build tag may leave
// out (gchat_hostd_test.go's). Each serves and exits.
var testMainHelpers = map[string]func(){}

func TestMain(m *testing.M) {
	for env, serve := range testMainHelpers {
		if os.Getenv(env) == "1" {
			serve()
		}
	}
	if os.Getenv(grainHelperEnv) == "1" {
		if len(os.Args) < 2 || os.Args[1] != "host-script" {
			fmt.Fprintln(os.Stderr, "grain: only host-script")
			os.Exit(2)
		}
		os.Exit(hostscriptcli.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	if os.Getenv(hostScriptHelperEnv) == "1" {
		s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: os.Getenv(hostScriptHelperData)})
		if err == nil {
			err = s.Serve(context.Background(), os.Stdin, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
