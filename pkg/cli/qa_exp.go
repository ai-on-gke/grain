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

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
	"gopkg.in/yaml.v3"
)

const qaExpUsage = "usage: grain qa exp <list|show|create|edit|start|halt-arm|resume-enrollment|stop|archive|results|keys> [flags]"

func decodeJSONOrYAMLFile(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return json.Unmarshal(raw, dst)
	}
	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return err
	}
	b, err := json.Marshal(generic)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func cmdQAExp(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	sub := ""
	rest := args
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "list":
		fs := flag.NewFlagSet("grain qa exp list", flag.ContinueOnError)
		state := fs.String("state", "", "filter by state (draft, running, stopped, archived)")
		limit := fs.Int("limit", 50, "maximum experiments to return")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		q := url.Values{}
		if strings.TrimSpace(*state) != "" {
			q.Set("state", strings.TrimSpace(*state))
		}
		if *limit > 0 {
			q.Set("limit", strconv.Itoa(*limit))
		}
		path := "/api/qa/experiments"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var resp struct {
			Experiments []map[string]any `json:"experiments"`
		}
		if err := c.DoJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		for _, e := range resp.Experiments {
			fmt.Printf("%v\t%v\t%v\t%v\n", e["id"], e["state"], e["primaryMetric"], e["name"])
		}
		return nil

	case "show":
		fs := flag.NewFlagSet("grain qa exp show", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output raw JSON")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp show <ID> [--json]")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/experiments/"+url.PathEscape(tail[0]), nil, &resp); err != nil {
			return err
		}
		if out.json || *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Printf("experiment: %v (%v)\n", resp["id"], resp["state"])
		fmt.Printf("  name:       %v\n", resp["name"])
		fmt.Printf("  hypothesis: %v\n", resp["hypothesis"])
		fmt.Printf("  primary:    %v (%v)\n", resp["primaryMetric"], resp["primaryContrast"])
		fmt.Printf("  segment:    %v\n", resp["segment"])
		return nil

	case "create":
		fs := flag.NewFlagSet("grain qa exp create", flag.ContinueOnError)
		file := fs.String("f", "", "experiment definition file (JSON or YAML)")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		if strings.TrimSpace(*file) == "" {
			return usageErrorf("usage: grain qa exp create -f <FILE>")
		}
		var payload map[string]any
		if err := decodeJSONOrYAMLFile(*file, &payload); err != nil {
			return err
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments", payload, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		expObj, _ := resp["experiment"].(map[string]any)
		fmt.Printf("created experiment %v (state=%v)\n", expObj["id"], expObj["state"])
		return nil

	case "edit":
		fs := flag.NewFlagSet("grain qa exp edit", flag.ContinueOnError)
		file := fs.String("f", "", "updated draft experiment fields (JSON or YAML)")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 || strings.TrimSpace(*file) == "" {
			return usageErrorf("usage: grain qa exp edit <ID> -f <FILE>")
		}
		var payload map[string]any
		if err := decodeJSONOrYAMLFile(*file, &payload); err != nil {
			return err
		}
		payload["action"] = "edit"
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", payload, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("edited draft experiment %s\n", tail[0])
		return nil

	case "start":
		fs := flag.NewFlagSet("grain qa exp start", flag.ContinueOnError)
		overrideNotes := fs.String("override-notes", "", "reason for overriding prior not-helpful warning")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp start <ID> [--override-notes TEXT]")
		}
		body := map[string]any{
			"action":        "start",
			"dryRunViewed":  true,
			"overrideNotes": strings.TrimSpace(*overrideNotes),
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", body, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("started experiment %s\n", tail[0])
		return nil

	case "halt-arm":
		fs := flag.NewFlagSet("grain qa exp halt-arm", flag.ContinueOnError)
		reason := fs.String("reason", "", "reason for halting the treatment arm")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 2 || strings.TrimSpace(*reason) == "" {
			return usageErrorf("usage: grain qa exp halt-arm <ID> <ARM> --reason <TEXT>")
		}
		body := map[string]any{
			"action": "halt-arm",
			"arm":    tail[1],
			"reason": strings.TrimSpace(*reason),
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", body, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("halted arm %s on experiment %s\n", tail[1], tail[0])
		return nil

	case "resume-enrollment":
		fs := flag.NewFlagSet("grain qa exp resume-enrollment", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp resume-enrollment <ID>")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", map[string]any{"action": "resume-enrollment"}, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("resumed enrollment on experiment %s\n", tail[0])
		return nil

	case "stop":
		fs := flag.NewFlagSet("grain qa exp stop", flag.ContinueOnError)
		decision := fs.String("decision", "", "terminal decision (ship|reject|inconclusive|invalid)")
		arm := fs.String("arm", "", "winning/referenced arm id")
		reason := fs.String("reason", "", "decision rationale")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 || strings.TrimSpace(*decision) == "" || strings.TrimSpace(*reason) == "" {
			return usageErrorf("usage: grain qa exp stop <ID> --decision <ship|reject|inconclusive|invalid> [--arm ARM] --reason <TEXT>")
		}
		body := map[string]any{
			"action":   "stop",
			"decision": strings.TrimSpace(*decision),
			"arm":      strings.TrimSpace(*arm),
			"reason":   strings.TrimSpace(*reason),
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", body, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("stopped experiment %s (%s)\n", tail[0], *decision)
		return nil

	case "archive":
		fs := flag.NewFlagSet("grain qa exp archive", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp archive <ID>")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/transition", map[string]any{"action": "archive"}, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("archived experiment %s\n", tail[0])
		return nil

	case "results":
		fs := flag.NewFlagSet("grain qa exp results", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output raw JSON")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp results <ID> [--json]")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/experiments/"+url.PathEscape(tail[0])+"/results", nil, &resp); err != nil {
			return err
		}
		if out.json || *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Printf("experiment: %v (%v)\n", resp["experimentId"], resp["state"])
		if verdicts, ok := resp["verdicts"].([]any); ok {
			for _, v := range verdicts {
				if vm, ok := v.(map[string]any); ok {
					fmt.Printf("  [%v] %v — %v\n", vm["status"], vm["title"], vm["summary"])
				}
			}
		}
		return nil

	case "keys":
		fs := flag.NewFlagSet("grain qa exp keys", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa exp keys <ID>")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/experiments/"+url.PathEscape(tail[0]), nil, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(map[string]any{
				"allocationKeys":  resp["allocationKeys"],
				"allocationPairs": resp["allocationPairs"],
			})
			return nil
		}
		if keys, ok := resp["allocationKeys"].([]any); ok {
			for _, k := range keys {
				fmt.Println(k)
			}
		}
		return nil

	default:
		return usageErrorf("%s", qaExpUsage)
	}
}
