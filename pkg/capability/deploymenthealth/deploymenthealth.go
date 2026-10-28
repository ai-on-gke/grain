// Package deploymenthealth grants bounded, read-only controller diagnostics.
package deploymenthealth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

const CapabilityName = "deployment-health"
const maxStateBytes = 256 * 1024
const maxStateFiles = 200

type Provider struct {
	model.BaseCapability
}

type ProviderConfig struct{}

func New() *Provider                           { return &Provider{} }
func NewWithConfig(_ ProviderConfig) *Provider { return &Provider{} }
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name: CapabilityName, Label: "Deployment health", Source: model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: "Read bounded controller-side host and sandbox health and the local synchronized deployment-state snapshot, without host shell or sandbox network access.",
	}
}

type Reader interface {
	DeploymentHealth(context.Context) (any, error)
}
type Config struct {
	TaskID, StateDir string
	Reader           Reader
	Redact           func(string) string
	Log              func(string, ...any)
}

func Tools(cfg Config) []mcp.Tool {
	if cfg.Log == nil {
		cfg.Log = log.Printf
	}
	return []mcp.Tool{healthTool(cfg), stateTool(cfg)}
}

func healthTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name:        "read_deployment_health",
		Description: "Read current controller host RAM, swap, disks and load, plus bounded live sandbox/VM counts and per-sandbox resource readings. Read-only; no host shell is exposed. Errors distinguish controller connectivity, authorization, and availability.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
		Handler: func(ctx context.Context, _ map[string]any) mcp.Result {
			if cfg.Redact == nil {
				return mcp.Result{Text: "Deployment health read refused: controller secrets could not be loaded for redaction.", IsError: true}
			}
			if cfg.Reader == nil {
				return mcp.Result{Text: "Deployment health is unavailable: this run has no controller connection.", IsError: true}
			}
			v, err := cfg.Reader.DeploymentHealth(ctx)
			if err != nil {
				audit(cfg, "grain deployment-health: task=%q health failed: %v", cfg.TaskID, err)
				return mcp.Result{Text: "Deployment health read failed: " + err.Error(), IsError: true}
			}
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return mcp.Result{Text: "Encoding deployment health failed: " + err.Error(), IsError: true}
			}
			out := redact(cfg, string(b))
			audit(cfg, "grain deployment-health: task=%q health bytes=%d", cfg.TaskID, len(out))
			return mcp.Result{Text: out}
		},
	}
}

func stateTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name:        "read_deployment_state",
		Description: "Read one JSON table from grain's controller-side synchronized state repository. This does not clone, use DNS, or use sandbox credentials. Paths are restricted to tables/*.json and responses are bounded.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "A tables/*.json path. Omit to list available tables."},
		}},
		Handler: func(_ context.Context, args map[string]any) mcp.Result { return readState(cfg, args) },
	}
}

func readState(cfg Config, args map[string]any) mcp.Result {
	path, _ := args["path"].(string)
	if cfg.Redact == nil {
		return mcp.Result{Text: "Deployment state read refused: controller secrets could not be loaded for redaction.", IsError: true}
	}
	if cfg.StateDir == "" {
		return mcp.Result{Text: "Deployment state is unavailable: the controller has no local synchronized state repository.", IsError: true}
	}
	if path == "" {
		matches, err := filepath.Glob(filepath.Join(cfg.StateDir, "tables", "*.json"))
		if err != nil {
			return mcp.Result{Text: err.Error(), IsError: true}
		}
		// A symlink is neither listed nor read: grain writes none into
		// tables/, and one there could lead anywhere on the controller.
		matches = slices.DeleteFunc(matches, func(m string) bool {
			info, err := os.Lstat(m)
			return err != nil || !info.Mode().IsRegular()
		})
		sort.Strings(matches)
		truncated := len(matches) > maxStateFiles
		if truncated {
			matches = matches[:maxStateFiles]
		}
		for i := range matches {
			matches[i], _ = filepath.Rel(cfg.StateDir, matches[i])
		}
		b, _ := json.Marshal(map[string]any{"files": matches, "truncated": truncated})
		audit(cfg, "grain deployment-health: task=%q state list files=%d", cfg.TaskID, len(matches))
		return mcp.Result{Text: string(b)}
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(clean, "tables/") || strings.Count(clean, "/") != 1 || !strings.HasSuffix(clean, ".json") {
		audit(cfg, "grain deployment-health: task=%q state refused path=%q", cfg.TaskID, path)
		return mcp.Result{Text: "State path is not authorized: only tables/*.json may be read.", IsError: true}
	}
	f := filepath.Join(cfg.StateDir, filepath.FromSlash(clean))
	info, err := os.Lstat(f)
	if err != nil {
		return mcp.Result{Text: fmt.Sprintf("Deployment state repository availability error for %q: %v", clean, err), IsError: true}
	}
	if !info.Mode().IsRegular() {
		audit(cfg, "grain deployment-health: task=%q state refused non-regular path=%q", cfg.TaskID, path)
		return mcp.Result{Text: "State path is not authorized: only regular tables/*.json files may be read.", IsError: true}
	}
	if info.Size() > maxStateBytes {
		return mcp.Result{Text: fmt.Sprintf("State table %q is %d bytes, over the per-call limit of %d; the read was refused without partial output.", clean, info.Size(), maxStateBytes), IsError: true}
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return mcp.Result{Text: fmt.Sprintf("Deployment state repository availability error for %q: %v", clean, err), IsError: true}
	}
	out := redact(cfg, string(b))
	audit(cfg, "grain deployment-health: task=%q state path=%q bytes=%d", cfg.TaskID, clean, len(out))
	return mcp.Result{Text: out}
}

func audit(cfg Config, format string, args ...any) {
	if cfg.Log != nil {
		cfg.Log(format, args...)
	}
}

func redact(cfg Config, s string) string {
	if cfg.Redact != nil {
		return cfg.Redact(s)
	}
	return s
}
