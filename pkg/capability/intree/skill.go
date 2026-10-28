package intree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

// ConfigMapDir is the default directory on the sandbox VM where mint_config-map
// places configured files and secrets when an entry omits an explicit path:
// ConfigMapDir/<name>.
const ConfigMapDir = "/home/debian/.config-map"

// SkillsDir is where mint_skill places each skill: SkillsDir/<skill>/.
const SkillsDir = "/home/debian/.skills"

var (
	skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	// skillFile is every path segment a placed file may have.
	skillFile = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

	configMapName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	// configMapKey is a map key, the file name it is placed under, as
	// a Kubernetes ConfigMap's data keys are.
	configMapKey    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	configMapModeRe = regexp.MustCompile(`^0[0-7]{3}$`)
)

// ConfigMapConfig is the config-map server's startup configuration.
type ConfigMapConfig struct {
	// Dir optionally holds one directory per skill, each with a SKILL.md,
	// as a fallback for CLI/test callers when no entries are configured.
	Dir string
	Now func() time.Time
	Log *log.Logger
}

// SkillConfig is the legacy skill server's configuration.
type SkillConfig struct {
	// Dir holds one directory per skill, each with a SKILL.md.
	Dir string
	Now func() time.Time
	Log *log.Logger
}

type skill struct {
	name        string
	description string
	files       []protocol.Placement
	bytes       int
}

// configMapEntry is one configured map: a config map, a secret map, or a
// -dir skill, each placing every one of its files when granted.
type configMapEntry struct {
	name        string
	description string
	secret      bool
	dir         string
	files       []configMapFile
	bytes       int
}

// configMapFile is one key of a map and the file it places.
type configMapFile struct {
	key       string
	placement protocol.Placement
	bytes     int
}

// configMapKeySchema is one data key of a config map; secretMapKeySchema
// one key of a secret map, whose value is never in the config itself.
var (
	configMapKeySchema = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"format":      "code",
				"description": "File content.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "Octal file permissions (defaults to 0644, or 0755 when executable is true).",
			},
			"executable": map[string]any{
				"type":        "boolean",
				"description": "Use 0755 permissions when mode is omitted.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional clean absolute path overriding <dir>/<key>.",
			},
		},
	}
	secretMapKeySchema = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"description": "Octal file permissions (defaults to 0600).",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional clean absolute path overriding <dir>/<key>.",
			},
		},
	}
)

// The map items leave additionalProperties open so that the parser, not
// the schema validator, refuses an entry still in the removed single-file
// shape -- with a message that says how to rewrite it.
var configMapConfigSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"config_maps": map[string]any{
			"type":        "array",
			"description": "Config maps: each one name holding several files, all placed on the sandbox VM by one grant. Stored in grain_config.",
			"items": map[string]any{
				"type":     "object",
				"required": []any{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Map name used in capability grants; must match " + configMapName.String() + ".",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "One-line note shown to the agent in its prompt.",
					},
					"dir": map[string]any{
						"type":        "string",
						"description": "Clean absolute directory the map's files are placed in (defaults to " + ConfigMapDir + "/<name>).",
					},
					"data": map[string]any{
						"type":                 "object",
						"description":          "Files of the map: each key is a file name (" + configMapKey.String() + ") placed at <dir>/<key>.",
						"additionalProperties": configMapKeySchema,
					},
				},
			},
		},
		"secrets": map[string]any{
			"type":        "array",
			"description": "Secret maps: each one name holding several secret files (mode 0600 by default). Values are stored in the encrypted secrets store via secret_values.",
			"items": map[string]any{
				"type":     "object",
				"required": []any{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Secret map name used in capability grants; must match " + configMapName.String() + ".",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "One-line note shown to the agent in its prompt (never include a secret value).",
					},
					"dir": map[string]any{
						"type":        "string",
						"description": "Clean absolute directory the map's files are placed in (defaults to " + ConfigMapDir + "/<name>).",
					},
					"keys": map[string]any{
						"type":                 "object",
						"description":          "Keys of the secret map: each key is a file name (" + configMapKey.String() + ") placed at <dir>/<key>; its value is secret_values' \"<name>/<key>\".",
						"additionalProperties": secretMapKeySchema,
					},
				},
			},
		},
		"secret_values": map[string]any{
			"type":        "string",
			"writeOnly":   true,
			"description": "JSON object mapping each secret map key, as \"<map>/<key>\", to its file content: {\"<map>/<key>\": \"<content>\"}. Stored in the encrypted secrets store.",
		},
	},
}

