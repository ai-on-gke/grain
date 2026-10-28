package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/procgroup"
)

// Efforts is a fallback for an unavailable catalog, not a model's
// supported choices. Codex advertises effort strings dynamically.
func Efforts() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
}

var effortID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ValidEffort reports whether effort is empty or shaped like an effort id
// codex could advertise. It checks the shape only, not any model's list.
func ValidEffort(effort string) bool { return effort == "" || effortID.MatchString(effort) }

// ReasoningEffort is one effort a model/list entry says the model supports.
type ReasoningEffort struct {
	Effort      string `json:"reasoningEffort"`
	Description string `json:"description"`
}

// Model is one entry of app-server's model/list answer.
type Model struct {
	ID            string            `json:"id"`
	Model         string            `json:"model"`
	Label         string            `json:"displayName"`
	Hidden        bool              `json:"hidden"`
	DefaultEffort string            `json:"defaultReasoningEffort"`
	Efforts       []ReasoningEffort `json:"supportedReasoningEfforts"`
}

// catalogTimeout bounds a probe of the codex in this container: one
// subprocess talking over a pipe, reached from an HTTP handler.
const catalogTimeout = 30 * time.Second

// Catalog uses the installed CLI's stable app-server protocol. It starts
// no model turn and installs no MCP servers. The private home uses the
// same authentication and no-environment policy as agent runs, staged
// through an agent.Host so that catalogHome is the one implementation
// both a probe and a run go through.
func Catalog(ctx context.Context, binary, apiKey, authFile string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	home, cleanup, err := catalogHome(ctx, agent.LocalHost{}, authFile)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, binary, "app-server")
	procgroup.Prepare(cmd)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), catalogEnv(home, apiKey)...)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	defer func() { cancel(); stdin.Close(); stdout.Close(); _ = cmd.Wait() }()
	return readCatalog(stdin, stdout)
}

// catalogHome stages the private CODEX_HOME a probe runs under, on
// whichever host it runs on -- the same writeHome a run's own home goes
// through, with no MCP server named in the config because a listing
// starts no turn and calls no tool.
func catalogHome(ctx context.Context, host agent.Host, authFile string) (home string, cleanup func(), err error) {
	f := &Framework{host: host, authFile: authFile}
	config, err := f.configTOML(nil)
	if err != nil {
		return "", nil, err
	}
	// No task id, so no session store: a model listing has no
	// conversation to keep and no task to keep one for, and gets the
	// temp home every run got before the store existed (openHome).
	home, _, cleanup, err = f.writeHome(ctx, config, "")
	return home, cleanup, err
}

// catalogEnv is what the probe adds to app-server's environment: its
// private home, the same no-environment policy a run gets, and the key
// in the environment rather than in argv.
func catalogEnv(home, apiKey string) []string {
	env := []string{"CODEX_HOME=" + home, "CODEX_EXEC_SERVER_URL=none"}
	if apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+apiKey)
	}
	return env
}

// catalogPage is one model/list answer: the models on it, and the cursor
// that reaches the next page if there is one.
type catalogPage struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// rpcResponse is one line of app-server's stdout that answers a request.
type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (r rpcResponse) decode(method string, result any) error {
	if len(r.Error) != 0 && string(r.Error) != "null" {
		return fmt.Errorf("codex: %s failed while reading model catalog", method)
	}
	return json.Unmarshal(r.Result, result)
}

func catalogRequest(id int, method string, params any) map[string]any {
	return map[string]any{"id": id, "method": method, "params": params}
}

func handshakeParams() map[string]any {
	return map[string]any{"clientInfo": map[string]string{"name": "grain", "version": "1"}}
}

func listParams(cursor *string) map[string]any {
	return map[string]any{"limit": 100, "includeHidden": false, "cursor": cursor}
}

func newCatalogScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	return scanner
}

// readCatalog drives one long-lived app-server over its own pipes: the
// handshake, then a request per page, each written only once the one
// before it has been answered.
func readCatalog(w io.Writer, r io.Reader) ([]Model, error) {
	encoder := json.NewEncoder(w)
	scanner := newCatalogScanner(r)
	id := 0
	call := func(method string, params any, result any) error {
		id++
		if err := encoder.Encode(catalogRequest(id, method, params)); err != nil {
			return err
		}
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var response rpcResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				return fmt.Errorf("codex: invalid model catalog response: %w", err)
			}
			if response.ID == nil || *response.ID != id {
				continue
			}
			return response.decode(method, result)
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("codex: model catalog connection closed")
	}
	var initialized json.RawMessage
	if err := call("initialize", handshakeParams(), &initialized); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	return collectCatalog(func(cursor *string) (catalogPage, error) {
		var page catalogPage
		err := call("model/list", listParams(cursor), &page)
		return page, err
	})
}

// collectCatalog walks the pages of one listing, each fetched by next
// (readCatalog asks one long-lived app-server repeatedly). Hidden models are dropped here rather than trusted
// to includeHidden, and a cursor a server repeats ends the walk rather
// than looping it.
func collectCatalog(next func(cursor *string) (catalogPage, error)) ([]Model, error) {
	var models []Model
	var cursor *string
	seen := make(map[string]bool)
	for page := 0; page < 100; page++ {
		result, err := next(cursor)
		if err != nil {
			return nil, err
		}
		for _, m := range result.Data {
			if m.Hidden {
				continue
			}
			if m.Model != "" {
				m.ID = m.Model
			}
			if m.ID == "" {
				return nil, fmt.Errorf("codex: model catalog contains an empty model ID")
			}
			models = append(models, m)
		}
		cursor = result.NextCursor
		if cursor == nil || *cursor == "" {
			return models, nil
		}
		if seen[*cursor] {
			return nil, fmt.Errorf("codex: model catalog repeated its pagination cursor")
		}
		seen[*cursor] = true
	}
	return nil, fmt.Errorf("codex: model catalog exceeded pagination limit")
}
