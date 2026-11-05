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

package intree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const (
	maxFigmaExportNodes   = 10
	maxFigmaSpecChildren  = 40
	maxFigmaSpecDepth     = 4
	maxFigmaTextPreview   = 500
	defaultFigmaNodeDepth = 3
)

var figmaHyphenNodeIDPattern = regexp.MustCompile(`^\d+-\d+(?:;\d+-\d+)*$`)

type figmaMCPGrant struct {
	s           *figmaServer
	attemptID   string
	accountName string
	allowedKeys map[string]bool
	perAttempt  int
	mcp         *protocol.CapabilityServer

	mu      sync.Mutex
	calls   int
	records []FigmaAudit
}

// ParsedFigmaURL holds the file key and optional node ID parsed from a Figma URL.
type ParsedFigmaURL struct {
	FileKey string `json:"file_key"`
	NodeID  string `json:"node_id,omitempty"`
	URLType string `json:"url_type,omitempty"`
}

// ParseFigmaURL extracts the file key and optional node-id from a Figma URL
// (such as https://www.figma.com/design/<key>/<title>?node-id=12-34).
func ParseFigmaURL(raw string) (*ParsedFigmaURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid Figma URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host != "figma.com" && !strings.HasSuffix(host, ".figma.com") {
		return nil, fmt.Errorf("URL host %q is not a figma.com URL", host)
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return nil, errors.New("Figma URL path must look like /design/<file_key>/... or /file/<file_key>/...")
	}
	kind := strings.ToLower(parts[0])
	switch kind {
	case "design", "file", "board", "proto", "slides":
	default:
		return nil, fmt.Errorf("unsupported Figma URL path prefix %q", parts[0])
	}
	fileKey := parts[1]
	// Branch URLs look like /design/<file_key>/branch/<branch_key>/<title>; use branch_key when present.
	if len(parts) >= 4 && strings.EqualFold(parts[2], "branch") {
		fileKey = parts[3]
	}
	if !figmaFileKeyPattern.MatchString(fileKey) {
		return nil, fmt.Errorf("extracted file key %q is invalid", fileKey)
	}
	nodeID := normalizeFigmaNodeID(u.Query().Get("node-id"))
	return &ParsedFigmaURL{FileKey: fileKey, NodeID: nodeID, URLType: kind}, nil
}

func normalizeFigmaNodeID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if figmaHyphenNodeIDPattern.MatchString(raw) {
		parts := strings.Split(raw, ";")
		for i, p := range parts {
			parts[i] = strings.Replace(p, "-", ":", 1)
		}
		return strings.Join(parts, ";")
	}
	return raw
}

func resolveFileAndNodes(args map[string]any) (string, []string, error) {
	fileKey, _ := args["file_key"].(string)
	fileKey = strings.TrimSpace(fileKey)
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)

	var urlNode string
	if rawURL != "" {
		parsed, err := ParseFigmaURL(rawURL)
		if err != nil {
			return "", nil, err
		}
		if fileKey != "" && fileKey != parsed.FileKey {
			return "", nil, fmt.Errorf("file_key %q disagrees with url's file key %q", fileKey, parsed.FileKey)
		}
		fileKey = parsed.FileKey
		urlNode = parsed.NodeID
	}
	if fileKey == "" {
		return "", nil, errors.New("provide either file_key or url")
	}
	if !figmaFileKeyPattern.MatchString(fileKey) {
		return "", nil, fmt.Errorf("invalid file_key %q", fileKey)
	}

	var nodes []string
	seen := map[string]bool{}
	addNode := func(s string) {
		n := normalizeFigmaNodeID(s)
		if n != "" && !seen[n] {
			seen[n] = true
			nodes = append(nodes, n)
		}
	}
	if rawList, ok := args["node_ids"].([]any); ok {
		for _, item := range rawList {
			if s, ok := item.(string); ok {
				addNode(s)
			}
		}
	}
	if single, ok := args["node_id"].(string); ok && strings.TrimSpace(single) != "" {
		addNode(single)
	}
	if len(nodes) == 0 && urlNode != "" {
		addNode(urlNode)
	}
	return fileKey, nodes, nil
}

