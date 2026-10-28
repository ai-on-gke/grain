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

// sandboxcli_cap.go is grain cap: the sandbox-callable tools of the
// external capability servers granted to this run (docs/capabilities-spec.md
// §5.2.6), reached through the sandbox proxy's /cap/ -- GET /cap/ for
// the listing, GET /cap/<server> for one server's tools and their input
// schemas, which each tool's flags are generated from, and
// /cap/<server>/mcp for the call itself. A model-only tool is listed by
// name only and the proxy refuses any call to it.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// capTimeout bounds one grain cap call, initialize to result.
const capTimeout = 10 * time.Minute

type capToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type capServerInfo struct {
	Name           string        `json:"name"`
	Grant          string        `json:"grant"`
	Tools          []capToolInfo `json:"tools"`
	ModelOnlyTools []string      `json:"modelOnlyTools"`
}

func (s capServerInfo) tool(name string) (capToolInfo, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return capToolInfo{}, false
}

// capCommand runs grain cap with args, everything after "cap".
func (c *sandboxCLI) capCommand(args []string) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return c.help("cap")
	}
	if args[0] == "list" {
		return c.capList(args[1:])
	}
	server := args[0]
	if len(args) == 1 || isHelpFlag(args[1]) {
		info, err := c.capServer(server)
		if err != nil {
			return err
		}
		c.printCapServer(info)
		return nil
	}
	toolName, rest := args[1], args[2:]
	info, err := c.capServer(server)
	if err != nil {
		return err
	}
	tool, ok := info.tool(toolName)
	if !ok {
		for _, m := range info.ModelOnlyTools {
			if m == toolName {
				return fmt.Errorf("%s's tool %s is model-only: it cannot be called from the sandbox, only as an MCP tool by the agent itself", server, toolName)
			}
		}
		return fmt.Errorf("capability server %s has no tool %q: run 'grain cap %s --help'", server, toolName, server)
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			fmt.Fprint(c.out, capToolHelp(server, tool))
			return nil
		}
	}
	arguments, err := capArguments(tool.InputSchema, rest)
	if err != nil {
		return fmt.Errorf("%s %s: %v (run 'grain cap %s %s --help')", server, toolName, err, server, toolName)
	}
	return c.capCall(server, toolName, arguments)
}