// legacyConfigMapFields are the fields of the removed single-file entry
// shape; an entry carrying one is refused with a rewrite hint.
var legacyConfigMapFields = []string{"content", "path", "mode", "executable", "files"}

func legacyEntryError(kind, name string, secret bool) error {
	if secret {
		return fmt.Errorf("secret %q uses the removed single-file format (path/mode on the entry); rewrite it as a secret map: {\"name\": %q, \"dir\": \"<optional dir>\", \"keys\": {\"<file name>\": {\"mode\": \"0600\"}}} and set its value in secret_values as \"%s/<file name>\"", name, name, name)
	}
	return fmt.Errorf("%s %q uses the removed single-file format (content/path/mode/executable/files on the entry); rewrite it as a map: {\"name\": %q, \"dir\": \"<optional dir>\", \"data\": {\"<file name>\": {\"content\": \"...\", \"mode\": \"0644\"}}}", kind, name, name)
}

func parsePlacementFile(entryKind, entryName, key, targetPath, content, mode string, executable bool, defaultMode string, seenPaths map[string]string) (protocol.Placement, int, error) {
	label := fmt.Sprintf("%s %q key %q", entryKind, entryName, key)
	if !protocol.CleanAbsPath(targetPath) {
		return protocol.Placement{}, 0, fmt.Errorf("%s: path %q is not a clean absolute path", label, targetPath)
	}
	if protocol.ReservedPath(targetPath) {
		return protocol.Placement{}, 0, fmt.Errorf("%s: path %q is in a reserved sandbox directory", label, targetPath)
	}
	if prev, dup := seenPaths[targetPath]; dup {
		return protocol.Placement{}, 0, fmt.Errorf("%s: path %q is already placed by %s", label, targetPath, prev)
	}
	seenPaths[targetPath] = entryName + "/" + key

	if mode == "" {
		if executable {
			mode = "0755"
		} else {
			mode = defaultMode
		}
	}
	if !configMapModeRe.MatchString(mode) {
		return protocol.Placement{}, 0, fmt.Errorf("%s: mode %q must be a four-digit octal mode such as %s", label, mode, defaultMode)
	}
	rawBytes := []byte(content)
	if len(rawBytes) > protocol.MaxPlacementBytes {
		return protocol.Placement{}, 0, fmt.Errorf("%s: %s is %d bytes, over the %d-byte limit", label, targetPath, len(rawBytes), protocol.MaxPlacementBytes)
	}
	pl := protocol.Placement{Path: targetPath, Mode: mode}
	if utf8.Valid(rawBytes) {
		pl.Content = content
	} else {
		pl.Content = base64.StdEncoding.EncodeToString(rawBytes)
		pl.Encoding = "base64"
	}
	return pl, len(rawBytes), nil
}

func parseSecretValues(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("config-map: secret_values must be a JSON string")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(s), &values); err != nil {
		return nil, fmt.Errorf("config-map: secret_values must be a JSON object mapping \"<map>/<key>\" to contents")
	}
	return values, nil
}

// SecretValueKey is the secret_values key holding the value of key in the
// secret map named name.
func SecretValueKey(name, key string) string { return name + "/" + key }

