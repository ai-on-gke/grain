package protocol

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ConfigureTool is the standard tool a server advertises to take its own
// configuration (§4.5): inputSchema.properties.config is the server's
// configuration schema, in the same enforced keyword subset as a
// method's args.
const ConfigureTool = "configure"

// DiscoverConfigure reads the configure tool's config schema out of a
// tools/list (§4.5.1): nil and no error when the server advertises no
// configure tool. A configure tool whose inputSchema does not require
// config, whose config is not an object schema, or whose schema uses a
// keyword grain would not enforce, is a discovery error naming it; so is
// a secret field (SecretFields) that is not a top-level string.
func DiscoverConfigure(tools []Tool) (map[string]any, error) {
	var found *Tool
	for i := range tools {
		if tools[i].Name != ConfigureTool {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("tool %q is advertised twice", ConfigureTool)
		}
		found = &tools[i]
	}
	if found == nil {
		return nil, nil
	}
	if !requires(found.InputSchema, "config") {
		return nil, fmt.Errorf("tool %q: inputSchema must be an object requiring config", ConfigureTool)
	}
	props, _ := found.InputSchema["properties"].(map[string]any)
	cfg, ok := props["config"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.config must be a schema object", ConfigureTool)
	}
	if ty, ok := cfg["type"]; ok && ty != "object" {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.config must have type object", ConfigureTool)
	}
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := found.InputSchema[key]; ok {
			if _, own := cfg[key]; !own {
				merged := make(map[string]any, len(cfg)+1)
				for k, v := range cfg {
					merged[k] = v
				}
				merged[key] = defs
				cfg = merged
			}
		}
	}
	norm, err := NormalizeArgsSchema(cfg)
	if err != nil {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.config: %v", ConfigureTool, err)
	}
	if err := checkSecretFields(norm); err != nil {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.config: %v", ConfigureTool, err)
	}
	return norm, nil
}

// isSecretSchema reports whether a property schema marks a secret:
// writeOnly: true, or format: password (§4.5.2).
func isSecretSchema(s map[string]any) bool {
	return s["writeOnly"] == true || s["format"] == "password"
}

