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

package model

import (
	"regexp"
	"strings"
)

var (
	batchTaskTokenRe  = regexp.MustCompile(`(?i)\b(?:task[- ])?#?([0-9]+)\b`)
	batchTaskLineRe   = regexp.MustCompile(`^([0-9]+)\s+\[`)
	batchRunIDRe      = regexp.MustCompile(`\b(ci-[a-zA-Z0-9_-]+|run-[a-zA-Z0-9_-]+)\b`)
	jsonTaskIDFieldRe = regexp.MustCompile(`"id"\s*:\s*"([0-9]+)"`)
	jsonCIRunIDRe     = regexp.MustCompile(`"id"\s*:\s*"(ci-[a-zA-Z0-9_-]+)"`)
)

// ReadsIDsFromOutput reports whether tool is a listing or feed read whose
// output, rather than its arguments, names the tasks and CI runs it read:
// the ones ReadOutputIDs parses.
func ReadsIDsFromOutput(tool string) bool {
	switch tool {
	case "list_grain_tasks", "read_pipeline_changes", "query_merge_queue", "list_ci_runs",
		"api_list_tasks", "api_list_ci_runs", "grain_list_tasks":
		return true
	}
	return false
}

// ReadOutputIDs returns the task and CI run IDs a listing or feed read
// (ReadsIDsFromOutput) showed in text, in the order they appear. It is the
// same whether the read was an MCP tool call or the in-sandbox grain CLI's
// request through the sandbox proxy, so a steward report is checked the
// same way however the steward read (orchestrator's stewardReportCheck).
func ReadOutputIDs(tool, text string) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if tool == "api_list_tasks" || tool == "grain_list_tasks" || tool == "list_grain_tasks" {
		for _, m := range jsonTaskIDFieldRe.FindAllStringSubmatch(text, -1) {
			if len(m) == 2 {
				add(NormalizeTaskID(m[1]))
			}
		}
	}
	if tool == "api_list_ci_runs" || tool == "list_ci_runs" {
		for _, m := range jsonCIRunIDRe.FindAllStringSubmatch(text, -1) {
			if len(m) == 2 {
				add(m[1])
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "next:") {
			continue
		}
		if tool == "list_grain_tasks" && !strings.HasPrefix(line, " ") {
			if m := batchTaskLineRe.FindStringSubmatch(line); len(m) == 2 {
				add(NormalizeTaskID(m[1]))
			}
		}
		if tool == "read_pipeline_changes" {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "task") {
				add(NormalizeTaskID(fields[1]))
			}
		}
		for _, m := range batchTaskTokenRe.FindAllStringSubmatch(line, -1) {
			if len(m) == 2 && (strings.Contains(strings.ToLower(m[0]), "task") || strings.Contains(m[0], "#")) {
				add(NormalizeTaskID(m[1]))
			}
		}
		if tool == "list_ci_runs" {
			for _, m := range batchRunIDRe.FindAllStringSubmatch(line, -1) {
				if len(m) == 2 {
					add(m[1])
				}
			}
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 && strings.HasPrefix(fields[0], "ci-") {
				add(fields[0])
			}
		}
	}
	return ids
}
