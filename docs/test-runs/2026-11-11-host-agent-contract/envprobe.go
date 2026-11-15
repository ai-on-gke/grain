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

// envprobe: §9 item 2. Run as the host's user against its grain-agentd's
// local socket, it starts the codex entry (env: ["*"]) once per variable
// and prints whether the host admitted it, so the frozen deny set is
// checked against a real agentd rather than a unit test.
//
//	go build -o /var/tmp/hac/envprobe docs/test-runs/2026-11-11-host-agent-contract/envprobe.go
//	sudo -u walkhost /var/tmp/hac/envprobe -socket /run/user-walkhost/grain-agentd/agentd.sock
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func main() {
	socket := flag.String("socket", "", "grain-agentd's local socket")
	cli := flag.String("cli", "codex", "the clis: entry to start")
	flag.Parse()
	c := hostd.Client{Socket: *socket, Build: "envprobe"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h, err := c.Hello(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hello:", err)
		os.Exit(1)
	}
	fmt.Printf("host build %s protocol %d env_open=%v logins=%v login_targets=%v\n",
		h.Build, h.Protocol, h.Has(hostd.FeatureEnvOpen), h.Logins, h.LoginTargets)
	vars := []string{
		// daemon-supplied variables "*" should admit
		"GRAIN_WALK_FROM_DAEMON=1", "OPENAI_API_KEY=sk-walk", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		// the frozen deny set
		"PATH=/tmp/evil", "HOME=/tmp", "USER=root", "LD_PRELOAD=/tmp/evil.so", "LD_LIBRARY_PATH=/tmp",
		"BASH_ENV=/tmp/evil.sh", "ENV=/tmp/evil.sh", "NODE_OPTIONS=--require=/tmp/evil.js",
		"PYTHONSTARTUP=/tmp/evil.py", "PYTHONPATH=/tmp", "PERL5OPT=-Mevil", "RUBYOPT=-revil",
		// loader/interpreter variables outside the frozen set (reported, not asserted)
		"GCONV_PATH=/tmp", "NODE_PATH=/tmp", "PYTHONHOME=/tmp", "PERL5LIB=/tmp", "RUBYLIB=/tmp",
		"JAVA_TOOL_OPTIONS=-javaagent:/tmp/evil.jar", "GIT_CONFIG_PARAMETERS='core.pager=sh'",
		"PROMPT_COMMAND=evil", "SHELLOPTS=xtrace", "PS4=$(evil)",
	}
	for _, kv := range vars {
		var out, errb bytes.Buffer
		_, err := c.Run(ctx, hostd.Request{Op: hostd.OpStart, Kind: hostd.KindCLI, Name: *cli,
			Args: []string{"--version"}, Env: []string{kv}}, nil, &out, &errb)
		name, _, _ := strings.Cut(kv, "=")
		if err != nil {
			fmt.Printf("%-28s refused: %v\n", name, err)
		} else {
			fmt.Printf("%-28s admitted\n", name)
		}
	}
}
