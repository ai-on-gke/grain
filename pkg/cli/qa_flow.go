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
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/ui"
	"gopkg.in/yaml.v3"
)

func cmdQAFlow(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	sub := ""
	rest := args
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "list":
		fs := flag.NewFlagSet("grain qa flow list", flag.ContinueOnError)
		key := fs.String("key", "", "filter by flow key")
		limit := fs.Int("limit", 50, "maximum versions to return")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		q := url.Values{}
		if strings.TrimSpace(*key) != "" {
			q.Set("key", strings.TrimSpace(*key))
		}
		if *limit > 0 {
			q.Set("limit", strconv.Itoa(*limit))
		}
		path := "/api/qa/flow-versions"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var resp struct {
			Versions []map[string]any `json:"versions"`
		}
		if err := c.DoJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		for _, v := range resp.Versions {
			state := "active"
			if v["retiredAt"] != nil {
				state = "retired"
			} else if b, _ := v["builtIn"].(bool); b {
				state = "built-in"
			}
			hashStr, _ := v["hash"].(string)
			shortHash := hashStr
			if len(shortHash) > 12 {
				shortHash = shortHash[:12]
			}
			fmt.Printf("%s@%v\t%s\t%s\t%s\n", v["key"], v["version"], shortHash, state, v["label"])
		}
		return nil

	case "show":
		fs := flag.NewFlagSet("grain qa flow show", flag.ContinueOnError)
		asYAML := fs.Bool("yaml", false, "output YAML declaration")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa flow show <REF> [--yaml]")
		}
		var resp map[string]any
		path := "/api/qa/flow-versions/" + url.PathEscape(tail[0])
		if err := c.DoJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		if *asYAML {
			if src, _ := resp["sourceYaml"].(string); strings.TrimSpace(src) != "" {
				fmt.Print(src)
				if !strings.HasSuffix(src, "\n") {
					fmt.Println()
				}
				return nil
			}
			raw, err := yaml.Marshal(resp["declaration"])
			if err != nil {
				return err
			}
			fmt.Print(string(raw))
			return nil
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("flow:    %v@%v\nhash:    %v\nlabel:   %v\nsummary: %v\n",
			resp["key"], resp["version"], resp["hash"], resp["label"], resp["summary"])
		return nil

	case "fork":
		fs := flag.NewFlagSet("grain qa flow fork", flag.ContinueOnError)
		newKey := fs.String("key", "", "new flow key")
		output := fs.String("o", "", "output file path (stdout if omitted)")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 || strings.TrimSpace(*newKey) == "" {
			return usageErrorf("usage: grain qa flow fork <SRC> --key <K> [-o <FILE>]")
		}
		var src map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/flow-versions/"+url.PathEscape(tail[0]), nil, &src); err != nil {
			return err
		}
		parentHash, _ := src["hash"].(string)
		forkDecl := qa.FlowDeclarationV2{
			Schema:  qa.FlowSchemaV2,
			Key:     strings.TrimSpace(*newKey),
			Extends: parentHash,
			Label:   strings.TrimSpace(*newKey),
			Skill:   qa.SkillDecl{Mode: "inherit", MaxBytes: 1800},
		}
		raw, err := yaml.Marshal(forkDecl)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*output) != "" {
			return os.WriteFile(*output, raw, 0o644)
		}
		fmt.Print(string(raw))
		return nil

	case "validate":
		fs := flag.NewFlagSet("grain qa flow validate", flag.ContinueOnError)
		dryRun := fs.Int("dry-run", 0, "dry run sampling count over recent PRs")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa flow validate <FILE> [--dry-run <N>]")
		}
		data, err := os.ReadFile(tail[0])
		if err != nil {
			return err
		}
		decl, err := qa.ParseFlowDeclaration(data)
		if err != nil {
			return err
		}
		var key string
		switch d := decl.(type) {
		case *qa.FlowDeclarationV3:
			key = d.Key
		case qa.FlowDeclarationV3:
			key = d.Key
		case *qa.FlowDeclarationV2:
			key = d.Key
		case qa.FlowDeclarationV2:
			key = d.Key
		}
		reqBody := map[string]any{
			"yaml":        string(data),
			"sampleLimit": *dryRun,
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/flow-versions?dryRun=1", reqBody, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("valid: %s (hash %v, sampled=%v, wouldBlock=%v)\n",
			key, resp["hash"], resp["sampledPRs"], resp["wouldBlockPRs"])
		return nil

	case "publish":
		fs := flag.NewFlagSet("grain qa flow publish", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa flow publish <FILE>")
		}
		data, err := os.ReadFile(tail[0])
		if err != nil {
			return err
		}
		if _, err := qa.ParseFlowDeclaration(data); err != nil {
			return err
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/flow-versions", map[string]any{"yaml": string(data)}, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("published hash: %v\n", resp["hash"])
		return nil

	case "retire":
		fs := flag.NewFlagSet("grain qa flow retire", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) != 1 {
			return usageErrorf("usage: grain qa flow retire <KEY@N|HASH>")
		}
		var resp map[string]any
		if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/flow-versions", map[string]any{
			"action": "retire",
			"hash":   tail[0],
		}, &resp); err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("retired %v at %v\n", resp["hash"], resp["retiredAt"])
		return nil

	case "adopt":
		fs := flag.NewFlagSet("grain qa flow adopt", flag.ContinueOnError)
		var f adoptFlags
		fs.StringVar(&f.Repo, "repo", "", "GitHub repository (owner/name)")
		fs.StringVar(&f.Path, "path", "", "Path to the flow declaration inside the repository")
		fs.StringVar(&f.Commit, "commit", "", "40-char commit SHA to read from")
		fs.BoolVar(&f.DryRun, "dry-run", false, "Validate and simulate adoption without saving")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) > 0 {
			return usageErrorf("usage: grain qa flow adopt --repo <O/R> --path <PATH> --commit <SHA> [--dry-run]")
		}
		return runQAFlowAdopt(ctx, c, out, f)

	case "probe":
		fs := flag.NewFlagSet("grain qa flow probe", flag.ContinueOnError)
		var f probeFlags
		fs.StringVar(&f.Flow, "flow", "", "Flow hash or key@version to probe (default golden-v3@1)")
		fs.StringVar(&f.Title, "title", "", "Simulated pull request title")
		fs.StringVar(&f.Body, "body", "", "Simulated pull request body description")
		fs.StringVar(&f.Files, "files", "", "Comma-separated list of simulated changed file paths")
		fs.IntVar(&f.NonTestAdded, "non-test-added", 0, "Simulated non-test lines added")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if len(tail) > 0 {
			return usageErrorf("usage: grain qa flow probe [--flow <REF>] --title <TITLE> [--body <BODY>] [--files <CSV>] [--non-test-added <N>]")
		}
		return runQAFlowProbe(ctx, c, out, f)

	default:
		return usageErrorf("usage: grain qa flow <list|show|fork|validate|publish|retire|adopt|probe> [args...]")
	}
}
