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

// probe_commandforge exercises real CommandForgeTransport (both direct and
// relayed through a live `grain hostd` socket offering `host-git`) against the
// mock `forgecli` script for:
//   - 304 Not Modified ETag revalidation (clean drain, no cancel/hang)
//   - 204 No Content response (clean drain, no cancel/hang)
//   - HEAD response (clean drain, no cancel/hang)
//   - 200 text/html partial non-login HTML cut short -> ErrForgeBodyCut
//   - 200 cut short with signed URL on stderr -> ErrForgeBodyCut + query redacted
//   - pre-header failure with signed URL on stderr -> check query redaction
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

func writeMode(stateDir string, mode map[string]any) {
	b, _ := json.Marshal(mode)
	if err := os.WriteFile(filepath.Join(stateDir, "mode.json"), b, 0o644); err != nil {
		log.Fatalf("write mode.json: %v", err)
	}
}

func main() {
	stateDir := flag.String("state-dir", "", "forgecli state directory")
	hostdSocket := flag.String("hostd-socket", "", "optional grain hostd unix socket for host-git relay")
	flag.Parse()

	gitproxytest.Install()

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)

	if *hostdSocket != "" {
		restore := gitproxy.UseHostGit(&gitproxy.HostGitClient{
			Hostd:  hostd.Client{Socket: *hostdSocket},
			Stderr: &logBuf,
		})
		defer restore()
	}

	credDir, err := os.MkdirTemp("", "probe-creds-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdtemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(credDir)

	cs, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewCredentialSet: %v\n", err)
		os.Exit(1)
	}
	if err := cs.SetAmbient("depot", gitproxytest.Host, gitproxytest.Source); err != nil {
		fmt.Fprintf(os.Stderr, "SetAmbient: %v\n", err)
		os.Exit(1)
	}
	if err := cs.SetHost("depot", gitproxy.CredentialHost{Host: gitproxytest.Host, Kind: "github"}); err != nil {
		fmt.Fprintf(os.Stderr, "SetHost: %v\n", err)
		os.Exit(1)
	}

	transport, err := cs.ForgeTransportOn("depot", gitproxytest.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ForgeTransportOn: %v\n", err)
		os.Exit(1)
	}
	rt := transport.(*github.RealTransport)
	ghClient := github.NewClient(transport, nil)

	results := map[string]any{"viaHostd": *hostdSocket != ""}

	// 1. 304 Not Modified via RealTransport conditional ETag revalidation.
	os.Remove(filepath.Join(*stateDir, "drain-304.ok"))
	writeMode(*stateDir, map[string]any{"drain_delay": 0.15})
	def1, err1 := ghClient.DefaultBranch("acme", "widgets")
	def2, err2 := ghClient.DefaultBranch("acme", "widgets")
	_, marker304Err := os.Stat(filepath.Join(*stateDir, "drain-304.ok"))
	results["304_not_modified"] = map[string]any{
		"firstBranch":  def1,
		"firstErr":     errStr(err1),
		"secondBranch": def2,
		"secondErr":    errStr(err2),
		"cleanExit":    marker304Err == nil,
		"pass":         err1 == nil && err2 == nil && def1 == "main" && def2 == "main" && marker304Err == nil,
	}

	// 2. 204 No Content clean drain.
	os.Remove(filepath.Join(*stateDir, "drain-204.ok"))
	resp204, err204 := transport.Request(http.MethodGet, "/repos/acme/widgets/drain204", nil, nil)
	_, marker204Err := os.Stat(filepath.Join(*stateDir, "drain-204.ok"))
	results["204_no_content"] = map[string]any{
		"status":    resp204.Status,
		"bodyLen":   len(resp204.Body),
		"err":       errStr(err204),
		"cleanExit": marker204Err == nil,
		"pass":      err204 == nil && resp204.Status == 204 && len(resp204.Body) == 0 && marker204Err == nil,
	}

	// 3. HEAD response clean drain.
	os.Remove(filepath.Join(*stateDir, "drain-head.ok"))
	headReq, _ := http.NewRequest(http.MethodHead, "https://"+gitproxytest.Host+"/api/v3/repos/acme/widgets", nil)
	headResp, headErr := rt.Client.Do(headReq)
	var headStatus int
	var headBodyLen int
	if headResp != nil {
		headStatus = headResp.StatusCode
		b, _ := io.ReadAll(headResp.Body)
		headBodyLen = len(b)
		headResp.Body.Close()
	}
	_, markerHeadErr := os.Stat(filepath.Join(*stateDir, "drain-head.ok"))
	results["head_drain"] = map[string]any{
		"status":    headStatus,
		"bodyLen":   headBodyLen,
		"err":       errStr(headErr),
		"cleanExit": markerHeadErr == nil,
		"pass":      headErr == nil && headStatus == 200 && headBodyLen == 0 && markerHeadErr == nil,
	}

	// 4. Cut-body 200 text/html partial non-login HTML -> ErrForgeBodyCut.
	writeMode(*stateDir, map[string]any{"job_log": "cut_html"})
	_, htmlErr := transport.Request(http.MethodGet, "/repos/acme/widgets/actions/jobs/701/logs", nil, nil)
	results["cut_html"] = map[string]any{
		"err":             errStr(htmlErr),
		"isErrForgeCut":   errors.Is(htmlErr, gitproxy.ErrForgeBodyCut),
		"leaksSecret":     strings.Contains(errStr(htmlErr), "HTML_SECRET_SIG"),
		"hasRedacted":     strings.Contains(errStr(htmlErr), "?[redacted]"),
		"pass":            errors.Is(htmlErr, gitproxy.ErrForgeBodyCut) && !strings.Contains(errStr(htmlErr), "HTML_SECRET_SIG") && strings.Contains(errStr(htmlErr), "?[redacted]"),
	}

	// 5. Cut-body 200 text/plain with signed redirect URL on stderr -> ErrForgeBodyCut + redacted.
	writeMode(*stateDir, map[string]any{"job_log": "cut_signed_url"})
	_, cutErr := transport.Request(http.MethodGet, "/repos/acme/widgets/actions/jobs/701/logs", nil, nil)
	results["cut_signed_url"] = map[string]any{
		"err":           errStr(cutErr),
		"isErrForgeCut": errors.Is(cutErr, gitproxy.ErrForgeBodyCut),
		"leaksSecret":   strings.Contains(errStr(cutErr), "SUPER_SECRET_SIG"),
		"hasRedacted":   strings.Contains(errStr(cutErr), "?[redacted]"),
		"pass":          errors.Is(cutErr, gitproxy.ErrForgeBodyCut) && !strings.Contains(errStr(cutErr), "SUPER_SECRET_SIG") && strings.Contains(errStr(cutErr), "?[redacted]"),
	}

	// 6. Pre-header failure with signed redirect URL on stderr -> check for secret leak!
	writeMode(*stateDir, map[string]any{"job_log": "preheader_signed_url"})
	_, preErr := transport.Request(http.MethodGet, "/repos/acme/widgets/actions/jobs/701/logs", nil, nil)
	results["preheader_signed_url"] = map[string]any{
		"err":         errStr(preErr),
		"leaksSecret": strings.Contains(errStr(preErr), "PREHEADER_SECRET_SIG"),
		"hasRedacted": strings.Contains(errStr(preErr), "?[redacted]"),
		"pass":        preErr != nil && !strings.Contains(errStr(preErr), "PREHEADER_SECRET_SIG") && strings.Contains(errStr(preErr), "?[redacted]"),
	}

	logs := logBuf.String()
	results["logs_leak_any_secret"] = strings.Contains(logs, "SUPER_SECRET_SIG") ||
		strings.Contains(logs, "HTML_SECRET_SIG") ||
		strings.Contains(logs, "PREHEADER_SECRET_SIG")
	results["logs_contain_context_canceled"] = strings.Contains(logs, "context canceled")
	results["logs_sample"] = logs

	// Also verify ForgeTransportOn timeout is 60s.
	results["forge_api_timeout_seconds"] = rt.Client.Timeout / time.Second

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
