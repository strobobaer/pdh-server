package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCodeServerProxy(t *testing.T) {
	var gotPath, gotCookie, gotAuth, gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCookie, gotAuth, gotHost = r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Host
		if r.URL.Path == "/login" {
			http.Redirect(w, r, "/?folder=/workspace/pdh", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newCodeServerProxy(target)

	req := httptest.NewRequest(http.MethodGet, "https://pdh.example/code/static/out/main.js", nil)
	req.Header.Set("Cookie", "pdh_token=geheim; pdh_user_id=u1; vscode-tkn=abc")
	req.Header.Set("Authorization", "Bearer geheim")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if gotPath != "/static/out/main.js" {
		t.Errorf("Pfad = %q, Präfix /code nicht entfernt", gotPath)
	}
	if gotCookie != "vscode-tkn=abc" || gotAuth != "" {
		t.Errorf("PDH-Anmeldung weitergegeben: Cookie %q, Authorization %q", gotCookie, gotAuth)
	}
	if gotHost != "pdh.example" {
		t.Errorf("Host = %q (Origin-Prüfung bei WebSockets)", gotHost)
	}

	req = httptest.NewRequest(http.MethodGet, "https://pdh.example/code/login", nil)
	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/code/?folder=/workspace/pdh" {
		t.Errorf("Weiterleitung = %q, soll unter /code/ bleiben", loc)
	}

	ws := httptest.NewRequest(http.MethodGet, "/code/stable", nil)
	ws.Header.Set("Upgrade", "websocket")
	ws.Header.Set("Connection", "keep-alive, Upgrade")
	if !isWebSocketUpgrade(ws) || isWebSocketUpgrade(req) {
		t.Error("WebSocket-Erkennung falsch")
	}
}
