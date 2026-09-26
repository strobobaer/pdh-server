package web

import "testing"

func TestUpdateAvailableRequiresKnownBuildBehindGitHub(t *testing.T) {
	tests := []struct {
		name             string
		comparisonStatus string
		buildCommit      string
		want             bool
	}{
		{name: "remote ahead", comparisonStatus: "ahead", buildCommit: "abc123", want: true},
		{name: "identical", comparisonStatus: "identical", buildCommit: "abc123"},
		{name: "remote behind", comparisonStatus: "behind", buildCommit: "abc123"},
		{name: "diverged", comparisonStatus: "diverged", buildCommit: "abc123"},
		{name: "unknown build", comparisonStatus: "ahead", buildCommit: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updateAvailable(test.comparisonStatus, test.buildCommit); got != test.want {
				t.Fatalf("updateAvailable(%q, %q) = %t, want %t", test.comparisonStatus, test.buildCommit, got, test.want)
			}
		})
	}
}