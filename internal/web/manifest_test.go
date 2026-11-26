package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Das PDH startet als installierte App ohne Browserleisten (Entwurf 11.11).
func TestWebManifestInstallable(t *testing.T) {
	raw, err := os.ReadFile("../../web/static/site.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"id": "/", "scope": "/", "start_url": "/", "display": "standalone", "lang": "de"} {
		if m[k] != want {
			t.Errorf("%s = %v, erwartet %q", k, m[k], want)
		}
	}
	out := renderFrameForTest(t, "server_config", frameData(t, nil))
	if !strings.Contains(between(out, `class="ft-user"`, `</nav>`), `data-app-install`) {
		t.Error("„Als App installieren“ fehlt im Benutzermenü")
	}
	if !strings.Contains(uiFile(t, "pdh-frame.js"), "beforeinstallprompt") {
		t.Error("Installationsangebot des Browsers wird nicht genutzt")
	}
}
