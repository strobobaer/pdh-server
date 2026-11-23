package web

import (
	"net/http"
	"net/url"
	"testing"
)

// Schlussprüfung I4: Der TIA-Baustein legt Zuordnungen nur an einer Anlage
// an, an der die Person arbeiten darf (Abteilung der Anlage).
func TestTIAMappingsNeedAssetDepartment(t *testing.T) {
	env := newTIAEnv(t, "mqtt", map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5"})
	mod := func(v url.Values) { v.Set("create_mappings", "on"); v.Set("infrastructure_id", env.infra) }

	env.h.assetGuardHook = func(*http.Request, string, string) bool { return false }
	if w := env.download(t, mod); w.Code != http.StatusForbidden {
		t.Errorf("fremde Abteilung: %d %s", w.Code, w.Body.String())
	}
	if n := len(tiaMappingsOf(t, env)); n != 0 {
		t.Errorf("es darf nichts angelegt sein: %d", n)
	}

	env.h.assetGuardHook = func(_ *http.Request, perm, infra string) bool { return perm == "import.write" && infra == env.infra }
	if w := env.download(t, mod); w.Code != http.StatusOK {
		t.Fatalf("eigene Abteilung: %d %s", w.Code, w.Body.String())
	}
	if n := len(tiaMappingsOf(t, env)); n == 0 {
		t.Error("Zuordnungen fehlen")
	}
}