func (f *figmaServer) mintMCP(_ context.Context, attemptID string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
	if !pathSafe(attemptID) {
		return refusedResponse("attempt_id " + attemptID + " is not valid"), nil
	}
	accName, _ := args["account"].(string)
	accName = strings.TrimSpace(accName)

	f.mu.Lock()
	c := f.conf
	var acc *FigmaAccount
	if c != nil {
		acc = c.account(accName)
	}
	f.mu.Unlock()
	if c == nil || acc == nil {
		return refusedResponse(fmt.Sprintf("Figma account %q is not configured", accName)), nil
	}
	if acc.AuthMode == FigmaAuthPAT && acc.PersonalAccessToken == "" {
		return refusedResponse(fmt.Sprintf("Figma account %q has no personal access token configured", acc.Name)), nil
	}
	if acc.AuthMode == FigmaAuthOAuth && (acc.ClientSecret == "" || acc.RefreshToken == "") {
		return refusedResponse(fmt.Sprintf("Figma account %q is missing its OAuth client secret or refresh token", acc.Name)), nil
	}

	allowed, errReason := effectiveFigmaFileKeys(acc.AllowedFileKeys, args["file_keys"])
	if errReason != "" {
		return refusedResponse(errReason), nil
	}

	serverName := FigmaName
	if len(c.Accounts) > 1 {
		serverName = FigmaName + "-" + acc.Name
	}
	gr := &figmaMCPGrant{
		s:           f,
		attemptID:   attemptID,
		accountName: acc.Name,
		allowedKeys: allowed,
		perAttempt:  c.PerAttempt,
	}
	mcpSrv, err := gr.buildToolset(serverName, c.Token)
	if err != nil {
		return nil, err
	}
	gr.mcp = mcpSrv

	f.mu.Lock()
	f.mcpGrants[attemptID] = gr
	f.mu.Unlock()

	scopeNote := "any Figma file accessible to account " + strconv.Quote(acc.Name)
	if len(allowed) > 0 {
		scopeNote = "Figma file key(s): " + joinSortedKeys(allowed)
	}
	return &protocol.MintResponse{
		Status:      protocol.StatusOK,
		MCPEndpoint: protocol.EndpointPath(attemptID),
		PromptSection: fmt.Sprintf(
			"You have Figma MCP access (%s) via server %q with tools `parse_figma_url`, `get_file`, `get_design_context`, `export_images`, `get_styles_and_variables`, and `get_comments` (read-only, up to %d calls this attempt). "+
				"Use `get_design_context` with a Figma URL or file_key + node_ids to inspect frame auto-layout, spacing, colors, borders, shadows, and typography, and `export_images` to fetch rendered PNGs of target frames so you can compare your rendered UI screenshots against the design.",
			scopeNote, serverName, c.PerAttempt,
		),
	}, nil
}

func effectiveFigmaFileKeys(accountAllowed []string, grantRaw any) (map[string]bool, string) {
	var acctSet map[string]bool
	if len(accountAllowed) > 0 {
		acctSet = make(map[string]bool, len(accountAllowed))
		for _, k := range accountAllowed {
			acctSet[k] = true
		}
	}
	rawSlice, hasGrantKeys := grantRaw.([]any)
	if !hasGrantKeys || len(rawSlice) == 0 {
		return acctSet, ""
	}
	grantSet := make(map[string]bool, len(rawSlice))
	for _, item := range rawSlice {
		s, _ := item.(string)
		s = strings.TrimSpace(s)
		if !figmaFileKeyPattern.MatchString(s) {
			return nil, fmt.Sprintf("invalid file key %q in file_keys", s)
		}
		if acctSet != nil && !acctSet[s] {
			return nil, fmt.Sprintf("file key %q is not permitted by account's allowed_file_keys", s)
		}
		grantSet[s] = true
	}
	return grantSet, ""
}

func (f *figmaServer) releaseMCP(_ context.Context, attemptID string) error {
	f.mu.Lock()
	gr := f.mcpGrants[attemptID]
	delete(f.mcpGrants, attemptID)
	f.mu.Unlock()
	if gr == nil {
		return nil
	}
	gr.mu.Lock()
	recs := append([]FigmaAudit(nil), gr.records...)
	gr.mu.Unlock()
	f.mu.Lock()
	f.mcpRetired[attemptID] = append(f.mcpRetired[attemptID], recs...)
	f.mu.Unlock()
	return nil
}