// parseConfigMap parses one config_maps or secrets item into its entry.
func parseConfigMap(kind string, i int, item any, secretValues map[string]string, seenNames map[string]bool, seenPaths map[string]string) (*configMapEntry, error) {
	secret := kind == "secret"
	list, keysField := "config_maps", "data"
	if secret {
		list, keysField = "secrets", "keys"
	}
	m, ok := item.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s[%d]: must be an object", list, i)
	}
	name, _ := m["name"].(string)
	name = strings.TrimSpace(name)
	if !configMapName.MatchString(name) {
		return nil, fmt.Errorf("%s[%d]: name %q does not match %s", list, i, name, configMapName)
	}
	for _, f := range legacyConfigMapFields {
		if _, ok := m[f]; ok {
			return nil, legacyEntryError(kind, name, secret)
		}
	}
	for f := range m {
		switch f {
		case "name", "description", "dir", keysField:
		default:
			return nil, fmt.Errorf("%s %q: %q is not an accepted field (want name, description, dir, %s)", kind, name, f, keysField)
		}
	}
	if seenNames[name] {
		return nil, fmt.Errorf("%s %q is configured twice", kind, name)
	}
	seenNames[name] = true

	desc, _ := m["description"].(string)
	dir, _ := m["dir"].(string)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = path.Join(ConfigMapDir, name)
	}
	if !protocol.CleanAbsPath(dir) {
		return nil, fmt.Errorf("%s %q: dir %q is not a clean absolute path", kind, name, dir)
	}
	if protocol.ReservedPath(dir) {
		return nil, fmt.Errorf("%s %q: dir %q is in a reserved sandbox directory", kind, name, dir)
	}
	keys, _ := m[keysField].(map[string]any)
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s %q: %s must hold at least one key", kind, name, keysField)
	}
	if len(keys) > protocol.MaxPlacements {
		return nil, fmt.Errorf("%s %q: %d keys, over the %d files one grant may place", kind, name, len(keys), protocol.MaxPlacements)
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	e := &configMapEntry{name: name, description: clip(strings.TrimSpace(desc)), secret: secret, dir: dir}
	for _, key := range names {
		if !configMapKey.MatchString(key) || key == "." || key == ".." {
			return nil, fmt.Errorf("%s %q: key %q must match %s and not be . or ..", kind, name, key, configMapKey)
		}
		opts, _ := keys[key].(map[string]any)
		target, _ := opts["path"].(string)
		target = strings.TrimSpace(target)
		if target == "" {
			target = path.Join(dir, key)
		}
		mode, _ := opts["mode"].(string)
		var content, defaultMode string
		var exec bool
		if secret {
			val, ok := secretValues[SecretValueKey(name, key)]
			if !ok || val == "" {
				return nil, fmt.Errorf("secret %q key %q has no value set in secret_values (as %q)", name, key, SecretValueKey(name, key))
			}
			content, defaultMode = val, "0600"
		} else {
			content, _ = opts["content"].(string)
			exec, _ = opts["executable"].(bool)
			defaultMode = "0644"
		}
		pl, n, err := parsePlacementFile(kind, name, key, target, content, strings.TrimSpace(mode), exec, defaultMode, seenPaths)
		if err != nil {
			return nil, err
		}
		e.files = append(e.files, configMapFile{key: key, placement: pl, bytes: n})
		e.bytes += n
	}
	if e.bytes > protocol.MaxPlacementTotal {
		return nil, fmt.Errorf("%s %q: its files are %d bytes, over the %d bytes one grant may place", kind, name, e.bytes, protocol.MaxPlacementTotal)
	}
	return e, nil
}

