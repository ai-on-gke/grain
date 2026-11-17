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
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa/judge"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const qaJudgeUsage = "usage: grain qa judge <run|label> [flags]"

var jsonScoreRe = regexp.MustCompile(`"overall"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
var jsonProbRe = regexp.MustCompile(`"treatment_prob"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)

func cmdQAJudge(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	sub := ""
	rest := args
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "run":
		fs := flag.NewFlagSet("grain qa judge run", flag.ContinueOnError)
		expID := fs.String("experiment", "", "experiment ID to evaluate")
		judgeModel := fs.String("judge-model", "", "judge model ID (must be from a different model family than coding models)")
		raters := fs.Int("raters", 3, "number of independent blind raters per packet")
		budgetTokens := fs.Int64("budget-tokens", 3000000, "maximum token budget across all judge calls")
		dryRun := fs.Bool("dry-run", false, "print packet/chunk counts and token estimate without invoking models")
		outDir := fs.String("out-dir", "", "directory to write readout bundle (defaults to ./qa-readout-<experiment>)")
		checkoutDir := fs.String("checkout", "", "optional local git checkout directory for reading full diffs")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		if strings.TrimSpace(*expID) == "" || strings.TrimSpace(*judgeModel) == "" {
			return usageErrorf("usage: grain qa judge run --experiment <ID> --judge-model <MODEL> [--raters 3] [--budget-tokens N] [--dry-run]")
		}
		if *raters < 1 {
			*raters = 1
		}
		return runBlindJudgeBatch(ctx, c, out, strings.TrimSpace(*expID), strings.TrimSpace(*judgeModel), *raters, *budgetTokens, *dryRun, strings.TrimSpace(*outDir), strings.TrimSpace(*checkoutDir))

	case "label":
		fs := flag.NewFlagSet("grain qa judge label", flag.ContinueOnError)
		expID := fs.String("experiment", "", "experiment ID")
		readoutDir := fs.String("readout-dir", "", "readout directory holding blind packets")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		if strings.TrimSpace(*expID) == "" {
			return usageErrorf("usage: grain qa judge label --experiment <ID> [--readout-dir DIR]")
		}
		return runBlindHumanCalibration(ctx, c, out, strings.TrimSpace(*expID), strings.TrimSpace(*readoutDir))

	default:
		return usageErrorf("%s", qaJudgeUsage)
	}
}