func (f *figmaServer) takeMCPAudit(attemptID string) ([]any, int) {
	f.mu.Lock()
	recs := f.mcpRetired[attemptID]
	delete(f.mcpRetired, attemptID)
	f.mu.Unlock()
	out := make([]any, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out, 0
}

// ServeHTTP serves /runs/<attempt_id>/mcp for active figma#mcp grants.
func (f *figmaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/runs/")
	id, kind, _ := strings.Cut(rest, "/")
	if !ok || kind != "mcp" || !protocol.ValidAttemptID(id) {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	gr := f.mcpGrants[id]
	f.mu.Unlock()
	if gr == nil {
		http.Error(w, "no grant "+id+": it was released, or never minted", http.StatusNotFound)
		return
	}
	gr.mcp.ServeHTTP(w, r)
}

func (gr *figmaMCPGrant) checkAndReserve(op, fileKey string, nodeCount int) (*figmaAccountClient, func(err error), error) {
	now := gr.s.cfg.Now().UTC()
	gr.mu.Lock()
	if gr.calls >= gr.perAttempt {
		reason := fmt.Sprintf("exceeded per-attempt Figma call limit (%d)", gr.perAttempt)
		gr.records = append(gr.records, FigmaAudit{
			Time: now, AttemptID: gr.attemptID, Account: gr.accountName,
			Operation: op, FileKey: fileKey, NodeCount: nodeCount, Refused: reason,
		})
		gr.mu.Unlock()
		return nil, nil, errors.New("refused: " + reason)
	}
	if fileKey != "" && len(gr.allowedKeys) > 0 && !gr.allowedKeys[fileKey] {
		reason := fmt.Sprintf("file_key %q is not in this grant's allowed file keys (%s)", fileKey, joinSortedKeys(gr.allowedKeys))
		gr.records = append(gr.records, FigmaAudit{
			Time: now, AttemptID: gr.attemptID, Account: gr.accountName,
			Operation: op, FileKey: fileKey, NodeCount: nodeCount, Refused: reason,
		})
		gr.mu.Unlock()
		return nil, nil, errors.New("refused: " + reason)
	}
	gr.calls++
	gr.mu.Unlock()

	gr.s.mu.Lock()
	var ac *figmaAccountClient
	if gr.s.clients != nil {
		ac = gr.s.clients[gr.accountName]
	}
	gr.s.mu.Unlock()
	if ac == nil {
		return nil, nil, fmt.Errorf("Figma account %q is no longer configured", gr.accountName)
	}

	finish := func(err error) {
		rec := FigmaAudit{
			Time:      now,
			AttemptID: gr.attemptID,
			Account:   gr.accountName,
			Operation: op,
			FileKey:   fileKey,
			NodeCount: nodeCount,
		}
		if err != nil {
			rec.Error = err.Error()
		}
		gr.mu.Lock()
		gr.records = append(gr.records, rec)
		gr.mu.Unlock()
	}
	return ac, finish, nil
}

func (gr *figmaMCPGrant) buildToolset(serverName, token string) (*protocol.CapabilityServer, error) {
	cs, err := protocol.NewCapabilityServer(serverName)
	if err != nil {
		return nil, err
	}
	cs.Token = token

	if err := cs.HandleTool(
		"parse_figma_url",
		"Parse a Figma design/file/board URL into its file_key and normalized node_id (e.g. 12-34 -> 12:34).",
		map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"url"},
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "Full Figma URL (e.g. https://www.figma.com/design/<file_key>/<name>?node-id=12-34)."},
			},
		},
		func(_ context.Context, a map[string]any) ([]byte, error) {
			raw, _ := a["url"].(string)
			parsed, err := ParseFigmaURL(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(parsed)
		},
	); err != nil {
		return nil, err
	}

	fileTargetProps := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"file_key": map[string]any{"type": "string", "description": "Figma file key (from the URL path /design/<file_key>/...). Optional when url is provided."},
			"url":      map[string]any{"type": "string", "description": "Full Figma URL; file_key and node-id are extracted automatically."},
		}
		for k, v := range extra {
			props[k] = v
		}
		return props
	}

	if err := cs.HandleTool(
		"get_file",
		"Fetch Figma file metadata, pages, top-level frames, and published component/style catalog.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": fileTargetProps(map[string]any{
				"depth": map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "description": "Document tree traversal depth (1 = pages only, 2 = top-level frames, max 4; default 2)."},
			}),
		},
		gr.handleGetFile,
	); err != nil {
		return nil, err
	}

	if err := cs.HandleTool(
		"get_design_context",
		"Fetch specific Figma frames/components/nodes and extract structured UI layout specs (dimensions, Flexbox/auto-layout, spacing, hex/rgba colors, strokes, border-radius, shadows, and typography).",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": fileTargetProps(map[string]any{
				"node_id":  map[string]any{"type": "string", "description": "Single node ID (e.g. 12:34 or 12-34). Extracted from url ?node-id=... if omitted."},
				"node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Multiple node IDs to inspect in one call."},
				"depth":    map[string]any{"type": "integer", "minimum": 1, "maximum": 6, "description": "Subtree depth to extract per node (default 3)."},
			}),
		},
		gr.handleGetDesignContext,
	); err != nil {
		return nil, err
	}

	if err := cs.HandleTool(
		"export_images",
		"Render one or more Figma frames/nodes as PNG, SVG, JPG, or PDF to compare against rendered UI screenshots. PNG and JPG renders come back as images you can look at; SVG and PDF as base64 bytes. Each also carries Figma's download URL, which a sandbox with network can fetch to a file (e.g. for grain screenshot --reference).",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": fileTargetProps(map[string]any{
				"node_id":  map[string]any{"type": "string", "description": "Single node ID to render (or extracted from url ?node-id=...)."},
				"node_ids": map[string]any{"type": "array", "maxItems": maxFigmaExportNodes, "items": map[string]any{"type": "string"}, "description": "Up to 10 node IDs to render."},
				"format":   map[string]any{"type": "string", "enum": []any{"png", "svg", "jpg", "pdf"}, "description": "Export format (default png)."},
				"scale":    map[string]any{"type": "number", "minimum": 0.5, "maximum": 4, "description": "Render scale factor from 0.5 to 4 (default 2)."},
			}),
		},
		gr.handleExportImages,
	); err != nil {
		return nil, err
	}

	if err := cs.HandleTool(
		"get_styles_and_variables",
		"Fetch published styles and local design tokens/variables defined in a Figma file.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           fileTargetProps(nil),
		},
		gr.handleGetStylesAndVariables,
	); err != nil {
		return nil, err
	}

	if err := cs.HandleTool(
		"get_comments",
		"List designer comments and annotations on a Figma file.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           fileTargetProps(nil),
		},
		gr.handleGetComments,
	); err != nil {
		return nil, err
	}

	return cs, nil
}