func parseConfigMapEntries(config map[string]any) ([]*configMapEntry, error) {
	if len(config) == 0 {
		return nil, nil
	}
	secretValues, err := parseSecretValues(config["secret_values"])
	if err != nil {
		return nil, err
	}
	seenNames := map[string]bool{}
	seenPaths := map[string]string{}
	var entries []*configMapEntry
	for _, list := range []struct{ field, kind string }{{"config_maps", "config-map"}, {"secrets", "secret"}} {
		items, _ := config[list.field].([]any)
		for i, item := range items {
			e, err := parseConfigMap(list.kind, i, item, secretValues, seenNames, seenPaths)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// loadSkills reads every skill under dir, refusing anything mint_skill
// could not place: a skill without SKILL.md, a file name outside
// skillFile, a symlink, or more than a response may carry.
func loadSkills(dir string) (map[string]*skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	skills := map[string]*skill{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !skillName.MatchString(e.Name()) {
			return nil, fmt.Errorf("skill directory %q does not match %s", e.Name(), skillName)
		}
		sk := &skill{name: e.Name()}
		root := filepath.Join(dir, e.Name())
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
				if !skillFile.MatchString(seg) {
					return fmt.Errorf("%s: %q is not a name a skill file may have", e.Name(), rel)
				}
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s: %s is not a regular file", e.Name(), rel)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if len(b) > protocol.MaxPlacementBytes {
				return fmt.Errorf("%s: %s is over %d bytes", e.Name(), rel, protocol.MaxPlacementBytes)
			}
			pl := protocol.Placement{Path: path.Join(SkillsDir, e.Name(), filepath.ToSlash(rel)), Mode: "0644"}
			if info.Mode()&0o111 != 0 {
				pl.Mode = "0755"
			}
			if utf8.Valid(b) {
				pl.Content = string(b)
			} else {
				pl.Content, pl.Encoding = base64.StdEncoding.EncodeToString(b), "base64"
			}
			sk.files = append(sk.files, pl)
			sk.bytes += len(b)
			if filepath.ToSlash(rel) == "SKILL.md" {
				sk.description = skillDescription(string(b))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !hasSkillMD(sk) {
			return nil, fmt.Errorf("skill %s has no SKILL.md", sk.name)
		}
		skills[sk.name] = sk
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("no skills in %s", dir)
	}
	return skills, nil
}

func hasSkillMD(sk *skill) bool {
	for _, f := range sk.files {
		if f.Path == path.Join(SkillsDir, sk.name, "SKILL.md") {
			return true
		}
	}
	return false
}

// skillDescription is a SKILL.md's front-matter description, or its first
// non-empty line, cut to one short line.
func skillDescription(md string) string {
	lines := strings.Split(md, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) == "---" {
				break
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "description:"); ok {
				return clip(strings.Trim(strings.TrimSpace(v), `"'`))
			}
		}
	}
	for _, l := range lines {
		if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" && l != "---" {
			return clip(l)
		}
	}
	return ""
}

func clip(s string) string {
	if len(s) <= 160 {
		return s
	}
	for i := 160; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i] + "…"
		}
	}
	return ""
}

func newSkillMethod(skills map[string]*skill, now func() time.Time) (*protocol.Method, error) {
	names := make([]string, 0, len(skills))
	for n := range skills {
		names = append(names, n)
	}
	sort.Strings(names)
	enum := make([]any, len(names))
	all := make([]any, len(names))
	for i, n := range names {
		enum[i], all[i] = n, n
	}
	return protocol.NewMethod(protocol.MethodSpec{
		Name:        "skill",
		Description: "Place skills (a SKILL.md and its helper files each) under " + SkillsDir + ".",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"skills": map[string]any{
					"type":    "array",
					"items":   map[string]any{"type": "string", "enum": enum},
					"default": all,
				},
			},
		},
		Now: now,
		Mint: func(_ context.Context, _ string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
			chosen := map[string]bool{}
			if list, ok := args["skills"].([]any); ok {
				for _, v := range list {
					if s, ok := v.(string); ok {
						chosen[s] = true
					}
				}
			}
			resp := &protocol.MintResponse{Status: protocol.StatusOK}
			var index strings.Builder
			index.WriteString("Skills are available under " + SkillsDir + "; read a skill's SKILL.md before using it:\n")
			total := 0
			for _, n := range names {
				if !chosen[n] {
					continue
				}
				sk := skills[n]
				resp.Placements = append(resp.Placements, sk.files...)
				total += sk.bytes
				fmt.Fprintf(&index, "- %s/%s/SKILL.md", SkillsDir, n)
				if sk.description != "" {
					index.WriteString(" -- " + sk.description)
				}
				index.WriteString("\n")
			}
			if len(resp.Placements) == 0 {
				return refusedResponse("no skills were chosen"), nil
			}
			if len(resp.Placements) > protocol.MaxPlacements || total > protocol.MaxPlacementTotal {
				return refusedResponse(fmt.Sprintf("the chosen skills are %d files and %d bytes, over the %d files and %d bytes one grant may place; grant fewer of them", len(resp.Placements), total, protocol.MaxPlacements, protocol.MaxPlacementTotal)), nil
			}
			if index.Len() > protocol.MaxPromptSectionBytes {
				return refusedResponse("the chosen skills' index is too long for a prompt section; grant fewer of them"), nil
			}
			resp.PromptSection = index.String()
			return resp, nil
		},
	})
}