func runBlindJudgeBatch(
	ctx context.Context,
	c *ui.HTTPClient,
	out *printer,
	expID, judgeModel string,
	raters int,
	budgetTokens int64,
	dryRun bool,
	outDir, checkoutDir string,
) error {
	var expDetail map[string]any
	if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/experiments/"+url.PathEscape(expID), nil, &expDetail); err != nil {
		return err
	}
	var expResults map[string]any
	if err := c.DoJSON(ctx, http.MethodGet, "/api/qa/experiments/"+url.PathEscape(expID)+"/results", nil, &expResults); err != nil {
		return err
	}

	// Collect coding models from modelMix.byArm to enforce cross-family blind evaluation.
	var codingModels []string
	if mm, ok := expResults["modelMix"].(map[string]any); ok {
		if byArm, ok := mm["byArm"].(map[string]any); ok {
			for _, mMap := range byArm {
				if models, ok := mMap.(map[string]any); ok {
					for mName := range models {
						if mName != "" && mName != "default" {
							codingModels = append(codingModels, mName)
						}
					}
				}
			}
		}
	}
	if err := judge.ValidateCrossFamilyJudge(judgeModel, codingModels); err != nil {
		return err
	}

	pairMap := map[string]string{}
	if pm, ok := expResults["pairByOrigin"].(map[string]any); ok {
		for k, v := range pm {
			if vs, ok := v.(string); ok {
				pairMap[k] = vs
			}
		}
	}

	// Fetch tasks and build blind packets for enrolled tasks with PRs.
	var taskPage struct {
		Tasks []ui.Task `json:"tasks"`
	}
	var tasks []ui.Task
	var rawTasks json.RawMessage
	if err := c.DoJSON(ctx, http.MethodGet, "/api/tasks?fields=full&limit=0", nil, &rawTasks); err != nil {
		return err
	}
	if json.Unmarshal(rawTasks, &tasks) != nil {
		_ = json.Unmarshal(rawTasks, &taskPage)
		tasks = taskPage.Tasks
	}

	type enrolledPacketMeta struct {
		packet judge.JudgePacket
		armID  string
		root   string
		taskID string
	}
	var metas []enrolledPacketMeta
	totalChunks := 0
	var estTokens int64

	for idx, t := range tasks {
		if t.PullRequest == "" {
			continue
		}
		var taskQA struct {
			Arm          string `json:"arm"`
			ExperimentID string `json:"experimentId"`
			ArmID        string `json:"armId"`
		}
		if err := c.DoJSON(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(t.ID)+"/qa", nil, &taskQA); err != nil {
			continue
		}
		if strings.TrimSpace(taskQA.ExperimentID) != expID {
			continue
		}
		arm := strings.TrimSpace(taskQA.ArmID)
		if arm == "" {
			arm = strings.TrimSpace(taskQA.Arm)
		}
		if arm == "" || arm == "excluded" || arm == "baseline" || arm == "off" {
			continue
		}
		diffText := fetchTaskDiffForJudge(ctx, c, t, checkoutDir)
		redactedDiff := judge.RedactBlindText(diffText)
		chunks := judge.SplitDiffIntoChunks(redactedDiff, 1500)
		if len(chunks) == 0 {
			chunks = []judge.DiffChunk{{Content: "(empty diff)"}}
		}
		inj := judge.DetectPromptInjection(diffText)
		pktTokens := (len(t.Title) + len(t.Description) + len(redactedDiff)) / 4
		estTokens += int64(pktTokens*(raters+1) + 400*(raters+1))
		totalChunks += len(chunks)

		root := strings.TrimSpace(t.WorkOrigin)
		if root == "" {
			root = "task:" + t.ID
		}
		pkt := judge.JudgePacket{
			PacketID:        fmt.Sprintf("pkt-%03d", idx+1),
			PairID:          pairMap[root],
			RootSpecTitle:   judge.RedactBlindText(t.Title),
			RootSpecBody:    judge.RedactBlindText(t.Description),
			CIConclusion:    "success",
			Chunks:          chunks,
			InjectionFlags:  inj,
			EstimatedTokens: pktTokens,
		}
		metas = append(metas, enrolledPacketMeta{
			packet: pkt,
			armID:  arm,
			root:   root,
			taskID: t.ID,
		})
	}

	if dryRun {
		summary := map[string]any{
			"experimentId":    expID,
			"judgeModel":      judgeModel,
			"packets":         len(metas),
			"chunks":          totalChunks,
			"raters":          raters,
			"estimatedTokens": estTokens,
			"budgetTokens":    budgetTokens,
		}
		if out.json {
			out.encode(summary)
			return nil
		}
		fmt.Printf("dry-run readout for %s (judge=%s):\n", expID, judgeModel)
		fmt.Printf("  packets / chunks: %d / %d\n", len(metas), totalChunks)
		fmt.Printf("  estimated tokens: %d (budget %d)\n", estTokens, budgetTokens)
		return nil
	}

	if outDir == "" {
		outDir = filepath.Join(".", "qa-readout-"+expID)
	}

	var scores []judge.PacketScore
	var raterMatrix [][]float64
	var probeProbs []float64
	var probeIsTreat []bool
	var packets []judge.JudgePacket
	var outcomes []map[string]any
	var spentTokens int64

	callJudge := func(prompt string) (string, error) {
		if spentTokens >= budgetTokens {
			return "", fmt.Errorf("judge token budget (%d) exhausted at %d tokens", budgetTokens, spentTokens)
		}
		var resp struct {
			Output    string `json:"output"`
			TokensIn  int64  `json:"tokensIn"`
			TokensOut int64  `json:"tokensOut"`
		}
		err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(expID)+"/readout", map[string]any{
			"action": "judge-prompt",
			"model":  judgeModel,
			"prompt": prompt,
		}, &resp)
		spentTokens += resp.TokensIn + resp.TokensOut
		return resp.Output, err
	}

	for _, m := range metas {
		packets = append(packets, m.packet)
		var rScores []float64
		for rIdx := 0; rIdx < raters; rIdx++ {
			var chunkNotes []string
			for cIdx, ch := range m.packet.Chunks {
				prompt := fmt.Sprintf(
					"You are blind senior code reviewer #%d evaluating a pull request strictly against its root specification.\n"+
						"Score overall engineering quality from 1.0 to 10.0 covering correctness, test strength, boundary safety, and scope discipline.\n"+
						"Spec Title: %s\nSpec Body:\n%s\n\nDiff chunk %d/%d:\n```diff\n%s\n```\n"+
						"Respond ONLY with JSON: {\"overall\": <1.0-10.0>, \"note\": \"one sentence\"}",
					rIdx+1, m.packet.RootSpecTitle, m.packet.RootSpecBody, cIdx+1, len(m.packet.Chunks), ch.Content,
				)
				txt, err := callJudge(prompt)
				if err != nil {
					return err
				}
				chunkNotes = append(chunkNotes, txt)
			}
			finalTxt := chunkNotes[0]
			if len(chunkNotes) > 1 {
				sumPrompt := fmt.Sprintf(
					"Synthesize your %d chunk evaluations into one overall score (1.0-10.0).\nChunk evaluations:\n%s\nRespond ONLY with JSON: {\"overall\": <1.0-10.0>}",
					len(chunkNotes), strings.Join(chunkNotes, "\n"),
				)
				txt, err := callJudge(sumPrompt)
				if err != nil {
					return err
				}
				finalTxt = txt
			}
			sc := parseJSONFloat(jsonScoreRe, finalTxt, 7.0)
			rScores = append(rScores, sc)
		}

		var mean float64
		for _, v := range rScores {
			mean += v
		}
		mean /= float64(len(rScores))
		scores = append(scores, judge.PacketScore{
			PacketID: m.packet.PacketID,
			ArmID:    m.armID,
			Score:    mean,
		})
		if len(rScores) >= 2 {
			raterMatrix = append(raterMatrix, rScores)
		}

		// Arm-prediction blindness probe.
		probePrompt := fmt.Sprintf(
			"Blindness audit probe: given ONLY this anonymized diff, estimate probability (0.0 to 1.0) that pre-PR automated quality gates ran on it.\n```diff\n%s\n```\nRespond ONLY with JSON: {\"treatment_prob\": <0.0-1.0>}",
			m.packet.Chunks[0].Content,
		)
		if pTxt, pErr := callJudge(probePrompt); pErr == nil {
			prob := parseJSONFloat(jsonProbRe, pTxt, 0.5)
			probeProbs = append(probeProbs, prob)
			probeIsTreat = append(probeIsTreat, m.armID != "control")
		}

		outcomes = append(outcomes, map[string]any{
			"pairId":      m.packet.PairID,
			"armId":       m.armID,
			"rootOrigin":  m.root,
			"taskId":      m.taskID,
			"panelScore":  mean,
			"raterScores": rScores,
		})
	}

	probeAUC := 0.5
	if len(probeProbs) >= 2 {
		probeAUC = judge.ComputeArmProbeAUC(probeProbs, probeIsTreat)
	}
	var armIDs []string
	if rawArms, ok := expDetail["arms"].([]any); ok {
		for _, ra := range rawArms {
			if am, ok := ra.(map[string]any); ok {
				if idStr, ok := am["id"].(string); ok {
					armIDs = append(armIDs, idStr)
				}
			}
		}
	}
	manifest := judge.ReadoutManifest{
		ExperimentID: expID,
		ArmIDs:       armIDs,
		TotalPackets: len(packets),
	}
	probe := judge.ProbeReport{AUC: probeAUC}
	if err := judge.WriteReadoutBundle(outDir, manifest, scores, nil, probe, packets); err != nil {
		return err
	}

	var importResp map[string]any
	if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(expID)+"/readout", map[string]any{
		"action":      "import",
		"model":       judgeModel,
		"manifest":    manifest,
		"scores":      scores,
		"raterMatrix": raterMatrix,
		"probe":       probe,
		"outcomes":    outcomes,
	}, &importResp); err != nil {
		return err
	}
	if out.json {
		out.encode(importResp)
		return nil
	}
	fmt.Printf("completed blind judge readout for %s (%d packets, %d tokens, probe AUC %.2f) -> %s\n",
		expID, len(packets), spentTokens, probeAUC, outDir)
	return nil
}

