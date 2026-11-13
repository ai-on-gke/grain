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

//go:build ignore

// envprobe: §11 item 1. Run as the host's user against its grain-agentd
// (or `grain host`) socket. It prints the host's Hello.Features, then
// starts the named entry once per variable with that one variable set
// (-kind cli or capability), and reports whether the host refused it.
// An admitted start runs the stub, which writes its environment to
// -stub-log; the probe then checks the child saw the variable (admitted)
// and that no child ran at all for a refused one.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func main() {
	socket := flag.String("socket", "", "the host agent's local socket")
	name := flag.String("name", "claude", "the allowlist entry to start")
	kind := flag.String("kind", "cli", "cli or capability")
	stage := flag.String("stage", "", "a directory under the entry's paths_under, for path-valued variables")
	stubLog := flag.String("stub-log", "", "the directory the stub writes its environment to (GRAIN_STUB_LOG)")
	flag.Parse()
	c := hostd.Client{Socket: *socket, Build: "envprobe"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := c.Hello(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hello:", err)
		os.Exit(1)
	}
	fmt.Printf("host build %s protocol %d env_open=%v env_deny_v2=%v\nfeatures: %v\n",
		h.Build, h.Protocol, h.Has(hostd.FeatureEnvOpen), h.Has(hostd.FeatureEnvDenyV2), h.Features)
	k := hostd.KindCLI
	if *kind == "capability" {
		k = hostd.KindCapability
	}
	vars := []string{
		// Ordinary: must pass under env: ["*"].
		"GRAIN_WALK_ORDINARY=hello",
		// env_deny_v2, values that are not path-like (paths_under never applies).
		"JAVA_TOOL_OPTIONS=-javaagent:/tmp/x.jar", "JDK_JAVA_OPTIONS=-Dx=1", "_JAVA_OPTIONS=-Dx=1",
		"GIT_CONFIG_PARAMETERS='core.pager=sh -c id'", "GIT_CONFIG_COUNT=1", "GIT_SSH_COMMAND=id",
		"GIT_ASKPASS=id", "PROMPT_COMMAND=id", "SHELLOPTS=xtrace", "BASHOPTS=extdebug", "PS4=$(id)",
		"PYTHONWARNINGS=ignore::x.y", "PERL5DB=BEGIN{}", "LUA_INIT=os.execute('id')", "LUA_INIT_5_4=x",
		"LUA_PATH=?.lua", "LUA_CPATH_5_3=?.so", "DYLD_INSERT_LIBRARIES=x.dylib",
		// env_deny_v2, path-valued: under paths_under, which v1 admitted.
		"NODE_PATH=" + *stage, "PYTHONHOME=" + *stage, "PYTHONUSERBASE=" + *stage, "PERL5LIB=" + *stage,
		"PERLLIB=" + *stage, "RUBYLIB=" + *stage, "GCONV_PATH=" + *stage, "CLASSPATH=" + *stage,
		"ZDOTDIR=" + *stage, "XDG_CONFIG_HOME=" + *stage, "OPENSSL_CONF=" + filepath.Join(*stage, "openssl.cnf"),
		"OPENSSL_ENGINES=" + *stage, "OPENSSL_MODULES=" + *stage, "GIT_EXEC_PATH=" + *stage,
		// The frozen v1 set, for comparison.
		"LD_PRELOAD=/tmp/x.so", "BASH_ENV=/tmp/x.sh", "NODE_OPTIONS=--require=/tmp/x.js", "PATH=/tmp",
	}
	refusedN, admittedN, bad := 0, 0, 0
	for _, kv := range vars {
		before, _ := filepath.Glob(filepath.Join(*stubLog, "*.txt"))
		var out, errb bytes.Buffer
		env := []string{kv}
		if *stubLog != "" {
			env = append(env, "GRAIN_STUB_LOG="+*stubLog)
		}
		var args []string // a capabilities: entry takes no arguments from the daemon
		if k == hostd.KindCLI {
			args = []string{"envprobe"}
		}
		_, err := c.Run(ctx, hostd.Request{Op: hostd.OpStart, Kind: k, Name: *name,
			Args: args, Env: env}, nil, &out, &errb)
		n, _, _ := strings.Cut(kv, "=")
		after, _ := filepath.Glob(filepath.Join(*stubLog, "*.txt"))
		ran := len(after) > len(before)
		saw := false
		if ran {
			for _, f := range after {
				if contains(before, f) {
					continue
				}
				b, _ := os.ReadFile(f)
				saw = bytes.Contains(b, []byte("\n"+n+"="))
			}
		}
		switch {
		case err != nil && !ran:
			refusedN++
			fmt.Printf("%-24s refused, no child: %v\n", n, err)
		case err == nil && saw:
			admittedN++
			fmt.Printf("%-24s admitted, child saw it\n", n)
		default:
			bad++
			fmt.Printf("%-24s UNEXPECTED err=%v ran=%v saw=%v\n", n, err, ran, saw)
		}
	}
	fmt.Printf("summary: %d refused, %d admitted, %d unexpected\n", refusedN, admittedN, bad)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
