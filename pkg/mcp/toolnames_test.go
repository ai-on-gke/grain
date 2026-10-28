package mcp

import "testing"

func TestQualifiedAndBareToolNameRoundTrip(t *testing.T) {
	for _, tool := range append(NewSandboxTools(""), NewMockTools(&MockSink{}, nil)...) {
		qualified := QualifiedToolName(tool.Name)
		if want := "mcp__grain-sandbox__" + tool.Name; qualified != want {
			t.Errorf("QualifiedToolName(%q) = %q, want %q", tool.Name, qualified, want)
		}
		if got := BareToolName(qualified); got != tool.Name {
			t.Errorf("BareToolName(%q) = %q, want %q", qualified, got, tool.Name)
		}
	}
}

// TestBareToolNameLeavesAnUnprefixedNameAlone covers the two ways a name
// reaches a Framework already bare: a CLI's own native tool, which was
// never namespaced under an MCP server at all, and a runtime that calls
// this package's tools in-process (testing.go's scripted fake). Neither
// may be rewritten on the way past.
func TestBareToolNameLeavesAnUnprefixedNameAlone(t *testing.T) {
	for _, name := range []string{"Bash", "ask_question", "", "mcp__other-server__ask_question"} {
		if got := BareToolName(name); got != name {
			t.Errorf("BareToolName(%q) = %q, want it unchanged", name, got)
		}
	}
}