func (gr *figmaMCPGrant) handleGetFile(ctx context.Context, args map[string]any) ([]byte, error) {
	fileKey, _, err := resolveFileAndNodes(args)
	if err != nil {
		return nil, err
	}
	depth := 2
	if v, ok := intFromAny(args["depth"]); ok && v >= 1 && v <= 4 {
		depth = v
	}
	ac, finish, err := gr.checkAndReserve("get_file", fileKey, 0)
	if err != nil {
		return nil, err
	}
	q := url.Values{"depth": []string{strconv.Itoa(depth)}}
	body, status, err := ac.getJSON(ctx, "/v1/files/"+url.PathEscape(fileKey), q)
	if err == nil && (status < 200 || status >= 300) {
		err = fmt.Errorf("Figma /v1/files/%s returned HTTP %d: %s", fileKey, status, summarizeFigmaErrorBody(body))
	}
	finish(err)
	if err != nil {
		return nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decoding Figma file response: %w", err)
	}
	doc, _ := raw["document"].(map[string]any)
	out := map[string]any{
		"file_key":      fileKey,
		"name":          raw["name"],
		"last_modified": raw["lastModified"],
		"version":       raw["version"],
		"editor_type":   raw["editorType"],
		"pages":         extractFigmaOutline(doc, depth),
		"components":    summarizeNamedCatalog(raw["components"]),
		"styles":        summarizeNamedCatalog(raw["styles"]),
	}
	return json.Marshal(out)
}

