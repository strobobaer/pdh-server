package web

import "testing"

// Baustein „Webseite“: nur http(s) oder eigene Seiten, Höhe begrenzt.
func TestBoardEmbedURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/x?a=1": "https://example.com/x?a=1",
		"http://10.0.0.5:8080/":     "http://10.0.0.5:8080/",
		"/planner":                  "/planner",
		"//evil.example":            "",
		"javascript:alert(1)":       "",
		"data:text/html,hi":         "",
		"ftp://host/file":           "",
		"https://":                  "",
		"":                          "",
	}
	for in, want := range cases {
		if got := boardEmbedURL(in); got != want {
			t.Errorf("boardEmbedURL(%q) = %q, erwartet %q", in, got, want)
		}
	}
	out := sanitizeBoardBlocks([]BoardBlock{{Type: "webseite", Config: map[string]string{"url": "javascript:x", "height": "99999", "reload": "-3"}}})
	if len(out) != 1 || out[0].Config["url"] != "" || out[0].Config["height"] != "2000" || out[0].Config["reload"] != "" {
		t.Fatalf("unerwartet: %+v", out)
	}
}