func newConfigMapMethods(entries []*configMapEntry, dirSkills map[string]*skill, now func() time.Time) ([]*protocol.Method, error) {
	if len(entries) == 0 && len(dirSkills) == 0 {
		return nil, nil
	}

	var allEntries []*configMapEntry
	byName := map[string]*configMapEntry{}
	var cmEnum, secEnum, keyEnum []any

	for _, e := range entries {
		allEntries = append(allEntries, e)
		byName[e.name] = e
		if e.secret {
			secEnum = append(secEnum, e.name)
		} else {
			cmEnum = append(cmEnum, e.name)
		}
		for _, f := range e.files {
			keyEnum = append(keyEnum, SecretValueKey(e.name, f.key))
		}
	}

	var skillNames []string
	for n := range dirSkills {
		if _, exists := byName[n]; !exists {
			skillNames = append(skillNames, n)
		}
	}
	sort.Strings(skillNames)
	for _, n := range skillNames {
		sk := dirSkills[n]
		e := &configMapEntry{
			name:        sk.name,
			description: sk.description,
			dir:         path.Join(SkillsDir, sk.name),
			bytes:       sk.bytes,
		}
		for _, pl := range sk.files {
			n := len(pl.Content)
			if pl.Encoding == "base64" {
				n = base64.StdEncoding.DecodedLen(n)
			}
			e.files = append(e.files, configMapFile{key: strings.TrimPrefix(pl.Path, e.dir+"/"), placement: pl, bytes: n})
		}
		allEntries = append(allEntries, e)
		byName[e.name] = e
		cmEnum = append(cmEnum, e.name)
	}

	props := map[string]any{}
	if len(cmEnum) > 0 {
		props["config_maps"] = map[string]any{
			"type":        "array",
			"description": "Config maps to place in the sandbox, every key of each (defaults to all configured maps when neither config_maps nor secrets is given).",
			"items":       map[string]any{"type": "string", "enum": cmEnum},
		}
	}
	if len(secEnum) > 0 {
		props["secrets"] = map[string]any{
			"type":        "array",
			"description": "Secret maps to place in the sandbox, every key of each (defaults to all configured maps when neither config_maps nor secrets is given).",
			"items":       map[string]any{"type": "string", "enum": secEnum},
		}
	}
	if len(keyEnum) > 0 {
		props["keys"] = map[string]any{
			"type":        "array",
			"description": "Optional subset of keys, as \"<map>/<key>\", to place instead of every key of the chosen maps (like a Kubernetes volume's items).",
			"items":       map[string]any{"type": "string", "enum": keyEnum},
		}
	}

	var descParts []string
	if len(cmEnum) > 0 {
		descParts = append(descParts, fmt.Sprintf("%d config map(s)", len(cmEnum)))
	}
	if len(secEnum) > 0 {
		descParts = append(descParts, fmt.Sprintf("%d secret map(s)", len(secEnum)))
	}
	desc := "Place configured config maps and secret maps onto the sandbox VM (" + strings.Join(descParts, ", ") + ")."

	cmMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        "config-map",
		Description: desc,
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
		},
		Now: now,
		Mint: func(_ context.Context, _ string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
			strs := func(key string) (map[string]bool, bool) {
				raw, ok := args[key]
				if !ok {
					return nil, false
				}
				set := map[string]bool{}
				if list, ok := raw.([]any); ok {
					for _, v := range list {
						if s, ok := v.(string); ok {
							set[s] = true
						}
					}
				}
				return set, true
			}
			chosen := map[string]bool{}
			explicit := false
			for _, key := range []string{"config_maps", "secrets"} {
				if set, ok := strs(key); ok {
					explicit = true
					for n := range set {
						chosen[n] = true
					}
				}
			}
			if !explicit {
				for _, e := range allEntries {
					chosen[e.name] = true
				}
			}
			onlyKeys, subset := strs("keys")

			resp := &protocol.MintResponse{Status: protocol.StatusOK}
			var index strings.Builder
			index.WriteString("Configured files are available in the sandbox:\n")
			total := 0
			for _, e := range allEntries {
				if !chosen[e.name] {
					continue
				}
				var placed []string
				for _, f := range e.files {
					if subset && !onlyKeys[SecretValueKey(e.name, f.key)] {
						continue
					}
					resp.Placements = append(resp.Placements, f.placement)
					total += f.bytes
					label := f.key
					if f.placement.Path != path.Join(e.dir, f.key) {
						label += " at " + f.placement.Path
					}
					placed = append(placed, label+" ("+f.placement.Mode+")")
				}
				if len(placed) == 0 {
					continue
				}
				kind := "config map"
				if e.secret {
					kind = "secret map"
				}
				fmt.Fprintf(&index, "- %s %s in %s: %s", kind, e.name, e.dir, strings.Join(placed, ", "))
				if e.description != "" {
					index.WriteString(" -- " + e.description)
				}
				index.WriteString("\n")
			}
			if len(resp.Placements) == 0 {
				return refusedResponse("no config-map or secret map keys were chosen"), nil
			}
			if len(resp.Placements) > protocol.MaxPlacements || total > protocol.MaxPlacementTotal {
				return refusedResponse(fmt.Sprintf("the chosen maps are %d files and %d bytes, over the %d files and %d bytes one grant may place; grant fewer of them", len(resp.Placements), total, protocol.MaxPlacements, protocol.MaxPlacementTotal)), nil
			}
			if index.Len() > protocol.MaxPromptSectionBytes {
				return refusedResponse("the chosen maps' index is too long for a prompt section; grant fewer of them"), nil
			}
			resp.PromptSection = index.String()
			return resp, nil
		},
	})
	if err != nil {
		return nil, err
	}
	methods := []*protocol.Method{cmMethod}
	if len(dirSkills) > 0 {
		skMethod, err := newSkillMethod(dirSkills, now)
		if err != nil {
			return nil, err
		}
		methods = append(methods, skMethod)
	}
	return methods, nil
}

