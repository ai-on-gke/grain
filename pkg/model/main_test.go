package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// TestMain stops the embedded server postgrestest may have started for
// simulate_test.go, so it does not outlive this test binary.
func TestMain(m *testing.M) { postgrestest.Main(m) }
