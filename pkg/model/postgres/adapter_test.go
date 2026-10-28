package postgres

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAdapterTablePrimaryKeysCoverAllSchemaTables(t *testing.T) {
	reTable := regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?\s*\((.*)\)\s*$`)
	reAI := regexp.MustCompile(`(?i)["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?\s+INTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT`)
	rePK := regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(([^)]+)\)`)
	reInlinePK := regexp.MustCompile(`(?i)["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?\s+[^,\n]+PRIMARY\s+KEY`)

	tableCount := 0
	for _, ddl := range model.Tables {
		if !strings.Contains(strings.ToUpper(ddl), "CREATE TABLE") {
			continue
		}
		tableCount++
		match := reTable.FindStringSubmatch(strings.TrimSpace(ddl))
		if len(match) < 3 {
			t.Fatalf("failed to parse table DDL: %s", ddl)
		}
		tableName := match[1]
		body := match[2]

		var expectedPKs []string
		if aiMatch := reAI.FindStringSubmatch(body); len(aiMatch) >= 2 {
			expectedCol := aiMatch[1]
			expectedPKs = []string{expectedCol}
		} else if pkMatch := rePK.FindStringSubmatch(body); len(pkMatch) >= 2 {
			for _, col := range strings.Split(pkMatch[1], ",") {
				c := strings.Trim(strings.TrimSpace(col), "`\"")
				expectedPKs = append(expectedPKs, c)
			}
		} else if inlineMatch := reInlinePK.FindStringSubmatch(body); len(inlineMatch) >= 2 {
			expectedPKs = []string{inlineMatch[1]}
		} else {
			t.Fatalf("could not determine PK for table %s from DDL: %s", tableName, ddl)
		}

		actualPKs, ok := tablePrimaryKeys[tableName]
		if !ok {
			t.Errorf("table %q is missing from tablePrimaryKeys in adapter.go", tableName)
			continue
		}
		if len(actualPKs) != len(expectedPKs) {
			t.Errorf("table %q PK length mismatch: got %v, want %v", tableName, actualPKs, expectedPKs)
			continue
		}
		for i := range actualPKs {
			if actualPKs[i] != expectedPKs[i] {
				t.Errorf("table %q PK[%d] mismatch: got %q, want %q", tableName, i, actualPKs[i], expectedPKs[i])
			}
		}
	}
	if tableCount == 0 {
		t.Fatal("no tables were parsed from model.Tables")
	}
}

func TestAdapterAutoIncrementTablesCoverAllSchemaAutoIncrements(t *testing.T) {
	reTable := regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?\s*\((.*)\)\s*$`)
	reAI := regexp.MustCompile(`(?i)["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?\s+INTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT`)

	aiCount := 0
	for _, ddl := range model.Tables {
		if !strings.Contains(strings.ToUpper(ddl), "CREATE TABLE") {
			continue
		}
		match := reTable.FindStringSubmatch(strings.TrimSpace(ddl))
		if len(match) < 3 {
			t.Fatalf("failed to parse table DDL: %s", ddl)
		}
		tableName := match[1]
		body := match[2]

		aiMatch := reAI.FindStringSubmatch(body)
		if len(aiMatch) >= 2 {
			aiCount++
			expectedCol := aiMatch[1]
			actualCol, ok := autoIncrementTables[tableName]
			if !ok {
				t.Errorf("table %q has autoincrement column %q but is missing from autoIncrementTables in adapter.go", tableName, expectedCol)
				continue
			}
			if actualCol != expectedCol {
				t.Errorf("table %q autoincrement column mismatch: got %q, want %q", tableName, actualCol, expectedCol)
			}
		}
	}
	if aiCount == 0 {
		t.Fatal("no autoincrement tables were parsed from model.Tables")
	}
}

func TestAdaptValueUTF8Sanitization(t *testing.T) {
	// String with invalid UTF-8 byte 0x81 and null byte \x00
	invalidStr := "step output with invalid byte: \x81 and null: \x00 and end"
	res := adaptValue(invalidStr)
	resStr, ok := res.(string)
	if !ok {
		t.Fatalf("expected string, got %T", res)
	}
	if strings.ContainsRune(resStr, '\x00') {
		t.Errorf("adaptValue did not strip null byte: %q", resStr)
	}
	if strings.Contains(resStr, "\x81") {
		t.Errorf("adaptValue did not sanitize invalid UTF-8 byte: %q", resStr)
	}
	if !strings.Contains(resStr, "\uFFFD") {
		t.Errorf("adaptValue did not replace invalid UTF-8 with replacement rune: %q", resStr)
	}
}

func TestAdaptDDLSpellsIntegerAsBigintExceptBooleans(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"CREATE TABLE `t` (`blocked_ms` INTEGER NOT NULL DEFAULT 0)",
			"CREATE TABLE `t` (`blocked_ms` BIGINT NOT NULL DEFAULT 0)"},
		{"CREATE TABLE `t` (`singleton` INTEGER NOT NULL CHECK (`singleton` = 1), PRIMARY KEY (`singleton`))",
			"CREATE TABLE `t` (`singleton` BIGINT NOT NULL CHECK (`singleton` = 1), PRIMARY KEY (`singleton`))"},
		{"CREATE TABLE `t` (`auto_merge` INTEGER NOT NULL DEFAULT 1)",
			"CREATE TABLE `t` (`auto_merge` BOOLEAN NOT NULL DEFAULT TRUE)"},
		{"CREATE TABLE `t` (`id` INTEGER PRIMARY KEY AUTOINCREMENT)",
			"CREATE TABLE `t` (`id` BIGSERIAL PRIMARY KEY)"},
		{"ALTER TABLE `merge_batch` ADD COLUMN `queued` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `merge_batch` ADD COLUMN `queued` BIGINT NOT NULL DEFAULT 0"},
		{"CREATE TABLE `t` (`integer_count` INTEGER NULL)",
			"CREATE TABLE `t` (`integer_count` BIGINT NULL)"},
	} {
		if got := adaptDDL(tc.in); got != tc.want {
			t.Errorf("adaptDDL(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestAdaptQueryMemoAnswersWhatTranslateQueryWould is adaptQuery's memo
// held to the uncached rewrite: the first call and the remembered second
// call both say exactly what translateQuery says.
func TestAdaptQueryMemoAnswersWhatTranslateQueryWould(t *testing.T) {
	for _, q := range []string{
		"SELECT `id` FROM `schedule` WHERE `enabled` = 1 AND `next_run_at` <= ? ORDER BY `id`",
		"INSERT OR IGNORE INTO `task_tag` (`task_id`,`tag`) VALUES (?, ?)",
		"PRAGMA table_info(task)",
	} {
		want := translateQuery(q)
		for i := 0; i < 2; i++ {
			if got := adaptQuery(q); got != want {
				t.Fatalf("adaptQuery(%q) call %d = %q, want %q", q, i+1, got, want)
			}
		}
		if cached, ok := adaptedQueries.Load(q); !ok || cached.(string) != want {
			t.Fatalf("adaptQuery(%q) left %v, %v in the memo, want %q", q, cached, ok, want)
		}
	}
}

// A byte length is measured with octet_length, never through a bytea
// cast: Postgres parses the text of a text->bytea cast as a bytea
// literal, so a stored prompt with a lone backslash failed every read
// that measured it (invalid input syntax for type bytea, 22P02).
func TestTranslateQueryMeasuresTextBytesWithoutCastingToBytea(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SELECT `id`,length(CAST(`prompt` AS BLOB)) FROM `task_run`",
			`SELECT "id",octet_length("prompt") FROM "task_run"`},
		{"SELECT SUM(LENGTH(CAST(s.`output` AS BLOB))) FROM `ci_step` s",
			`SELECT SUM(octet_length(s."output")) FROM "ci_step" s`},
		{"SELECT CAST(`x` AS BLOB) FROM `t`",
			`SELECT CAST("x" AS BYTEA) FROM "t"`},
	} {
		if got := translateQuery(tc.in); got != tc.want {
			t.Errorf("translateQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
