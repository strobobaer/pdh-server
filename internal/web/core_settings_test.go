package web

import (
	"encoding/json"
	"testing"
	"time"
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

func TestMicrosoftTokenEncryptionRoundTrip(t *testing.T) {
	handler := &Handler{jwtSecret: "test-signing-secret-with-more-than-32-characters"}
	const token = "sample-refresh-token"
	encrypted, err := handler.encryptMicrosoftToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == token {
		t.Fatal("encrypted token must not contain plaintext")
	}
	decrypted, err := handler.decryptMicrosoftToken(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != token {
		t.Fatalf("decrypted token = %q, want %q", decrypted, token)
	}
}

func TestParseMicrosoftDateTime(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Time
	}{
		{name: "UTC offset", value: "2026-09-27T13:45:00Z", want: time.Date(2026, 9, 27, 13, 45, 0, 0, time.UTC)},
		{name: "Graph UTC wall time", value: "2026-09-27T13:45:00.0000000", want: time.Date(2026, 9, 27, 13, 45, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMicrosoftDateTime(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("parsed datetime = %s, want %s", got, test.want)
			}
		})
	}
}

func TestMicrosoftBusyStatusFiltersFreeEvents(t *testing.T) {
	if !microsoftBusyStatus("busy") || !microsoftBusyStatus("oof") || !microsoftBusyStatus("tentative") {
		t.Fatal("expected busy statuses to be imported")
	}
	if microsoftBusyStatus("free") || microsoftBusyStatus("unknown") {
		t.Fatal("free and unknown statuses must not block availability")
	}
}

func TestMicrosoftScopesKeepTeamsOptIn(t *testing.T) {
	calendarScopes := microsoftScopes("calendar")
	teamsScopes := microsoftScopes("teams")
	if hasMicrosoftScope(calendarScopes, "Chat.ReadWrite") || hasMicrosoftScope(calendarScopes, "ChannelMessage.Send") {
		t.Fatal("standard account consent must not request Teams permissions")
	}
	if !hasMicrosoftScope(teamsScopes, "Chat.ReadWrite") || !hasMicrosoftScope(teamsScopes, "ChannelMessage.Send") {
		t.Fatal("Teams consent must request direct chat and channel message permissions")
	}
}