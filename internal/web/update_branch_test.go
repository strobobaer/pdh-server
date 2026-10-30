package web

import "testing"

func TestUpdateBranch(t *testing.T) {
	cases := map[string]string{"": "main", "  ": "main", "feature/verbund-update": "feature/verbund-update", "-x": "main", "a..b": "main", "a b": "main"}
	for in, want := range cases {
		t.Setenv("PDH_UPDATE_BRANCH", in)
		if got := updateBranch(); got != want {
			t.Errorf("PDH_UPDATE_BRANCH=%q: %q, erwartet %q", in, got, want)
		}
	}
}
