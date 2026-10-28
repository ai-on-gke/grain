package postgres

import (
	"strings"
	"testing"
)

func TestSanitizedDSNMasksEveryPasswordForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"userinfo", Config{URL: "postgres://grain:hunter2@db:5432/grain"}, "postgres://grain:%2A%2A%2A%2A%2A%2A@db:5432/grain"},
		{"query", Config{URL: "postgres://grain@db/grain?sslmode=require&password=hunter2"}, "postgres://grain@db/grain?sslmode=require&password=******"},
		{"key/value", Config{URL: "host=db user=grain password=hunter2 dbname=grain"}, "host=db user=grain password=****** dbname=grain"},
		{"key/value quoted", Config{URL: "host=db password = 'hun ter\\'2' dbname=grain"}, "host=db password = ****** dbname=grain"},
		{"fields", Config{Host: "db", User: "grain", Password: "hunter2"}, "postgres://grain:%2A%2A%2A%2A%2A%2A@db:5432"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.SanitizedDSN()
			if strings.Contains(got, "hunter2") || strings.Contains(got, "hun ter") {
				t.Fatalf("SanitizedDSN() = %q, leaks the password", got)
			}
			if got != tc.want {
				t.Errorf("SanitizedDSN() = %q, want %q", got, tc.want)
			}
		})
	}
}
