package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// The deployment's own API scope sets (api_scope_set.go), read and
// written. A write is validated against every other set the store holds
// (ResolveAPIScopeSets), in the same transaction, so a form or tool can
// never store a set whose includes are missing or form a cycle; a pull
// request against tables/api_scope_set.json can, which is what the state
// check and InstallAPIScopeSets are for.

const apiScopeSetColumns = "`name`,`description`,`scopes`,`includes`,`repos`,`deny`,`requires_person`,`version`,`deprecated`"

// ListAPIScopeSets returns every declared set, by name.
func (s *Store) ListAPIScopeSets(ctx context.Context) ([]APIScopeSet, error) {
	return listAPIScopeSets(ctx, s.db)
}

func listAPIScopeSets(ctx context.Context, q querier) ([]APIScopeSet, error) {
	var out []APIScopeSet
	err := each(ctx, q, "SELECT "+apiScopeSetColumns+" FROM `api_scope_set`", nil,
		func(rows *sql.Rows) error {
			set, err := scanAPIScopeSet(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, set)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing api scope sets: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PutAPIScopeSet creates or replaces one set, refusing it unless every
// set the store would then hold resolves.
func (s *Store) PutAPIScopeSet(ctx context.Context, set APIScopeSet) error {
	if err := ValidateAPIScopeSetName(set.Name); err != nil {
		return err
	}
	return s.write(ctx, "put api scope set "+set.Name, func(tx *sql.Tx) error {
		sets, err := listAPIScopeSets(ctx, tx)
		if err != nil {
			return err
		}
		replaced := false
		for i := range sets {
			if sets[i].Name == set.Name {
				sets[i], replaced = set, true
			}
		}
		if !replaced {
			sets = append(sets, set)
		}
		if _, err := ResolveAPIScopeSets(sets); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"REPLACE INTO `api_scope_set` ("+apiScopeSetColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
			set.Name, set.Description, jsonList(set.Scopes), jsonList(set.Includes), jsonList(set.Repos),
			jsonList(set.Deny), set.RequiresPerson, set.Version, set.Deprecated)
		if err != nil {
			return fmt.Errorf("writing api scope set %s: %w", set.Name, err)
		}
		return nil
	})
}

// DeleteAPIScopeSet drops one set, refusing while another set includes
// it. Grants naming it stop granting anything.
func (s *Store) DeleteAPIScopeSet(ctx context.Context, name string) error {
	return s.write(ctx, "delete api scope set "+name, func(tx *sql.Tx) error {
		sets, err := listAPIScopeSets(ctx, tx)
		if err != nil {
			return err
		}
		var rest []APIScopeSet
		for _, set := range sets {
			if set.Name != name {
				rest = append(rest, set)
			}
		}
		if _, err := ResolveAPIScopeSets(rest); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM `api_scope_set` WHERE `name` = ?", name)
		return err
	})
}

// LoadAPIScopeSets installs the store's sets in this process
// (InstallAPIScopeSets): on startup, and after every state import.
func (s *Store) LoadAPIScopeSets(ctx context.Context) error {
	sets, err := s.ListAPIScopeSets(ctx)
	if err != nil {
		return err
	}
	return InstallAPIScopeSets(sets)
}

// jsonList is a list column's text: "[]" rather than "null" for an empty
// one, so a dump of a table nobody changed is byte-identical.
func jsonList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	data, _ := json.Marshal(items)
	return string(data)
}

func scanAPIScopeSet(scan func(...any) error) (APIScopeSet, error) {
	var set APIScopeSet
	var scopes, includes, repos, deny string
	if err := scan(&set.Name, &set.Description, &scopes, &includes, &repos, &deny,
		&set.RequiresPerson, &set.Version, &set.Deprecated); err != nil {
		return APIScopeSet{}, err
	}
	for _, col := range []struct {
		text string
		into *[]string
		name string
	}{{scopes, &set.Scopes, "scopes"}, {includes, &set.Includes, "includes"}, {repos, &set.Repos, "repos"}, {deny, &set.Deny, "deny"}} {
		if col.text == "" {
			continue
		}
		// Unlike custom_field.options, an unreadable list is an error:
		// read as empty, a deny list would silently grant more.
		if err := json.Unmarshal([]byte(col.text), col.into); err != nil {
			return APIScopeSet{}, fmt.Errorf("api scope set %s: %s is not a JSON list of strings: %v", set.Name, col.name, err)
		}
	}
	return set, nil
}
