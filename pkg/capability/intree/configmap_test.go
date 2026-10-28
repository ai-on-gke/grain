package intree

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

func TestConfigMapStartsUnconfiguredAndConfiguresMaps(t *testing.T) {
	s, err := NewConfigMap(ConfigMapConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Before configuration, it attaches with its configure schema and zero
	// methods, so the registry marks the built-in needs-config.
	unconfigured, err := attachConfigured(t, s, nil, nil)
	if err != nil {
		t.Fatalf("unconfigured attach: %v", err)
	}
	if len(unconfigured.Methods) != 0 {
		t.Errorf("unconfigured methods = %v, want empty", unconfigured.Methods)
	}
	if got := protocol.SecretFields(unconfigured.ConfigSchema); !reflect.DeepEqual(got, []string{"secret_values"}) {
		t.Errorf("secret fields = %v, want [secret_values]", got)
	}

	s2, err := NewConfigMap(ConfigMapConfig{})
	if err != nil {
		t.Fatal(err)
	}
	const superSecret = "npm-auth-token-super-secret-value-999"
	ps, err := attachConfigured(t, s2, map[string]any{
		"config_maps": []any{
			map[string]any{
				"name":        "my-tools",
				"description": "Tool settings and a helper script.",
				"data": map[string]any{
					"settings.toml": map[string]any{"content": "[user]\nname = Grain Agent\n"},
					"run.sh":        map[string]any{"content": "#!/bin/sh\necho ok\n", "executable": true},
					"gitconfig":     map[string]any{"content": "[core]\n", "path": "/home/debian/.gitconfig-extra", "mode": "0640"},
				},
			},
			map[string]any{
				"name": "app",
				"dir":  "/home/debian/.config/app",
				"data": map[string]any{
					"a.conf": map[string]any{"content": "a=1\n"},
					"b.conf": map[string]any{"content": "b=2\n", "mode": "0600"},
				},
			},
		},
		"secrets": []any{
			map[string]any{
				"name":        "deploy-creds",
				"description": "Deploy credentials.",
				"keys": map[string]any{
					"key.json": map[string]any{"mode": "0400"},
					"token":    map[string]any{},
					".npmrc":   map[string]any{"path": "/home/debian/.npmrc"},
				},
			},
		},
		"secret_values": "$secret:cm-secrets",
	}, map[string]string{
		"cm-secrets": `{"deploy-creds/key.json":"{\"k\":1}","deploy-creds/token":"` + superSecret + `","deploy-creds/.npmrc":"//registry.npmjs.org/:_authToken=` + superSecret + `\n","stale/old":"ignored"}`,
	})
	if err != nil {
		t.Fatalf("configured attach: %v", err)
	}
	if ps.Methods["config-map"] == nil || ps.Methods["config-map"].Releasable {
		t.Fatalf("config-map method = %+v, want stateless config-map", ps.Methods["config-map"])
	}

	// Minting with nil args places every key of every map.
	all := mint(t, ps, "config-map", "e1-1-0", nil)
	if all.Status != protocol.StatusOK || len(all.Placements) != 8 {
		t.Fatalf("mint all = %+v (placements=%d, want 8)", all, len(all.Placements))
	}
	for path, want := range map[string]struct{ mode, content string }{
		"/home/debian/.config-map/my-tools/settings.toml": {"0644", "[user]\nname = Grain Agent\n"},
		"/home/debian/.config-map/my-tools/run.sh":        {"0755", "#!/bin/sh\necho ok\n"},
		"/home/debian/.gitconfig-extra":                   {"0640", "[core]\n"},
		"/home/debian/.config/app/a.conf":                 {"0644", "a=1\n"},
		"/home/debian/.config/app/b.conf":                 {"0600", "b=2\n"},
		"/home/debian/.config-map/deploy-creds/key.json":  {"0400", `{"k":1}`},
		"/home/debian/.config-map/deploy-creds/token":     {"0600", superSecret},
		"/home/debian/.npmrc":                             {"0600", "//registry.npmjs.org/:_authToken=" + superSecret + "\n"},
	} {
		if p := placement(t, all, path); p.Mode != want.mode || p.Content != want.content {
			t.Errorf("placement %s = %+v, want mode %s content %q", path, p, want.mode, want.content)
		}
	}

	// PromptSection lists each map once, with its directory and key
	// names, and never a secret value.
	for _, want := range []string{
		"- config map my-tools in /home/debian/.config-map/my-tools: gitconfig at /home/debian/.gitconfig-extra (0640), run.sh (0755), settings.toml (0644) -- Tool settings and a helper script.",
		"- config map app in /home/debian/.config/app: a.conf (0644), b.conf (0600)",
		"- secret map deploy-creds in /home/debian/.config-map/deploy-creds: .npmrc at /home/debian/.npmrc (0600), key.json (0400), token (0600) -- Deploy credentials.",
	} {
		if !strings.Contains(all.PromptSection, want) {
			t.Errorf("PromptSection missing %q:\n%s", want, all.PromptSection)
		}
	}
	if strings.Contains(all.PromptSection, superSecret) {
		t.Errorf("PromptSection leaked secret content:\n%s", all.PromptSection)
	}

	// A grant naming one map places every key of it, and no other map's.
	onlySecret := mint(t, ps, "config-map", "e1-2-0", map[string]any{"secrets": []any{"deploy-creds"}})
	if len(onlySecret.Placements) != 3 {
		t.Errorf("secret map grant = %+v, want its 3 keys", onlySecret.Placements)
	}
	onlyCM := mint(t, ps, "config-map", "e1-3-0", map[string]any{"config_maps": []any{"app"}})
	if got := placementPaths(onlyCM); !reflect.DeepEqual(got, []string{"/home/debian/.config/app/a.conf", "/home/debian/.config/app/b.conf"}) {
		t.Errorf("config map grant placed %v, want app's two files", got)
	}
	// keys narrows the chosen maps to a subset, like a k8s volume's items.
	subset := mint(t, ps, "config-map", "e1-4-0", map[string]any{"keys": []any{"my-tools/run.sh", "deploy-creds/token"}})
	if got := placementPaths(subset); !reflect.DeepEqual(got, []string{"/home/debian/.config-map/my-tools/run.sh", "/home/debian/.config-map/deploy-creds/token"}) {
		t.Errorf("keys subset placed %v", got)
	}
	if strings.Contains(subset.PromptSection, "settings.toml") || strings.Contains(subset.PromptSection, "map app") {
		t.Errorf("keys subset index lists unplaced keys:\n%s", subset.PromptSection)
	}
	notChosen := mint(t, ps, "config-map", "e1-5-0", map[string]any{"config_maps": []any{"app"}, "keys": []any{"my-tools/run.sh"}})
	if notChosen.Status != protocol.StatusRefused {
		t.Errorf("key of an unchosen map = %+v, want refused", notChosen)
	}
	emptyChoice := mint(t, ps, "config-map", "e1-6-0", map[string]any{"config_maps": []any{}})
	if emptyChoice.Status != protocol.StatusRefused {
		t.Errorf("empty selection = %+v, want refused", emptyChoice)
	}
	if _, err := protocol.ValidateArgs(ps.Methods["config-map"].ArgsSchema, map[string]any{"secrets": []any{"unknown"}}); err == nil {
		t.Error("unknown secret map name was accepted by ArgsSchema")
	}
	if _, err := protocol.ValidateArgs(ps.Methods["config-map"].ArgsSchema, map[string]any{"keys": []any{"app/nope"}}); err == nil {
		t.Error("unknown key was accepted by ArgsSchema")
	}
}

func placementPaths(r *protocol.MintResponse) []string {
	var out []string
	for _, p := range r.Placements {
		out = append(out, p.Path)
	}
	return out
}

func TestConfigMapValidationAndReservedPaths(t *testing.T) {
	cm := func(name string, data map[string]any) map[string]any {
		return map[string]any{"name": name, "data": data}
	}
	file := func(content string) map[string]any { return map[string]any{"content": content} }
	for name, tc := range map[string]struct {
		config  map[string]any
		secrets map[string]string
		want    string
	}{
		"invalid name": {
			config: map[string]any{"config_maps": []any{cm("../bad", map[string]any{"f": file("x")})}},
			want:   "does not match",
		},
		"key with slash": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{"sub/f": file("x")})}},
			want:   `key "sub/f" must match`,
		},
		"dotdot key": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{"..": file("x")})}},
			want:   `key ".." must match`,
		},
		"map with no keys": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{})}},
			want:   "at least one key",
		},
		"duplicate name across config_maps and secrets": {
			config: map[string]any{
				"config_maps":   []any{cm("dup", map[string]any{"f": file("x")})},
				"secrets":       []any{map[string]any{"name": "dup", "keys": map[string]any{"g": map[string]any{}}}},
				"secret_values": "$secret:s",
			},
			secrets: map[string]string{"s": `{"dup/g":"val"}`},
			want:    "configured twice",
		},
		"duplicate path across maps": {
			config: map[string]any{
				"config_maps": []any{
					cm("a", map[string]any{"f": map[string]any{"content": "1", "path": "/home/debian/same"}}),
					map[string]any{"name": "b", "dir": "/home/debian", "data": map[string]any{"same": file("2")}},
				},
			},
			want: `path "/home/debian/same" is already placed by a/f`,
		},
		"duplicate path between a config map and a secret map": {
			config: map[string]any{
				"config_maps":   []any{cm("a", map[string]any{"tok": file("1")})},
				"secrets":       []any{map[string]any{"name": "s", "dir": "/home/debian/.config-map/a", "keys": map[string]any{"tok": map[string]any{}}}},
				"secret_values": "$secret:s",
			},
			secrets: map[string]string{"s": `{"s/tok":"v"}`},
			want:    "already placed by a/tok",
		},
		"relative path override": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{"f": map[string]any{"content": "x", "path": "relative/file"}})}},
			want:   "clean absolute path",
		},
		"unclean dir": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "a", "dir": "/home/debian/../etc", "data": map[string]any{"f": file("x")}}}},
			want:   "clean absolute path",
		},
		"reserved path .bashrc": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{"f": map[string]any{"content": "x", "path": "/home/debian/.bashrc"}})}},
			want:   "reserved sandbox directory",
		},
		"reserved path .git-credentials": {
			config: map[string]any{
				"secrets":       []any{map[string]any{"name": "git", "dir": "/home/debian", "keys": map[string]any{".git-credentials": map[string]any{}}}},
				"secret_values": "$secret:s",
			},
			secrets: map[string]string{"s": `{"git/.git-credentials":"secret"}`},
			want:    "reserved sandbox directory",
		},
		"reserved dir /home/debian/.grain": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "a", "dir": "/home/debian/.grain", "data": map[string]any{"state": file("x")}}}},
			want:   "reserved sandbox directory",
		},
		"reserved path /etc/sudoers": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "sudo", "dir": "/etc", "data": map[string]any{"sudoers": file("x")}}}},
			want:   "reserved sandbox directory",
		},
		"invalid file mode": {
			config: map[string]any{"config_maps": []any{cm("a", map[string]any{"f": map[string]any{"content": "x", "mode": "4755"}})}},
			want:   "four-digit octal mode",
		},
		"oversized map": {
			config: map[string]any{"config_maps": []any{cm("big", func() map[string]any {
				data := map[string]any{}
				for i := 0; i < 9; i++ {
					data[fmt.Sprintf("f%d", i)] = file(strings.Repeat("x", protocol.MaxPlacementBytes))
				}
				return data
			}())}},
			want: "over the",
		},
		"missing secret value": {
			config: map[string]any{
				"secrets":       []any{map[string]any{"name": "creds", "keys": map[string]any{"a": map[string]any{}, "b": map[string]any{}}}},
				"secret_values": "$secret:s",
			},
			secrets: map[string]string{"s": `{"creds/a":"1"}`},
			want:    `secret "creds" key "b" has no value set in secret_values (as "creds/b")`,
		},
		"content on a secret key rejected by schema": {
			config: map[string]any{
				"secrets": []any{map[string]any{"name": "leaked", "keys": map[string]any{"f": map[string]any{"content": "plaintext"}}}},
			},
			want: "content",
		},
		"old single-file config map": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "gitconfig", "path": "/home/debian/.gitconfig", "content": "x"}}},
			want:   `config-map "gitconfig" uses the removed single-file format (content/path/mode/executable/files on the entry); rewrite it as a map: {"name": "gitconfig", "dir": "<optional dir>", "data": {"<file name>": {"content": "...", "mode": "0644"}}}`,
		},
		"old files bundle": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "bundle", "files": []any{map[string]any{"path": "/home/debian/a", "content": "a"}}}}},
			want:   `config-map "bundle" uses the removed single-file format`,
		},
		"old single-file secret": {
			config: map[string]any{
				"secrets":       []any{map[string]any{"name": "npmrc", "path": "/home/debian/.npmrc"}},
				"secret_values": "$secret:s",
			},
			secrets: map[string]string{"s": `{"npmrc":"tok"}`},
			want:    `secret "npmrc" uses the removed single-file format (path/mode on the entry); rewrite it as a secret map`,
		},
		"unknown field": {
			config: map[string]any{"config_maps": []any{map[string]any{"name": "a", "items": "x", "data": map[string]any{"f": file("x")}}}},
			want:   `"items" is not an accepted field`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewConfigMap(ConfigMapConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = attachConfigured(t, s, tc.config, tc.secrets)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("attach = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestConfigMapSupportsOptionalDirFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("---\ndescription: Alpha skill.\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewConfigMap(ConfigMapConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ps := attach(t, s)
	if ps.Methods["config-map"] == nil || ps.Methods["skill"] == nil {
		t.Fatalf("methods = %v, want both config-map and skill when -dir is set", ps.Methods)
	}
	resp := mint(t, ps, "config-map", "e1-1-0", nil)
	if len(resp.Placements) != 1 || resp.Placements[0].Path != "/home/debian/.skills/alpha/SKILL.md" {
		t.Errorf("mint config-map from -dir = %+v", resp)
	}
}
