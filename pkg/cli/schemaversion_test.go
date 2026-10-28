package cli

import (
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestSchemaVersionCmdPrintsModelSchemaVersion(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	schemaVersionCmd(nil)

	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}

	got := strings.TrimSpace(string(out))
	want := strconv.Itoa(model.SchemaVersion)
	if got != want {
		t.Errorf("grain schema-version printed %q, want %q (pkg/model.SchemaVersion)", got, want)
	}
}