// SecretFields lists, sorted, the top-level properties of a config
// schema that are secrets (writeOnly: true or format: password). Grain
// keeps their values only in the encrypted secrets store and records a
// $secret:<name> reference in their place (§4.5.2).
func SecretFields(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	var out []string
	for name, p := range props {
		if ps, ok := p.(map[string]any); ok && isSecretSchema(ps) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// checkSecretFields refuses a secret marker anywhere but on a top-level
// string property: a secret is stored as one $secret:<name> per field,
// which a nested or non-string value has no place for.
func checkSecretFields(schema map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	for name, p := range props {
		ps, ok := p.(map[string]any)
		if !ok || !isSecretSchema(ps) {
			if ok {
				if err := noNestedSecret("config."+name, ps); err != nil {
					return err
				}
			}
			continue
		}
		if ty, ok := ps["type"]; ok && ty != "string" {
			return fmt.Errorf("config.%s: a secret field (writeOnly or format: password) must be a string", name)
		}
	}
	return nil
}

func noNestedSecret(path string, s map[string]any) error {
	for k, v := range s {
		switch sub := v.(type) {
		case map[string]any:
			if k == "properties" || k == "$defs" || k == "definitions" {
				for name, p := range sub {
					if ps, ok := p.(map[string]any); ok {
						if isSecretSchema(ps) {
							return fmt.Errorf("%s.%s: a secret field (writeOnly or format: password) must be a top-level property of config", path, name)
						}
						if err := noNestedSecret(path+"."+name, ps); err != nil {
							return err
						}
					}
				}
				continue
			}
			if isSecretSchema(sub) {
				return fmt.Errorf("%s.%s: a secret field (writeOnly or format: password) must be a top-level property of config", path, k)
			}
			if err := noNestedSecret(path+"."+k, sub); err != nil {
				return err
			}
		case []any:
			for i, item := range sub {
				if is, ok := item.(map[string]any); ok {
					if isSecretSchema(is) {
						return fmt.Errorf("%s.%s[%d]: a secret field (writeOnly or format: password) must be a top-level property of config", path, k, i)
					}
					if err := noNestedSecret(fmt.Sprintf("%s.%s[%d]", path, k, i), is); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// SecretRef is the reference a secret config field is stored as.
func SecretRef(name string) string { return secretPrefix + name }

// configSecretRef returns the secret name a config value references, if
// it is exactly one $secret:<name>.
func configSecretRef(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, secretPrefix) {
		return "", false
	}
	name := strings.TrimPrefix(s, secretPrefix)
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return "", false
	}
	return name, true
}

// checkConfigRefs is ServerConfig.Validate's check of a stored config: a
// $secret: reference may only be a whole top-level value, and anything
// that merely contains one is refused rather than half-resolved.
func checkConfigRefs(config map[string]any) error {
	for k, v := range config {
		if _, ok := configSecretRef(v); ok {
			continue
		}
		if containsSecretMarker(v) {
			return fmt.Errorf("config.%s: a $secret: reference must be the whole value of a top-level field", k)
		}
	}
	return nil
}

func containsSecretMarker(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, secretPrefix)
	case map[string]any:
		for _, x := range t {
			if containsSecretMarker(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if containsSecretMarker(x) {
				return true
			}
		}
	}
	return false
}

// CheckStoredConfig validates a stored configuration against a server's
// config schema without resolving its secrets (§4.5.3), the check
// Settings makes before saving: every secret field must be a
// $secret:<name> reference rather than a value, no other field may be one,
// and the rest must validate. A secret field's own constraints are checked
// only once it is resolved, by ResolveConfig.
func CheckStoredConfig(schema map[string]any, config map[string]any) error {
	secret := map[string]bool{}
	for _, f := range SecretFields(schema) {
		secret[f] = true
	}
	for k, v := range config {
		_, isRef := configSecretRef(v)
		switch {
		case secret[k] && !isRef:
			return fmt.Errorf("config.%s: is a secret field; it is stored in the secrets store and referenced as $secret:<name>, never as a value", k)
		case !secret[k] && isRef:
			return fmt.Errorf("config.%s: is not a secret field, so it cannot reference a secret", k)
		}
	}
	// Secret fields are checked for presence only: their schemas are
	// swapped for {} so a pattern or length is not tested against the
	// reference.
	relaxed := schema
	if len(secret) > 0 {
		relaxed = make(map[string]any, len(schema))
		for k, v := range schema {
			relaxed[k] = v
		}
		props := map[string]any{}
		for k, v := range schema["properties"].(map[string]any) {
			if secret[k] {
				v = map[string]any{}
			}
			props[k] = v
		}
		relaxed["properties"] = props
	}
	_, err := validateObject("config", relaxed, config)
	return err
}

// ResolveConfig checks a stored configuration (CheckStoredConfig),
// replaces each secret field's $secret:<name> with what the store holds,
// and validates the result in full, returning the normalized
// configuration to send to configure. A missing secret refuses it, naming
// the secret. The result holds secret values: keep it in memory only,
// and never log it.
func ResolveConfig(schema map[string]any, config map[string]any, lookup SecretLookup) (map[string]any, error) {
	if err := CheckStoredConfig(schema, config); err != nil {
		return nil, err
	}
	resolved := make(map[string]any, len(config))
	var missing []string
	for k, v := range config {
		if name, ok := configSecretRef(v); ok {
			val, found := "", false
			if lookup != nil {
				val, found = lookup(name)
			}
			if !found || val == "" {
				missing = append(missing, name)
				continue
			}
			v = val
		}
		resolved[k] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("config references secrets that are missing or empty: %s", strings.Join(dedupe(missing), ", "))
	}
	out, err := validateObject("config", schema, resolved)
	if err != nil {
		// A validator message can quote the offending value; for a
		// secret field that is the secret, so it is reported by path.
		for _, f := range SecretFields(schema) {
			if strings.HasPrefix(err.Error(), "config."+f+":") || strings.HasPrefix(err.Error(), "config."+f+" ") {
				return nil, fmt.Errorf("config.%s: the secret it references does not satisfy the field's schema", f)
			}
		}
		return nil, err
	}
	return out, nil
}

// Configure calls the server's configure tool with its resolved
// configuration (§4.5.4). The configuration holds secret values: an
// error is the server's own message, which the caller scrubs.
func (s *Server) Configure(ctx context.Context, config map[string]any) error {
	if config == nil {
		config = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(ctx, s.ListTimeout)
	defer cancel()
	_, err := s.caller.CallTool(ctx, ConfigureTool, map[string]any{"config": config})
	return err
}
