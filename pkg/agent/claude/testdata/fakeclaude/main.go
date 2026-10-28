// Command fakeclaude stands in for the claude CLI inside a test image
// (container_docker_test.go). It does what a real claude run does with
// grain's MCP server and nothing else: reads the --mcp-config grain
// staged, calls one tool over the HTTP endpoint it names, and writes a
// stream-json transcript naming what the tool returned. A prompt of
// "sleep" makes it hang until it is killed, for the cancellation test;
// "--probe FILE" prints FILE, for the bind-mount preflight.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "--probe" {
		b, err := os.ReadFile(args[1])
		if err != nil {
			fail(err)
		}
		os.Stdout.Write(b)
		return
	}
	var configPath string
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			configPath = args[i+1]
		}
	}
	prompt, _ := io.ReadAll(os.Stdin)
	fmt.Println(`{"type":"system","subtype":"init","session_id":"sess-container"}`)
	if strings.TrimSpace(string(prompt)) == "sleep" {
		time.Sleep(10 * time.Minute)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		fail(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fail(err)
	}
	server, ok := cfg.MCPServers["grain-sandbox"]
	if !ok || server.Type != "http" {
		fail(fmt.Errorf("--mcp-config names no http grain-sandbox server: %s", raw))
	}
	call := func(id int, method string, params any) json.RawMessage {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range server.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fail(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		var r struct {
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(out, &r); err != nil || r.Error != nil {
			fail(fmt.Errorf("%s: %d %s", method, resp.StatusCode, out))
		}
		return r.Result
	}
	call(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "fakeclaude", "version": "0"}})
	file := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(prompt)), "read "))
	res := call(2, "tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"file_path": file}})
	var tool struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(res, &tool)
	var text []string
	for _, c := range tool.Content {
		text = append(text, c.Text)
	}
	result := fmt.Sprintf("tool said %q; uid=%d home=%s token=%t", strings.Join(text, ""), os.Getuid(),
		os.Getenv("HOME"), os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "tok-in-env")
	line, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": result,
		"session_id": "sess-container", "num_turns": 1})
	fmt.Println(string(line))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakeclaude:", err)
	os.Exit(1)
}