// ConfigMapConfigSchema returns the operator-facing configure schema of the
// built-in config-map server.
func ConfigMapConfigSchema() map[string]any {
	s, err := protocol.NormalizeArgsSchema(configMapConfigSchema)
	if err != nil {
		return configMapConfigSchema
	}
	return protocol.OperatorSchema(s)
}

// NewConfigMap builds the configurable config-map server: method config-map,
// stateless, placing configured files and secrets on the sandbox VM.
func NewConfigMap(cfg ConfigMapConfig) (*Server, error) {
	var dirSkills map[string]*skill
	if cfg.Dir != "" {
		var err error
		dirSkills, err = loadSkills(cfg.Dir)
		if err != nil {
			return nil, fmt.Errorf("config-map: %w", err)
		}
	}
	initial, err := newConfigMapMethods(nil, dirSkills, cfg.Now)
	if err != nil {
		return nil, err
	}
	s, err := newServer("config-map", cfg.Log, initial...)
	if err != nil {
		return nil, err
	}
	if err := s.configurable(configMapConfigSchema, func(_ context.Context, raw map[string]any) ([]*protocol.Method, error) {
		entries, err := parseConfigMapEntries(raw)
		if err != nil {
			return nil, err
		}
		return newConfigMapMethods(entries, dirSkills, cfg.Now)
	}); err != nil {
		return nil, err
	}
	return s, nil
}

// NewSkill builds the skill server: method skill (§6.4), stateless, with
// no release_skill.
func NewSkill(cfg SkillConfig) (*Server, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("skill: -dir is required")
	}
	skills, err := loadSkills(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("skill: %w", err)
	}
	method, err := newSkillMethod(skills, cfg.Now)
	if err != nil {
		return nil, err
	}
	return newServer("skill", cfg.Log, method)
}

func init() {
	register(Builder{
		Name:        "config-map",
		Summary:     "configurable files and secrets placed in the sandbox (mint_config-map)",
		NeedsConfig: true,
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("config-map", logOutput(logger))
			dir := fs.String("dir", "", "optional directory holding one subdirectory per skill, each with a SKILL.md")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			return NewConfigMap(ConfigMapConfig{Dir: *dir, Log: logger})
		},
	})
	register(Builder{
		Name:     "skill",
		Summary:  "stateless skills placed under ~/.skills (mint_skill)",
		Internal: true,
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("skill", logOutput(logger))
			dir := fs.String("dir", "", "directory holding one subdirectory per skill, each with a SKILL.md")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			if *dir != "" {
				return NewSkill(SkillConfig{Dir: *dir, Log: logger})
			}
			return NewConfigMap(ConfigMapConfig{Log: logger})
		},
	})
}
