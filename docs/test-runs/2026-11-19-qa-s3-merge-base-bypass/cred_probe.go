//go:build ignore

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

// cred_probe exercises checks.OpenMirrorWorkspace against a live ghsim HTTP
// remote with embedded URL userinfo (both successful shallow->deepened
// merge-base resolution and missing-base ErrBaseResolutionFailed) while
// wrapping `git` to log every argv invoked and verifying zero credentials leak
// into argv or returned error messages.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/qa/checks"
)

func main() {
	remoteURL := flag.String("remote-url", "", "HTTP remote URL with embedded userinfo")
	baseBranch := flag.String("base", "main", "target base branch")
	headBranch := flag.String("head", "", "head branch to evaluate")
	missingBase := flag.String("missing-base", "deleted-base-branch", "non-existent base branch to trigger ErrBaseResolutionFailed")
	secret := flag.String("secret", "s3cr3t-w4lk-t0k3n", "embedded secret password to check for leakage")
	cacheDir := flag.String("cache-dir", "", "mirror cache root")
	argvLog := flag.String("argv-log", "", "file written by wrapped git recording argv")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Successful deepened merge-base resolution with embedded URL credentials.
	ws, err := checks.OpenMirrorWorkspace(ctx, *cacheDir, *remoteURL, "", *baseBranch, *headBranch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenMirrorWorkspace(valid) failed: %v\n", err)
		os.Exit(1)
	}
	diff, err := ws.BaseHeadDiff(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BaseHeadDiff failed: %v\n", err)
		os.Exit(1)
	}

	// 2. Missing base branch must return checks.ErrBaseResolutionFailed with zero leaked creds.
	_, missErr := checks.OpenMirrorWorkspace(ctx, filepath.Join(*cacheDir, "miss"), *remoteURL, "", *missingBase, *headBranch)
	isBaseResErr := errors.Is(missErr, checks.ErrBaseResolutionFailed)
	missErrStr := ""
	if missErr != nil {
		missErrStr = missErr.Error()
	}

	// 3. Also test username-only PAT style URL (http://<secret>@host/...).
	userOnlyURL := strings.Replace(*remoteURL, "botuser:"+*secret+"@", *secret+"@", 1)
	_, userOnlyErr := checks.OpenMirrorWorkspace(ctx, filepath.Join(*cacheDir, "useronly"), userOnlyURL, "", *missingBase, *headBranch)
	userOnlyErrStr := ""
	if userOnlyErr != nil {
		userOnlyErrStr = userOnlyErr.Error()
	}

	argvBytes, _ := os.ReadFile(*argvLog)
	argvText := string(argvBytes)
	b64Cred := base64.StdEncoding.EncodeToString([]byte("botuser:" + *secret))
	b64UserOnly := base64.StdEncoding.EncodeToString([]byte(*secret))

	leaks := []string{}
	for name, blob := range map[string]string{
		"argv":            argvText,
		"missErr":         missErrStr,
		"userOnlyMissErr": userOnlyErrStr,
	} {
		for tokName, tok := range map[string]string{
			"secret":      *secret,
			"b64Cred":     b64Cred,
			"b64UserOnly": b64UserOnly,
		} {
			if tok != "" && strings.Contains(blob, tok) {
				leaks = append(leaks, fmt.Sprintf("%s leaked %s", name, tokName))
			}
		}
	}

	res := map[string]any{
		"baseSha":         ws.BaseSHA,
		"headSha":         ws.Head,
		"diffBytes":       len(diff),
		"diffHasCleanGo":  strings.Contains(diff, "pkg/feature/clean.go"),
		"diffHasMainOnly": strings.Contains(diff, "main-advance-"),
		"isBaseResErr":    isBaseResErr,
		"missErr":         missErrStr,
		"userOnlyMissErr": userOnlyErrStr,
		"gitCallCount":    len(strings.Split(strings.TrimSpace(argvText), "\n")),
		"deepenInvoked":   strings.Contains(argvText, "--deepen=256") || strings.Contains(argvText, "--unshallow"),
		"leaks":           leaks,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	if !isBaseResErr || len(leaks) > 0 {
		os.Exit(2)
	}
}
