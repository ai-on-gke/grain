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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"gopkg.in/yaml.v3"
)

// QuarantineToolConfig configures the quarantine_test MCP tool.
type QuarantineToolConfig struct {
	WorkspaceDir string
	TaskID       string
	Repo         string
	Branch       string
	Sink         *MockSink
	Filer        ProposalFiler
}

// NewQuarantineTools constructs the quarantine tool suite.
func NewQuarantineTools(cfg QuarantineToolConfig) []Tool {
	return []Tool{
		quarantineTestTool(cfg),
	}
}

func quarantineTestTool(cfg QuarantineToolConfig) Tool {
	return Tool{
		Name: "quarantine_test",
		Description: "Quarantine a failing or flaky test so its failures in CI are non-blocking warnings, and file a follow-up repair task. " +
			"Records the quarantine entry in .grain/quarantine.yml with metadata (reason, created_at, expires_at, author task ID, and linked follow-up task ID).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"test_name", "reason"},
			"properties": map[string]any{
				"test_name": map[string]any{
					"type":        "string",
					"description": "Qualified test name, pattern, or suite path to quarantine (e.g. TestFlakyReconcile or pkg/engine.TestFlakyReconcile).",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Explanation of why the test is being quarantined (e.g. suspected timing flake under CPU contention).",
				},
				"file_follow_up": map[string]any{
					"type":        "boolean",
					"description": "Whether to automatically file a linked follow-up repair task to fix and unquarantine the test (default true).",
				},
				"ttl": map[string]any{
					"type":        "string",
					"description": "Time-to-live expiration duration (e.g. 30d, 720h). Defaults to 30d.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			testName, _ := argString(args, "test_name")
			testName = strings.TrimSpace(testName)
			if testName == "" {
				return Result{IsError: true, Text: "A test quarantine needs a \"test_name\": provide the test name, pattern, or suite path."}
			}
			reason, _ := argString(args, "reason")
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return Result{IsError: true, Text: "A test quarantine needs a \"reason\": explain why the test is being quarantined."}
			}

			fileFollowUp := true
			if ff, ok := argBool(args, "file_follow_up"); ok {
				fileFollowUp = ff
			}

			ttl, _ := argString(args, "ttl")
			ttl = strings.TrimSpace(ttl)
			if ttl == "" {
				ttl = "30d"
			}

			now := time.Now().UTC()
			var expiresAt *time.Time
			dur, err := parseQuarantineTTL(ttl)
			if err == nil && dur > 0 {
				exp := now.Add(dur)
				expiresAt = &exp
			}

			var followUpID string
			if fileFollowUp {
				title := fmt.Sprintf("fix(test): unquarantine and fix flaky test %s", testName)
				body := fmt.Sprintf("Test `%s` was quarantined by task %s.\n\nReason: %s\n\nPlease investigate the root cause, fix the flake, and remove the entry from .grain/quarantine.yml.", testName, cfg.TaskID, reason)
				proposed := ProposedTask{
					ID:     fmt.Sprintf("quarantine-fix-%s", strings.ToLower(sanitizeTaskID(testName))),
					Title:  title,
					Body:   body,
					Repo:   cfg.Repo,
					Branch: cfg.Branch,
				}
				if cfg.Filer != nil {
					filed, err := cfg.Filer.FileProposal(ctx, proposed, cfg.TaskID)
					if err == nil && filed.ID != "" {
						followUpID = filed.ID
					}
				}
				if followUpID == "" && cfg.Sink != nil {
					cfg.Sink.RecordProposedTask(proposed)
					followUpID = proposed.ID
				}
			}

			quarantineRelPath := ".grain/quarantine.yml"
			quarantinePath := quarantineRelPath
			if cfg.WorkspaceDir != "" {
				quarantinePath = filepath.Join(cfg.WorkspaceDir, quarantineRelPath)
			}

			var qCfg ci.QuarantineConfig
			var raw []byte
			if cfg.WorkspaceDir != "" {
				raw, _ = os.ReadFile(quarantinePath)
				if len(raw) == 0 {
					altPath := filepath.Join(cfg.WorkspaceDir, ".grain/quarantine.yaml")
					raw, _ = os.ReadFile(altPath)
				}
			} else {
				raw, _ = os.ReadFile(quarantineRelPath)
			}

			if len(raw) > 0 {
				parsed, err := ci.LoadQuarantine(raw)
				if err == nil && parsed != nil {
					qCfg = *parsed
				}
			}

			newEntry := ci.QuarantineEntry{
				TestName:       testName,
				Reason:         reason,
				TaskID:         cfg.TaskID,
				FollowUpTaskID: followUpID,
				CreatedAt:      &now,
				ExpiresAt:      expiresAt,
				TTL:            ttl,
			}

			updated := false
			for i, e := range qCfg.Tests {
				if e.TestName == testName {
					qCfg.Tests[i] = newEntry
					updated = true
					break
				}
			}
			if !updated {
				qCfg.Tests = append(qCfg.Tests, newEntry)
			}

			outBytes, err := yaml.Marshal(qCfg)
			if err != nil {
				return Result{IsError: true, Text: fmt.Sprintf("failed to marshal quarantine yaml: %v", err)}
			}

			if cfg.WorkspaceDir != "" {
				_ = os.MkdirAll(filepath.Dir(quarantinePath), 0755)
				if err := os.WriteFile(quarantinePath, outBytes, 0644); err != nil {
					return Result{IsError: true, Text: fmt.Sprintf("failed to write quarantine file: %v", err)}
				}
			} else {
				_ = os.MkdirAll(filepath.Dir(quarantineRelPath), 0755)
				if err := os.WriteFile(quarantineRelPath, outBytes, 0644); err != nil {
					return Result{IsError: true, Text: fmt.Sprintf("failed to write quarantine file: %v", err)}
				}
			}

			msg := fmt.Sprintf("Quarantined test %q.\n- Reason: %s\n- TTL: %s\n- Created: %s\n",
				testName, reason, ttl, now.Format(time.RFC3339))
			if followUpID != "" {
				msg += fmt.Sprintf("- Follow-up repair task filed: %s\n", followUpID)
			}
			msg += "Recorded in .grain/quarantine.yml."

			return Result{Text: msg}
		},
	}
}

func parseQuarantineTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

func sanitizeTaskID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
