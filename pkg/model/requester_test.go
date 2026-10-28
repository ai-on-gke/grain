package model

import "testing"

func TestDerivedFrom(t *testing.T) {
	for name, c := range map[string]struct {
		task Task
		want string
	}{
		"a root":            {Task{ID: "1"}, ""},
		"a GitHub origin":   {Task{ID: "1", WorkOrigin: "acme/widgets#4"}, ""},
		"its own tree":      {Task{ID: "1", WorkOrigin: TaskOrigin("1")}, ""},
		"another's tree":    {Task{ID: "1", WorkOrigin: TaskOrigin("2")}, "2"},
		"a port":            {Task{ID: "1", Links: []Link{{Kind: LinkPortOf, Target: "3"}}}, "3"},
		"a proposal":        {Task{ID: "1", WorkOrigin: TaskOrigin("2"), Links: []Link{{Kind: LinkProposedBy, Target: "4"}}}, "4"},
		"a dependency only": {Task{ID: "1", Links: []Link{{Kind: LinkDependsOn, Target: "5"}}}, ""},
	} {
		if got := c.task.DerivedFrom(); got != c.want {
			t.Errorf("%s: DerivedFrom = %q, want %q", name, got, c.want)
		}
	}
}

func TestInheritRequester(t *testing.T) {
	source := Task{ID: "7", Creator: " friend@example.com ",
		Approval: &Attribution{Actor: Principal{Kind: PrincipalHuman, ID: "lead@example.com"}}}
	derived := Task{ID: "8", Creator: "forged@example.com", Tags: []string{"k"}}
	InheritRequester(&derived, source)
	if derived.Creator != "friend@example.com" {
		t.Errorf("inherited creator %q, want friend@example.com", derived.Creator)
	}
	if derived.Approval != nil {
		t.Error("an approval was inherited")
	}

	// A team the derived task already names is kept.
	other := Task{ID: "9"}
	InheritRequester(&other, source)

}