func (gr *figmaMCPGrant) handleGetDesignContext(ctx context.Context, args map[string]any) ([]byte, error) {
	fileKey, nodes, err := resolveFileAndNodes(args)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("get_design_context requires at least one node ID (pass node_id, node_ids, or a url with ?node-id=...)")
	}
	depth := defaultFigmaNodeDepth
	if v, ok := intFromAny(args["depth"]); ok && v >= 1 && v <= 6 {
		depth = v
	}
	ac, finish, err := gr.checkAndReserve("get_design_context", fileKey, len(nodes))
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"ids":   []string{strings.Join(nodes, ",")},
		"depth": []string{strconv.Itoa(depth)},
	}
	body, status, err := ac.getJSON(ctx, "/v1/files/"+url.PathEscape(fileKey)+"/nodes", q)
	if err == nil && (status < 200 || status >= 300) {
		err = fmt.Errorf("Figma /v1/files/%s/nodes returned HTTP %d: %s", fileKey, status, summarizeFigmaErrorBody(body))
	}
	finish(err)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Name         string                    `json:"name"`
		LastModified string                    `json:"lastModified"`
		Nodes        map[string]map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decoding Figma nodes response: %w", err)
	}

	specs := make([]map[string]any, 0, len(nodes))
	for _, id := range nodes {
		entry := raw.Nodes[id]
		if entry == nil {
			specs = append(specs, map[string]any{"id": id, "missing": true})
			continue
		}
		doc, _ := entry["document"].(map[string]any)
		if doc == nil {
			specs = append(specs, map[string]any{"id": id, "missing": true})
			continue
		}
		specs = append(specs, extractDesignNodeSpec(doc, 0, depth))
	}
	return json.Marshal(map[string]any{
		"file_key":      fileKey,
		"file_name":     raw.Name,
		"last_modified": raw.LastModified,
		"nodes":         specs,
	})
}

func (gr *figmaMCPGrant) handleExportImages(ctx context.Context, args map[string]any) ([]byte, error) {
	fileKey, nodes, err := resolveFileAndNodes(args)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("export_images requires at least one node ID (pass node_id, node_ids, or a url with ?node-id=...)")
	}
	if len(nodes) > maxFigmaExportNodes {
		return nil, fmt.Errorf("at most %d nodes may be exported per call", maxFigmaExportNodes)
	}
	format, _ := args["format"].(string)
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "png"
	}
	switch format {
	case "png", "svg", "jpg", "pdf":
	default:
		return nil, fmt.Errorf("unsupported export format %q", format)
	}
	scale := 2.0
	if rawScale, ok := args["scale"]; ok {
		switch s := rawScale.(type) {
		case float64:
			scale = s
		case int:
			scale = float64(s)
		}
		if scale < 0.5 || scale > 4 {
			return nil, errors.New("scale must be between 0.5 and 4")
		}
	}

	ac, finish, err := gr.checkAndReserve("export_images", fileKey, len(nodes))
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"ids":    []string{strings.Join(nodes, ",")},
		"format": []string{format},
		"scale":  []string{strconv.FormatFloat(scale, 'f', -1, 64)},
	}
	body, status, err := ac.getJSON(ctx, "/v1/images/"+url.PathEscape(fileKey), q)
	if err == nil && (status < 200 || status >= 300) {
		err = fmt.Errorf("Figma /v1/images/%s returned HTTP %d: %s", fileKey, status, summarizeFigmaErrorBody(body))
	}
	if err != nil {
		finish(err)
		return nil, err
	}

	var imgResp struct {
		Err    string            `json:"err"`
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(body, &imgResp); err != nil {
		finish(err)
		return nil, fmt.Errorf("decoding Figma images response: %w", err)
	}
	if imgResp.Err != "" {
		err := fmt.Errorf("Figma image render failed: %s", imgResp.Err)
		finish(err)
		return nil, err
	}

	exported := make([]map[string]any, 0, len(nodes))
	for _, id := range nodes {
		imgURL := strings.TrimSpace(imgResp.Images[id])
		if imgURL == "" {
			exported = append(exported, map[string]any{"node_id": id, "missing": true})
			continue
		}
		data, ct, dlErr := gr.s.downloadExportedImage(ctx, imgURL)
		if dlErr != nil {
			finish(dlErr)
			return nil, fmt.Errorf("node %s: %w", id, dlErr)
		}
		img := map[string]any{
			"node_id":      id,
			"format":       format,
			"content_type": ct,
			"byte_length":  len(data),
			// Figma's own rendered-image URL (it expires after a while):
			// a sandbox with network can download it to a file, e.g. for
			// grain screenshot --reference.
			"url": imgURL,
		}
		// A raster render goes back as an MCP image the agent can look at,
		// not as base64 text in its context. Anything else -- SVG, PDF --
		// or a caller that takes no images carries its bytes in the JSON.
		if (format == "png" || format == "jpg") && protocol.ToolImage(ctx, ct, data) {
			img["image_content"] = true
		} else {
			img["content_base64"] = base64.StdEncoding.EncodeToString(data)
		}
		exported = append(exported, img)
	}
	finish(nil)
	return json.Marshal(map[string]any{
		"file_key": fileKey,
		"format":   format,
		"scale":    scale,
		"images":   exported,
	})
}

