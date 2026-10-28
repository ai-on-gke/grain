package statecheck

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

// APIScopeSetsFile is where the state repository carries the
// deployment's API scope sets.
const APIScopeSetsFile = "tables/api_scope_set.json"

// APIScopeSets checks the deployment's API scope sets the dump declares
// (model.APIScopeSet) the way the daemon installs them: as one whole,
// which either resolves or is refused. It is a failure rather than a
// warning because a refused set is not a degraded one -- the daemon keeps
// the previous sets in effect and the merged file means nothing -- and
// the pull request is the one place anybody is looking.
//
// What it resolves to is written to out for every set the dump declares
// -- its scopes, whether only a person may grant it, and, given the
// previous version of the file (previous non-nil), how each set's
// effective scopes move -- so a reviewer of an agent's pull request sees
// what merging it would mean without resolving includes by hand. Merging
// grants nobody anything; that is said too, since it is the question the
// report invites.
func APIScopeSets(ctx context.Context, db *sql.DB, out io.Writer, previous []byte) error {
	sets, err := model.New(db).ListAPIScopeSets(ctx)
	if err != nil {
		return fmt.Errorf("reading the API scope sets this dump declares: %w", err)
	}
	after, err := model.ResolveAPIScopeSets(sets)
	if err != nil {
		return fmt.Errorf("%s declares API scope sets grain would refuse, so none of this file "+
			"would take effect and the sets already in effect would stay:\n  %s",
			APIScopeSetsFile, strings.ReplaceAll(err.Error(), "\n", "\n  "))
	}
	if len(sets) == 0 && previous == nil {
		return nil
	}
	fmt.Fprintf(out, "api scope sets: %d declared in %s (merging one grants it to nobody; "+
		"a grant still goes through admission and policy)\n", len(sets), APIScopeSetsFile)
	for _, s := range sets {
		r := after[s.Name]
		fmt.Fprintf(out, "  %s", r.Name)
		if r.Version > 0 {
			fmt.Fprintf(out, " (version %d)", r.Version)
		}
		if r.Deprecated {
			fmt.Fprint(out, " [deprecated]")
		}
		fmt.Fprintf(out, ": %s\n", scopeList(r))
		if r.Person {
			fmt.Fprintln(out, "    requires_person: yes -- only a person may grant it")
		} else {
			fmt.Fprintln(out, "    requires_person: no")
		}
	}
	if previous == nil {
		return nil
	}
	before, err := previousAPIScopeSets(previous)
	if err != nil {
		fmt.Fprintf(out, "api scope sets: the previous %s could not be read, so no diff: %v\n", APIScopeSetsFile, err)
		return nil
	}
	changes := model.DiffAPIScopeSets(before, after)
	if len(changes) == 0 {
		fmt.Fprintln(out, "api scope sets: no set's effective scopes change")
		return nil
	}
	fmt.Fprintln(out, "api scope sets: effective scopes against the previous version")
	for _, c := range changes {
		switch {
		case c.New:
			fmt.Fprintf(out, "  %s: new\n", c.Name)
		case c.Deleted:
			fmt.Fprintf(out, "  %s: deleted -- every grant of it grants nothing\n", c.Name)
			continue
		default:
			fmt.Fprintf(out, "  %s:\n", c.Name)
		}
		for _, sc := range c.Added {
			fmt.Fprintf(out, "    + %s%s\n", sc, reach(after[c.Name].RepoLimits[sc]))
		}
		for _, sc := range c.Removed {
			fmt.Fprintf(out, "    - %s%s\n", sc, reach(before[c.Name].RepoLimits[sc]))
		}
		for _, r := range c.Reach {
			fmt.Fprintf(out, "    ~ %s: %s -> %s\n", r.Scope, reachName(r.Before), reachName(r.After))
		}
		if !c.New && c.Person != c.WasPerson {
			fmt.Fprintf(out, "    requires_person: %s -> %s\n", yesNo(c.WasPerson), yesNo(c.Person))
		}
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// scopeList is a resolved set's scopes, each with the repositories it is
// limited to.
func scopeList(r model.ResolvedAPIScopeSet) string {
	if len(r.Resolved) == 0 {
		return "(no scopes)"
	}
	parts := make([]string, len(r.Resolved))
	for i, sc := range r.Resolved {
		parts[i] = string(sc) + reach(r.RepoLimits[sc])
	}
	return strings.Join(parts, ", ")
}

// reach is a scope's repository limit as scopeList writes it after the
// scope: "@a|b", or nothing for every repository.
func reach(lim []string) string {
	if lim == nil {
		return ""
	}
	return "@" + strings.Join(lim, "|")
}

// reachName is a repository limit as a reach change writes it.
func reachName(lim []string) string {
	if lim == nil {
		return "every repository"
	}
	return strings.Join(lim, ", ")
}

// previousAPIScopeSets resolves the previous version of the dump file.
// One that no longer resolves (or never did) is diffed as having had no
// sets, rather than failing the change that may be fixing it.
func previousAPIScopeSets(data []byte) (map[string]model.ResolvedAPIScopeSet, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]model.ResolvedAPIScopeSet{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, err
	}
	var sets []model.APIScopeSet
	for _, row := range rows {
		set := model.APIScopeSet{
			Name:           text(row["name"]),
			Description:    text(row["description"]),
			Scopes:         list(row["scopes"]),
			Includes:       list(row["includes"]),
			Repos:          list(row["repos"]),
			Deny:           list(row["deny"]),
			RequiresPerson: number(row["requires_person"]) != 0,
			Version:        number(row["version"]),
			Deprecated:     number(row["deprecated"]) != 0,
		}
		sets = append(sets, set)
	}
	resolved, err := model.ResolveAPIScopeSets(sets)
	if err != nil {
		return map[string]model.ResolvedAPIScopeSet{}, nil
	}
	return resolved, nil
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []string {
	var out []string
	_ = json.Unmarshal([]byte(text(v)), &out)
	return out
}

func number(v any) int {
	switch t := v.(type) {
	case json.Number:
		n, _ := strconv.Atoi(t.String())
		return n
	case bool:
		if t {
			return 1
		}
	}
	return 0
}
