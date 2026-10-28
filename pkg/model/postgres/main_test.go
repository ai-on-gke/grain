package postgres_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model/postgres/postgrestest"
)

// TestMain stops the embedded server postgrestest may have started, so it
// does not outlive this test binary.
func TestMain(m *testing.M) { postgrestest.Main(m) }