func (c *sandboxCLI) capList(args []string) error {
	resp, err := c.do("/cap/", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := c.capBody(resp)
	if err != nil {
		return err
	}
	for _, a := range args {
		if a == "--json" {
			_, err := c.out.Write(body)
			return err
		}
	}
	var list struct {
		Servers []capServerInfo `json:"servers"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decoding the proxy's capability listing: %w", err)
	}
	if len(list.Servers) == 0 {
		fmt.Fprintln(c.out, "No capability tools are callable from this sandbox: this run's grants serve none, or only model-only ones.")
		return nil
	}
	for i, s := range list.Servers {
		if i > 0 {
			fmt.Fprintln(c.out)
		}
		c.printCapServer(s)
	}
	return nil
}

func (c *sandboxCLI) printCapServer(s capServerInfo) {
	fmt.Fprintf(c.out, "%s", s.Name)
	if s.Grant != "" {
		fmt.Fprintf(c.out, "  (%s)", s.Grant)
	}
	fmt.Fprintln(c.out)
	for _, t := range s.Tools {
		fmt.Fprintf(c.out, "  grain cap %s %-24s %s\n", s.Name, t.Name, capFirstLine(t.Description))
	}
	if len(s.ModelOnlyTools) > 0 {
		fmt.Fprintf(c.out, "  model-only, not callable here: %s\n", strings.Join(s.ModelOnlyTools, ", "))
	}
}

func capFirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 100 {
		s = s[:97] + "..."
	}
	return s
}

// capServer is GET /cap/<server>.
func (c *sandboxCLI) capServer(server string) (capServerInfo, error) {
	resp, err := c.do("/cap/"+url.PathEscape(server), nil)
	if err != nil {
		return capServerInfo{}, err
	}
	defer resp.Body.Close()
	body, err := c.capBody(resp)
	if err != nil {
		return capServerInfo{}, err
	}
	var info capServerInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return capServerInfo{}, fmt.Errorf("decoding capability server %s: %w", server, err)
	}
	return info, nil
}

// capBody is resp's body, or the proxy's refusal printed and reported.
func (c *sandboxCLI) capBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(body)))
		return nil, errSandboxReported
	}
	return body, nil
}

// capCall calls tool on server through /cap/<server>/mcp and prints its
// result: indented when it is JSON, as it is otherwise.
func (c *sandboxCLI) capCall(server, tool string, arguments map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), capTimeout)
	defer cancel()
	headers := map[string]string{}
	if c.token != "" {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+c.token))
	}
	client, err := protocol.Dial(ctx, protocol.ServerConfig{
		ServerURL: c.base + "/cap/" + url.PathEscape(server) + "/mcp", Headers: headers,
	}, protocol.DialOptions{HTTPClient: c.client})
	if err != nil {
		return fmt.Errorf("reaching capability server %s: %w", server, err)
	}
	defer client.Close()
	raw, err := client.CallTool(ctx, tool, arguments)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		buf.WriteByte('\n')
		_, err = c.out.Write(buf.Bytes())
		return err
	}
	_, err = fmt.Fprintln(c.out, string(raw))
	return err
}

// capProperty is one input property, as a flag.
type capProperty struct {
	name     string
	schema   map[string]any
	required bool
}

func (p capProperty) kind() string {
	switch t := p.schema["type"].(type) {
	case string:
		return t
	case []any:
		// ["string", "null"] and the like: the first non-null type.
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

func capProperties(schema map[string]any) []capProperty {
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	var out []capProperty
	for name, raw := range props {
		s, _ := raw.(map[string]any)
		out = append(out, capProperty{name: name, schema: s, required: required[name]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].required != out[j].required {
			return out[i].required
		}
		return out[i].name < out[j].name
	})
	return out
}

// capToolHelp is a tool's --help, its flags generated from its
// inputSchema.
func capToolHelp(server string, t capToolInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "grain cap %s %s [flags | --json '<args>']\n\n", server, t.Name)
	if d := strings.TrimSpace(t.Description); d != "" {
		b.WriteString(d + "\n\n")
	}
	props := capProperties(t.InputSchema)
	if len(props) > 0 {
		b.WriteString("Flags:\n")
	}
	for _, p := range props {
		flag := "--" + p.name
		switch k := p.kind(); k {
		case "boolean":
		case "array":
			item := "value"
			if items, ok := p.schema["items"].(map[string]any); ok {
				if s, ok := items["type"].(string); ok {
					item = s
				}
			}
			flag += " <" + item + ">..."
		case "":
			flag += " <json>"
		default:
			flag += " <" + k + ">"
		}
		var notes []string
		if p.required {
			notes = append(notes, "required")
		}
		if enum, ok := p.schema["enum"].([]any); ok && len(enum) > 0 {
			var vals []string
			for _, v := range enum {
				vals = append(vals, fmt.Sprint(v))
			}
			notes = append(notes, "one of "+strings.Join(vals, "|"))
		}
		if def, ok := p.schema["default"]; ok {
			notes = append(notes, fmt.Sprintf("default %v", def))
		}
		desc := capFirstLine(fmt.Sprint(p.schema["description"]))
		if p.schema["description"] == nil {
			desc = ""
		}
		if len(notes) > 0 {
			desc = strings.TrimSpace(desc + " (" + strings.Join(notes, "; ") + ")")
		}
		fmt.Fprintf(&b, "  %-32s %s\n", flag, desc)
	}
	b.WriteString("  --json '<object>'                The whole arguments object as JSON; flags given beside it override its keys\n")
	b.WriteString("\nAn array flag may be repeated or given a JSON array; an object, or a property of no declared type, takes JSON.\n")
	return b.String()
}

// capArguments builds a tools/call's arguments from flags, each converted
// to its property's declared type, on top of --json's object.
func capArguments(schema map[string]any, args []string) (map[string]any, error) {
	props := map[string]capProperty{}
	for _, p := range capProperties(schema) {
		props[p.name] = p
	}
	lookup := func(flag string) (capProperty, bool) {
		if p, ok := props[flag]; ok {
			return p, true
		}
		p, ok := props[strings.ReplaceAll(flag, "-", "_")]
		return p, ok
	}
	out := map[string]any{}
	var base map[string]any
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || len(a) < 3 {
			return nil, fmt.Errorf("unexpected argument %q: every argument is a --flag", a)
		}
		name, value, hasValue := strings.Cut(a[2:], "=")
		if name == "json" {
			if !hasValue {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("--json needs a value")
				}
				i++
				value = args[i]
			}
			if err := json.Unmarshal([]byte(value), &base); err != nil || base == nil {
				return nil, fmt.Errorf("--json must be a JSON object: %v", err)
			}
			continue
		}
		p, ok := lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
		if p.kind() == "boolean" && !hasValue {
			out[p.name] = true
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s needs a value", name)
			}
			i++
			value = args[i]
		}
		v, err := capConvert(p.schema, value)
		if err != nil {
			return nil, fmt.Errorf("--%s: %v", name, err)
		}
		if p.kind() == "array" {
			if list, ok := v.([]any); ok && strings.HasPrefix(strings.TrimSpace(value), "[") {
				out[p.name] = list
				continue
			}
			prev, _ := out[p.name].([]any)
			out[p.name] = append(prev, v.([]any)...)
			continue
		}
		out[p.name] = v
	}
	merged := map[string]any{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range out {
		merged[k] = v
	}
	var missing []string
	for _, p := range capProperties(schema) {
		if _, ok := merged[p.name]; p.required && !ok {
			missing = append(missing, "--"+p.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required %s", strings.Join(missing, ", "))
	}
	return merged, nil
}

// capConvert is value as schema's type. An array's single value is
// returned as a one-element list (or the whole list, given JSON).
func capConvert(schema map[string]any, value string) (any, error) {
	p := capProperty{schema: schema}
	switch p.kind() {
	case "string":
		return value, nil
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", value)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", value)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", value)
		}
		return b, nil
	case "array":
		if strings.HasPrefix(strings.TrimSpace(value), "[") {
			var list []any
			if err := json.Unmarshal([]byte(value), &list); err != nil {
				return nil, fmt.Errorf("not a JSON array: %v", err)
			}
			return list, nil
		}
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			items = map[string]any{"type": "string"}
		}
		v, err := capConvert(items, value)
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	default:
		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			if p.kind() == "" {
				// A property of no declared type takes a bare word as
				// the string it reads as.
				return value, nil
			}
			return nil, fmt.Errorf("not JSON: %v", err)
		}
		return v, nil
	}
}
