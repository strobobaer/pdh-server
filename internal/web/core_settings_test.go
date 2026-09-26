package web

import (
	"encoding/json"
	"testing"
)

func TestGitHubComparisonResponseDecode(t *testing.T) {
	var response githubComparisonResponse
	if err := json.Unmarshal([]byte(`{"status":"ahead","ahead_by":1,"behind_by":0,"commits":[{"sha":"abc123","html_url":"https://github.com/strobobaer/pdh-server/commit/abc123","commit":{"message":"Fix update comparison","author":{"date":"2026-09-26T11:19:37Z"}}}]}`), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "ahead" || response.AheadBy != 1 || len(response.Commits) != 1 {
		t.Fatalf("unexpected comparison response: %#v", response)
	}
	if response.Commits[0].SHA != "abc123" || response.Commits[0].Commit.Message != "Fix update comparison" {
		t.Fatalf("unexpected commit summary: %#v", response.Commits[0])
	}
}

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