func (gr *figmaMCPGrant) handleGetStylesAndVariables(ctx context.Context, args map[string]any) ([]byte, error) {
	fileKey, _, err := resolveFileAndNodes(args)
	if err != nil {
		return nil, err
	}
	ac, finish, err := gr.checkAndReserve("get_styles_and_variables", fileKey, 0)
	if err != nil {
		return nil, err
	}
	stylesBody, stylesStatus, err := ac.getJSON(ctx, "/v1/files/"+url.PathEscape(fileKey)+"/styles", nil)
	if err == nil && (stylesStatus < 200 || stylesStatus >= 300) {
		err = fmt.Errorf("Figma /v1/files/%s/styles returned HTTP %d: %s", fileKey, stylesStatus, summarizeFigmaErrorBody(stylesBody))
	}
	if err != nil {
		finish(err)
		return nil, err
	}
	var stylesPayload map[string]any
	_ = json.Unmarshal(stylesBody, &stylesPayload)
	var styles any
	if meta, ok := stylesPayload["meta"].(map[string]any); ok {
		styles = meta["styles"]
	}

	out := map[string]any{
		"file_key": fileKey,
		"styles":   styles,
	}
	varsBody, varsStatus, varsErr := ac.getJSON(ctx, "/v1/files/"+url.PathEscape(fileKey)+"/variables/local", nil)
	if varsErr == nil && varsStatus >= 200 && varsStatus < 300 {
		var varsPayload map[string]any
		if json.Unmarshal(varsBody, &varsPayload) == nil {
			out["variables"] = varsPayload["meta"]
		}
	} else {
		out["variables_note"] = fmt.Sprintf("local variables unavailable (HTTP %d; Figma variables API requires file_variables:read scope)", varsStatus)
	}
	finish(nil)
	return json.Marshal(out)
}

