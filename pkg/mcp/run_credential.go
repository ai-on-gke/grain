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

package mcp

import (
	"context"
	"slices"
	"strings"
)

// RunCredentialEnv names the environment variable a run's `grain
// mcpserver` finds its run credential in: the bearer token, bound to the
// run's task and run ids, it reaches the daemon with instead of the
// access code (pkg/ui's run_credential.go). Handed over in the
// environment rather than on the server's argv, which every user on the
// host can read.
const RunCredentialEnv = "GRAIN_RUN_CREDENTIAL"

// RunCredentialEnvEntry is credential as a NAME=VALUE environment entry,
// or nil for no credential.
func RunCredentialEnvEntry(credential string) []string {
	if credential == "" {
		return nil
	}
	return []string{RunCredentialEnv + "=" + credential}
}

type serverEnvKey struct{}

// WithServerEnv returns ctx carrying env (NAME=VALUE entries), which
// StartCommandHTTP adds to the environment of the server it starts.
func WithServerEnv(ctx context.Context, env ...string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	all := append(slices.Clone(serverEnvFrom(ctx)), env...)
	return context.WithValue(ctx, serverEnvKey{}, all)
}

func serverEnvFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	env, _ := ctx.Value(serverEnvKey{}).([]string)
	return env
}

// ServerEnvMap is what WithServerEnv put on ctx as the name-to-value map
// an MCP config file's "env" for a server it forks takes, nil for
// nothing.
func ServerEnvMap(ctx context.Context) map[string]string {
	env := serverEnvFrom(ctx)
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			out[name] = value
		}
	}
	return out
}