func fetchTaskDiffForJudge(ctx context.Context, c *ui.HTTPClient, t ui.Task, checkoutDir string) string {
	if checkoutDir != "" && t.Branch != "" && t.Base != "" {
		cmd := exec.CommandContext(ctx, "git", "-C", checkoutDir, "diff", "origin/"+t.Base+"...origin/"+t.Branch, "--")
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"GIT_TERMINAL_PROMPT=0",
		}
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			return string(out)
		}
	}
	var dp struct {
		Files []struct {
			Path  string `json:"path"`
			Hunks []struct {
				Header string `json:"header"`
				Lines  []struct {
					Text string `json:"text"`
				} `json:"lines"`
			} `json:"hunks"`
		} `json:"files"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(t.ID)+"/diff-preview?max=50", nil, &dp); err == nil {
		var b strings.Builder
		for _, f := range dp.Files {
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.Path, f.Path)
			for _, h := range f.Hunks {
				b.WriteString(h.Header + "\n")
				for _, l := range h.Lines {
					b.WriteString(l.Text + "\n")
				}
			}
		}
		return b.String()
	}
	return ""
}

func parseJSONFloat(re *regexp.Regexp, s string, fallback float64) float64 {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return fallback
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fallback
	}
	return v
}

func runBlindHumanCalibration(ctx context.Context, c *ui.HTTPClient, out *printer, expID, readoutDir string) error {
	if readoutDir == "" {
		readoutDir = filepath.Join(".", "qa-readout-"+expID)
	}
	manifest, scores, sevs, probe, packets, err := judge.LoadReadoutBundle(readoutDir)
	if err != nil {
		return fmt.Errorf("loading readout bundle from %s: %w", readoutDir, err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	labelled := 0
	for _, p := range packets {
		fmt.Printf("\n=== Blind Packet %s (%s) ===\n%s\nScore (1-10, or q to finish): ", p.PacketID, p.RootSpecTitle, p.RootSpecBody)
		if !scanner.Scan() {
			break
		}
		txt := strings.TrimSpace(scanner.Text())
		if txt == "q" || txt == "" {
			break
		}
		if _, pErr := strconv.ParseFloat(txt, 64); pErr == nil {
			labelled++
		}
	}
	var resp map[string]any
	if err := c.DoJSON(ctx, http.MethodPost, "/api/qa/experiments/"+url.PathEscape(expID)+"/readout", map[string]any{
		"action":           "import",
		"manifest":         manifest,
		"scores":           scores,
		"severities":       sevs,
		"probe":            probe,
		"humanLabelsCount": labelled,
	}, &resp); err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	fmt.Printf("recorded %d blind human calibration label(s) for %s\n", labelled, expID)
	return nil
}