func (gr *figmaMCPGrant) handleGetComments(ctx context.Context, args map[string]any) ([]byte, error) {
	fileKey, _, err := resolveFileAndNodes(args)
	if err != nil {
		return nil, err
	}
	ac, finish, err := gr.checkAndReserve("get_comments", fileKey, 0)
	if err != nil {
		return nil, err
	}
	body, status, err := ac.getJSON(ctx, "/v1/files/"+url.PathEscape(fileKey)+"/comments", nil)
	if err == nil && (status < 200 || status >= 300) {
		err = fmt.Errorf("Figma /v1/files/%s/comments returned HTTP %d: %s", fileKey, status, summarizeFigmaErrorBody(body))
	}
	finish(err)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Comments []struct {
			ID         string         `json:"id"`
			Message    string         `json:"message"`
			CreatedAt  string         `json:"created_at"`
			ResolvedAt string         `json:"resolved_at"`
			ParentID   string         `json:"parent_id"`
			User       map[string]any `json:"user"`
			ClientMeta map[string]any `json:"client_meta"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decoding Figma comments response: %w", err)
	}
	comments := make([]map[string]any, 0, len(payload.Comments))
	for _, c := range payload.Comments {
		author, _ := c.User["handle"].(string)
		item := map[string]any{
			"id":         c.ID,
			"author":     author,
			"message":    c.Message,
			"created_at": c.CreatedAt,
			"resolved":   c.ResolvedAt != "",
		}
		if c.ParentID != "" {
			item["parent_id"] = c.ParentID
		}
		if nodeID, _ := c.ClientMeta["node_id"].(string); nodeID != "" {
			item["node_id"] = nodeID
		}
		comments = append(comments, item)
	}
	return json.Marshal(map[string]any{
		"file_key": fileKey,
		"comments": comments,
	})
}

func summarizeFigmaErrorBody(body []byte) string {
	var e struct {
		Err     string `json:"err"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if strings.TrimSpace(e.Err) != "" {
			return strings.TrimSpace(e.Err)
		}
		if strings.TrimSpace(e.Message) != "" {
			return strings.TrimSpace(e.Message)
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func extractFigmaOutline(node map[string]any, maxDepth int) []map[string]any {
	if node == nil || maxDepth <= 0 {
		return nil
	}
	rawChildren, _ := node["children"].([]any)
	out := make([]map[string]any, 0, min(len(rawChildren), maxFigmaSpecChildren))
	for i, ch := range rawChildren {
		if i >= maxFigmaSpecChildren {
			break
		}
		cm, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{
			"id":   cm["id"],
			"name": cm["name"],
			"type": cm["type"],
		}
		if maxDepth > 1 {
			if sub := extractFigmaOutline(cm, maxDepth-1); len(sub) > 0 {
				entry["children"] = sub
			}
		}
		out = append(out, entry)
	}
	return out
}

func summarizeNamedCatalog(raw any) []map[string]any {
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 100 {
		keys = keys[:100]
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		em, _ := m[k].(map[string]any)
		out = append(out, map[string]any{
			"id":          k,
			"name":        em["name"],
			"description": em["description"],
			"style_type":  em["styleType"],
		})
	}
	return out
}

// extractDesignNodeSpec distills a Figma document node into CSS/UI-ready
// layout, spacing, color, border, shadow, and typography attributes.
func extractDesignNodeSpec(node map[string]any, depth, maxDepth int) map[string]any {
	spec := map[string]any{
		"id":   node["id"],
		"name": node["name"],
		"type": node["type"],
	}
	if visible, ok := node["visible"].(bool); ok && !visible {
		spec["visible"] = false
	}
	if bbox, ok := node["absoluteBoundingBox"].(map[string]any); ok {
		spec["bounds"] = map[string]any{
			"width":  roundNumber(bbox["width"]),
			"height": roundNumber(bbox["height"]),
		}
	}
	if mode, _ := node["layoutMode"].(string); mode != "" && mode != "NONE" {
		layout := map[string]any{
			"direction": strings.ToLower(mode), // horizontal or vertical
		}
		if v, ok := node["itemSpacing"].(float64); ok && v != 0 {
			layout["gap"] = roundNumber(v)
		}
		pt, _ := node["paddingTop"].(float64)
		pr, _ := node["paddingRight"].(float64)
		pb, _ := node["paddingBottom"].(float64)
		pl, _ := node["paddingLeft"].(float64)
		if pt != 0 || pr != 0 || pb != 0 || pl != 0 {
			layout["padding"] = []any{roundNumber(pt), roundNumber(pr), roundNumber(pb), roundNumber(pl)}
		}
		if v, _ := node["primaryAxisAlignItems"].(string); v != "" {
			layout["main_align"] = v
		}
		if v, _ := node["counterAxisAlignItems"].(string); v != "" {
			layout["cross_align"] = v
		}
		if v, _ := node["layoutWrap"].(string); v != "" && v != "NO_WRAP" {
			layout["wrap"] = v
		}
		spec["auto_layout"] = layout
	}
	if hSize, _ := node["layoutSizingHorizontal"].(string); hSize != "" {
		spec["sizing_horizontal"] = hSize
	}
	if vSize, _ := node["layoutSizingVertical"].(string); vSize != "" {
		spec["sizing_vertical"] = vSize
	}
	if fills := extractPaints(node["fills"]); len(fills) > 0 {
		spec["fills"] = fills
	}
	if strokes := extractPaints(node["strokes"]); len(strokes) > 0 {
		spec["strokes"] = strokes
		if sw, ok := node["strokeWeight"].(float64); ok && sw > 0 {
			spec["stroke_weight"] = roundNumber(sw)
		}
	}
	if cr, ok := node["cornerRadius"].(float64); ok && cr > 0 {
		spec["border_radius"] = roundNumber(cr)
	} else if radii, ok := node["rectangleCornerRadii"].([]any); ok && len(radii) == 4 {
		spec["border_radii"] = radii
	}
	if effects := extractEffects(node["effects"]); len(effects) > 0 {
		spec["effects"] = effects
	}
	if chars, ok := node["characters"].(string); ok {
		if len(chars) > maxFigmaTextPreview {
			chars = chars[:maxFigmaTextPreview] + "..."
		}
		spec["text"] = chars
	}
	if style, ok := node["style"].(map[string]any); ok {
		typo := map[string]any{}
		for _, k := range []string{"fontFamily", "fontSize", "fontWeight", "lineHeightPx", "letterSpacing", "textAlignHorizontal"} {
			if v, exists := style[k]; exists && v != nil {
				typo[k] = roundNumber(v)
			}
		}
		if len(typo) > 0 {
			spec["typography"] = typo
		}
	}
	if compID, _ := node["componentId"].(string); compID != "" {
		spec["component_id"] = compID
	}

	limit := min(maxDepth, maxFigmaSpecDepth)
	if depth < limit {
		if rawChildren, ok := node["children"].([]any); ok && len(rawChildren) > 0 {
			children := make([]map[string]any, 0, min(len(rawChildren), maxFigmaSpecChildren))
			for i, ch := range rawChildren {
				if i >= maxFigmaSpecChildren {
					spec["children_truncated"] = len(rawChildren) - maxFigmaSpecChildren
					break
				}
				if cm, ok := ch.(map[string]any); ok {
					children = append(children, extractDesignNodeSpec(cm, depth+1, maxDepth))
				}
			}
			if len(children) > 0 {
				spec["children"] = children
			}
		}
	}
	return spec
}

func extractPaints(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		pm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if vis, ok := pm["visible"].(bool); ok && !vis {
			continue
		}
		kind, _ := pm["type"].(string)
		if kind == "SOLID" {
			if col, ok := pm["color"].(map[string]any); ok {
				opacity := 1.0
				if op, ok := pm["opacity"].(float64); ok {
					opacity = op
				}
				out = append(out, formatFigmaColor(col, opacity))
			}
		} else if kind != "" {
			out = append(out, strings.ToLower(kind))
		}
	}
	return out
}

func extractEffects(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		em, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if vis, ok := em["visible"].(bool); ok && !vis {
			continue
		}
		kind, _ := em["type"].(string)
		if kind == "DROP_SHADOW" || kind == "INNER_SHADOW" {
			off, _ := em["offset"].(map[string]any)
			ox, _ := off["x"].(float64)
			oy, _ := off["y"].(float64)
			rad, _ := em["radius"].(float64)
			col, _ := em["color"].(map[string]any)
			prefix := ""
			if kind == "INNER_SHADOW" {
				prefix = "inset "
			}
			out = append(out, fmt.Sprintf("%s%.0fpx %.0fpx %.0fpx %s", prefix, ox, oy, rad, formatFigmaColor(col, 1.0)))
		} else if kind != "" {
			out = append(out, strings.ToLower(kind))
		}
	}
	return out
}

func formatFigmaColor(col map[string]any, paintOpacity float64) string {
	if col == nil {
		return ""
	}
	rf, _ := col["r"].(float64)
	gf, _ := col["g"].(float64)
	bf, _ := col["b"].(float64)
	af := 1.0
	if a, ok := col["a"].(float64); ok {
		af = a
	}
	alpha := af * paintOpacity
	r := clampByte(rf)
	g := clampByte(gf)
	b := clampByte(bf)
	if alpha >= 0.999 {
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %.2f)", r, g, b, alpha)
}

func clampByte(v float64) int {
	n := int(math.Round(v * 255))
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return n
}

func roundNumber(v any) any {
	f, ok := v.(float64)
	if !ok {
		return v
	}
	return math.Round(f*100) / 100
}
