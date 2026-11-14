package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh"
	"pdh/internal/core/rbac"
	"pdh/internal/datanet"
)

// dnDo ruft einen Handler mit Pfadwerten (id, file) auf.
func dnDo(h http.HandlerFunc, method, path string, params map[string]string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

// dnPageHandler: Handler mit Testdatenbank und allen Rechten. Die Vorlagen
// liegen relativ zum Arbeitsordner; er wechselt für den Test in die Wurzel.
func dnPageHandler(t *testing.T) *Handler {
	t.Helper()
	pool := dataNetTestPool(t)
	tmpl := loadTestTemplates(t)
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })
	h := &Handler{db: pool, rbac: rb, jwtSecret: "t", tmpl: tmpl,
		hasPermHook: func(*http.Request, string) bool { return true }}
	return h
}

func TestDataNetPagesRequirePermission(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	cases := []struct {
		name   string
		fn     http.HandlerFunc
		meth   string
		path   string
		params map[string]string
		want   string
	}{
		{"Seite", h.DataNetPage, "GET", "/datanet", nil, permDataNetView},
		{"anlegen", h.DataNetCreate, "POST", "/datanet/clients", nil, permDataNetEdit},
		{"sperren", h.DataNetRevoke, "POST", "/datanet/clients/" + id + "/revoke", map[string]string{"id": id}, permDataNetEdit},
		{"entfernen", h.DataNetDelete, "POST", "/datanet/clients/" + id + "/delete", map[string]string{"id": id}, permDataNetAdmin},
		{"Einstellungen", h.DataNetSettings, "POST", "/datanet/settings", nil, permDataNetAdmin},
		{"Download", h.DataNetDownload, "GET", "/datanet/download/pdh-data-client-linux-amd64", map[string]string{"file": "pdh-data-client-linux-amd64"}, permDataNetEdit},
	}
	for _, c := range cases {
		asked = nil
		w := dnDo(c.fn, c.meth, c.path, c.params, url.Values{})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[len(asked)-1] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}
	// Entfernen fragt datanet.admin – datanet.edit genügt nicht.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permDataNetAdmin }
	if w := dnDo(h.DataNetDelete, "POST", "/datanet/clients/"+id+"/delete", map[string]string{"id": id}, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Entfernen mit datanet.edit: Status %d", w.Code)
	}
	if w := dnDo(h.DataNetSettings, "POST", "/datanet/settings", nil, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Einstellungen mit datanet.edit: Status %d", w.Code)
	}
	// Keine gültige ID: nicht gefunden statt Datenbankzugriff.
	h.hasPermHook = func(*http.Request, string) bool { return true }
	for _, fn := range []http.HandlerFunc{h.DataNetRevoke, h.DataNetDelete} {
		if w := dnDo(fn, "POST", "/datanet/clients/x/revoke", map[string]string{"id": "x"}, url.Values{}); w.Code != http.StatusNotFound {
			t.Errorf("ungültige ID: Status %d", w.Code)
		}
	}
}

func TestDataNetGateRules(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		method, path, perm, ref string
		hit                     bool
	}{
		{"POST", "/datanet/clients", permDataNetEdit, "", true},
		{"POST", "/datanet/clients/" + id + "/revoke", permDataNetEdit, dataNetRef, true},
		{"POST", "/datanet/clients/" + id + "/delete", permDataNetAdmin, dataNetRef, true},
		{"POST", "/datanet/settings", permDataNetAdmin, "", true},
		{"GET", "/datanet/clients/" + id + "/delete", "", "", false},
	}
	for _, c := range cases {
		_, perm, ref, _, hit := matchGate(c.method, c.path)
		if hit != c.hit || perm != c.perm || ref != c.ref {
			t.Errorf("%s %s: Treffer %v Recht %q Typ %q; erwartet %v %q %q", c.method, c.path, hit, perm, ref, c.hit, c.perm, c.ref)
		}
	}
	if areaAdminPerm[dataNetRef] != permDataNetAdmin {
		t.Errorf("Bereichsadministrator des Datennetzes fehlt in areaAdminPerm")
	}
}

func TestDataNetACL(t *testing.T) {
	want := `"tagOwners": { "tag:pdh": ["autogroup:admin"], "tag:pdh-data": ["autogroup:admin"] },
"acls": [
  { "action": "accept", "src": ["tag:pdh"], "dst": ["tag:pdh-data:1433,3306"] },
  { "action": "accept", "src": ["tag:pdh-data"], "dst": ["tag:pdh:8443"] }
]`
	if got := dataNetACL([]int{1433, 3306}); got != want {
		t.Errorf("ACL:\n%s\nerwartet:\n%s", got, want)
	}
	if got := dataNetACL([]int{3306, 1433, 3306, 0, 70000}); !strings.Contains(got, "tag:pdh-data:1433,3306\"") {
		t.Errorf("Ports nicht sortiert/eindeutig/gültig: %s", got)
	}
	if got := dataNetACL(nil); !strings.Contains(got, "tag:pdh-data:<PORT>") {
		t.Errorf("Platzhalter fehlt: %s", got)
	}
}

func TestDataNetInstallCmd(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	addrs := []string{"100.64.0.1", "100.64.0.2"}
	win := dataNetInstallCmd("windows", id, "TOK", addrs, []int{1433})
	lin := dataNetInstallCmd("linux", id, "TOK", addrs, []int{1433, 3306})
	if !strings.HasPrefix(win, "pdh-data-client-windows-amd64.exe install") || !strings.HasPrefix(lin, "sudo ./pdh-data-client-linux-amd64 install") {
		t.Errorf("Anfang: %q / %q", win, lin)
	}
	for _, c := range []string{win, lin} {
		for _, want := range []string{"--id " + id, "--token TOK", "--pdh 100.64.0.1,100.64.0.2", "--authkey <EINMAL-SCHLÜSSEL>"} {
			if !strings.Contains(c, want) {
				t.Errorf("%q fehlt in %q", want, c)
			}
		}
	}
	if !strings.Contains(win, "--target 1433") || strings.Contains(win, "--target 3306") {
		t.Errorf("Windows-Ziele: %s", win)
	}
	if !strings.Contains(lin, "--target 1433 --target 3306") {
		t.Errorf("Linux-Ziele: %s", lin)
	}
	if def := dataNetInstallCmd("linux", id, "TOK", addrs, nil); !strings.Contains(def, "--target 1433") {
		t.Errorf("Standard-Ziel fehlt: %s", def)
	}
	if got := dataNetInstallCmd("linux", id, "TOK", nil, nil); got != "Erst das Datennetz des PDH einschalten" {
		t.Errorf("ohne Knoten: %q", got)
	}
}

// Die Fassung ohne Spur in der Shell-Historie enthält das Token nie.
func TestDataNetInstallCmdSafe(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	addrs := []string{"100.64.0.1"}
	prepW, cmdW := dataNetInstallCmdSafe("windows", id, addrs, []int{1433})
	prepL, cmdL := dataNetInstallCmdSafe("linux", id, addrs, []int{1433})
	if !strings.HasPrefix(prepW, "set /p PDH_DATANET_TOKEN=") || !strings.HasPrefix(cmdW, "pdh-data-client-windows-amd64.exe install") {
		t.Errorf("Windows: %q / %q", prepW, cmdW)
	}
	if !strings.HasPrefix(prepL, "read -rs") || !strings.Contains(cmdL, "sudo ./pdh-data-client-linux-amd64 install") || !strings.Contains(cmdL, "--token-file -") {
		t.Errorf("Linux: %q / %q", prepL, cmdL)
	}
	for _, c := range []string{cmdW, cmdL} {
		if strings.Contains(c, "--token ") || !strings.Contains(c, "--id "+id) || !strings.Contains(c, "--pdh 100.64.0.1") {
			t.Errorf("Befehl: %q", c)
		}
	}
	if p, c := dataNetInstallCmdSafe("linux", id, nil, nil); p != "" || c != "Erst das Datennetz des PDH einschalten" {
		t.Errorf("ohne Knoten: %q %q", p, c)
	}
}

func TestDataNetClientDir(t *testing.T) {
	t.Setenv("PDH_DATA_CLIENT_DIR", "/x/clients")
	if got := dataNetClientDir(); got != "/x/clients" {
		t.Errorf("Umgebung: %q", got)
	}
	t.Setenv("PDH_DATA_CLIENT_DIR", "")
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if got := dataNetClientDir(); got != filepath.Dir(exe) {
		t.Errorf("Standard: %q, erwartet %q", got, filepath.Dir(exe))
	}
}

func TestDataNetDownload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PDH_DATA_CLIENT_DIR", dir)
	content := []byte("programm-inhalt")
	if err := os.WriteFile(filepath.Join(dir, "pdh-data-client-linux-amd64"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	// Eine Datei, die nicht zur Liste gehört, wird nie ausgeliefert.
	os.WriteFile(filepath.Join(dir, "geheim.txt"), []byte("x"), 0o600)
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	for _, name := range []string{"../etc/passwd", "pdh", "geheim.txt", "..", "", "pdh-data-client-windows-amd64.exe"} {
		w := dnDo(h.DataNetDownload, "GET", "/datanet/download/x", map[string]string{"file": name}, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%q: Status %d, erwartet 404", name, w.Code)
		}
	}
	w := dnDo(h.DataNetDownload, "GET", "/datanet/download/pdh-data-client-linux-amd64", map[string]string{"file": "pdh-data-client-linux-amd64"}, nil)
	if w.Code != 200 || w.Body.String() != string(content) {
		t.Fatalf("Download: Status %d, Inhalt %q", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "pdh-data-client-linux-amd64") {
		t.Errorf("Content-Disposition: %q", cd)
	}
	sum := sha256.Sum256(content)
	ds := dataNetDownloads()
	if len(ds) != 2 {
		t.Fatalf("Downloads: %+v", ds)
	}
	for _, d := range ds {
		switch d.File {
		case "pdh-data-client-linux-amd64":
			if !d.Present || d.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("Linux: %+v", d)
			}
		default:
			if d.Present || d.SHA256 != "" {
				t.Errorf("fehlende Datei: %+v", d)
			}
		}
	}
}

func TestDataNetCreateShowsTokenOnce(t *testing.T) {
	h := dnPageHandler(t)
	ctx := context.Background()
	name := "seitentest-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	t.Cleanup(func() {
		h.db.Exec(context.Background(), `DELETE FROM datanet_events WHERE client_name = $1`, name)
		h.db.Exec(context.Background(), `DELETE FROM datanet_clients WHERE name = $1`, name)
		h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key LIKE 'datanet.cloud.node.pagetest%'`)
	})
	node, _ := json_(map[string]any{"addrs": []string{"fd7a::1", "100.64.0.1"}})
	if err := h.dataNetPut(ctx, dataNetKeyNode+"pagetest-a", node); err != nil {
		t.Fatal(err)
	}
	node2, _ := json_(map[string]any{"addrs": []string{"100.64.0.2"}})
	if err := h.dataNetPut(ctx, dataNetKeyNode+"pagetest-b", node2); err != nil {
		t.Fatal(err)
	}

	w := dnDo(h.DataNetCreate, "POST", "/datanet/clients", nil, url.Values{"name": {name}})
	if w.Code != 200 {
		t.Fatalf("Status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	var id, hash string
	if err := h.db.QueryRow(ctx, `SELECT id::text, token_hash FROM datanet_clients WHERE name = $1`, name).Scan(&id, &hash); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pdh-data-client-windows-amd64.exe install", "sudo ./pdh-data-client-linux-amd64 install", "--id " + id,
		"pdh-pagetest-a,100.64.0.1,pdh-pagetest-b,100.64.0.2", "&lt;EINMAL-SCHLÜSSEL&gt;", "Settings", "tag:pdh-data", "PDH_DATANET_TOKEN", "--token-file -"} {
		if !strings.Contains(body, want) {
			t.Errorf("Antwort enthält %q nicht", want)
		}
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control: %q", cc)
	}
	// Das Token steht in der Antwort; es ist der einzige Wert mit passendem Hash.
	token := ""
	for _, part := range strings.FieldsFunc(body, func(r rune) bool { return strings.ContainsRune(" \t\n\r<>\"'", r) }) {
		if len(part) == 43 && dataNetHash(part) == hash {
			token = part
			break
		}
	}
	if token == "" {
		t.Fatal("Token in der Antwort nicht gefunden")
	}

	// Danach nie wieder: weder Token noch Befehl noch ID-in-URL.
	for _, path := range []string{"/datanet", "/datanet?n=revoked"} {
		w2 := dnDo(h.DataNetPage, "GET", path, nil, nil)
		if w2.Code != 200 {
			t.Fatalf("Seite: Status %d", w2.Code)
		}
		page := w2.Body.String()
		if strings.Contains(page, token) || strings.Contains(page, "install --id") {
			t.Error("Token oder Installationsbefehl nach dem Anlegen noch sichtbar")
		}
		if !strings.Contains(page, name) || !strings.Contains(page, "Noch keine Meldung") {
			t.Error("Client erscheint nicht als „Noch keine Meldung“")
		}
	}
	// Gleicher Name noch einmal: Fehler, kein zweiter Client, kein Token.
	w3 := dnDo(h.DataNetCreate, "POST", "/datanet/clients", nil, url.Values{"name": {name}})
	if strings.Contains(w3.Body.String(), "install --id") || !strings.Contains(w3.Body.String(), "schon vergeben") {
		t.Errorf("doppelter Name: %d", w3.Code)
	}
	// Leerer Name.
	w4 := dnDo(h.DataNetCreate, "POST", "/datanet/clients", nil, url.Values{"name": {"  "}})
	if strings.Contains(w4.Body.String(), "install --id") {
		t.Error("leerer Name legt einen Client an")
	}
}

func TestDataNetSettingsKeyEncrypted(t *testing.T) {
	h := dnPageHandler(t)
	ctx := context.Background()
	server := h.dataNetServerName()
	t.Cleanup(func() {
		h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key IN ($1, $2, $3)`, dataNetKeyEnabled, dataNetKeyAuth+server, dataNetKeyAuth+"pagetest-b")
	})
	const key = "tskey-auth-ABCDEF123456"
	w := dnDo(h.DataNetSettings, "POST", "/datanet/settings", nil, url.Values{"authkey." + server: {key}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Status %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); strings.Contains(loc, "tskey") || strings.Contains(loc, "ABCDEF") {
		t.Errorf("Schlüssel in der Adresse: %s", loc)
	}
	stored := h.dataNetSetting(ctx, dataNetKeyAuth+server)
	if stored == "" || strings.HasPrefix(stored, "tskey-") || strings.Contains(stored, "ABCDEF") {
		t.Errorf("Schlüssel nicht verschlüsselt gespeichert: %q", stored)
	}
	if _, got := h.dataNetSettings(ctx); got != key {
		t.Errorf("Schlüssel nicht lesbar: %q", got)
	}
	if h.dataNetSetting(ctx, dataNetKeyEnabled) != "false" {
		t.Errorf("Schalter aus erwartet: %q", h.dataNetSetting(ctx, dataNetKeyEnabled))
	}
	// Die Seite zeigt den Schlüssel nie, nur dass einer hinterlegt ist.
	page := dnDo(h.DataNetPage, "GET", "/datanet", nil, nil).Body.String()
	if strings.Contains(page, "ABCDEF") || strings.Contains(page, stored) || !strings.Contains(page, "hinterlegt") {
		t.Error("Seite zeigt den Schlüssel oder nicht, dass er hinterlegt ist")
	}
	// Leeres Feld lässt den Schlüssel stehen; falsche Form und fremde Namen werden abgewiesen.
	dnDo(h.DataNetSettings, "POST", "/datanet/settings", nil, url.Values{"authkey." + server: {""}})
	if h.dataNetSetting(ctx, dataNetKeyAuth+server) != stored {
		t.Error("leeres Feld hat den Schlüssel gelöscht")
	}
	for _, bad := range []url.Values{
		{"authkey." + server: {"kein-schluessel"}},
		{"authkey.../../x": {"tskey-auth-x"}},
		{"authkey.UPPER": {"tskey-auth-x"}},
	} {
		w := dnDo(h.DataNetSettings, "POST", "/datanet/settings", nil, bad)
		if !strings.Contains(w.Header().Get("Location"), "e=") {
			t.Errorf("%v: Fehler erwartet, Antwort %d %s", bad, w.Code, w.Header().Get("Location"))
		}
	}
	if h.dataNetSetting(ctx, dataNetKeyAuth+"../../x") != "" || h.dataNetSetting(ctx, dataNetKeyAuth+"UPPER") != "" {
		t.Error("Schlüssel für ungültigen Servernamen gespeichert")
	}
}

func TestDataNetPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	now := time.Now()
	ok := []datanet.TargetStatus{{Port: 1433, OK: true}}
	bad := []datanet.TargetStatus{{Port: 1433, OK: false, Error: "Verbindung abgelehnt"}}
	clients := []dataNetClient{
		{ID: "1", Name: "werk-ok", Hostname: "PC1", Version: pdh.Version(), OS: "windows", Targets: ok, LastSeen: now.Add(-10 * time.Second)},
		{ID: "2", Name: "werk-offline", Version: pdh.Version(), Targets: ok, LastSeen: now.Add(-time.Hour)},
		{ID: "3", Name: "werk-db", Version: pdh.Version(), Targets: bad, LastSeen: now},
		{ID: "4", Name: "werk-gesperrt", Revoked: true, RevokedAt: now, LastSeen: now},
		{ID: "5", Name: "werk-neu"},
		{ID: "6", Name: "werk-alt", Version: "0.0.1", Targets: ok, LastSeen: now},
	}
	rows := dataNetRows(clients, now, pdh.Version())
	wantState := []string{"ok", "offline", "db_down", "revoked", "new", "ok"}
	for i, r := range rows {
		if r.State != wantState[i] {
			t.Errorf("%s: Zustand %q, erwartet %q", r.Name, r.State, wantState[i])
		}
	}
	if rows[5].VersionDiff != true || rows[0].VersionDiff || rows[4].VersionDiff {
		t.Errorf("Versionsabweichung: %+v", rows)
	}
	for _, d := range []DataNetPageData{
		{CanEdit: true, CanAdmin: true, Enabled: true, NetState: "running", NetStateLabel: "Verbunden", ACL: dataNetACL(nil),
			Servers:   []dataNetServerRow{{Name: "srv-a", Self: true, Addrs: "100.64.0.1"}, {Name: "srv-b", HasKey: true}},
			Downloads: dataNetDownloads(), Version: pdh.Version()},
		{CanEdit: true, CanAdmin: true, Clients: rows, ACL: dataNetACL([]int{1433}), Version: pdh.Version(),
			Created: &dataNetCreated{Name: "x", ID: "i", Token: "TOKENWERT", Windows: "w-befehl", Linux: "l-befehl",
				PrepWindows: "pw", SafeWindows: "sw", PrepLinux: "pl", SafeLinux: "sl"}},
		{Clients: rows, ACL: dataNetACL(nil), Err: "Fehler"}, // nur ansehen: keine Knöpfe
	} {
		out := renderPage(t, tmpl, "datanet", d)
		if len(d.Clients) == 0 {
			continue
		}
		for _, want := range []string{"werk-ok", "werk-offline", "werk-gesperrt", "werk-neu", "Noch keine Meldung", "Offline", "Gesperrt", "andere Version als das PDH", "Verbindung abgelehnt"} {
			if !strings.Contains(out, want) {
				t.Errorf("Seite enthält %q nicht", want)
			}
		}
		if d.CanEdit {
			if !strings.Contains(out, "/datanet/clients/1/revoke") || !strings.Contains(out, "/datanet/clients/1/delete") {
				t.Error("Knöpfe Sperren/Entfernen fehlen")
			}
			if strings.Contains(out, "/datanet/clients/4/revoke") {
				t.Error("gesperrter Client kann noch gesperrt werden")
			}
		} else if strings.Contains(out, "/revoke") || strings.Contains(out, "/delete") {
			t.Error("Knöpfe ohne Recht sichtbar")
		}
		if strings.Count(out, "andere Version als das PDH") != 1 {
			t.Errorf("Versionshinweis: %d", strings.Count(out, "andere Version als das PDH"))
		}
	}
}

func json_(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func TestDataNetPDHAddrs(t *testing.T) {
	nodes := []dataNetNode{
		{Server: "b", Addrs: []string{"fd7a::2", "100.64.0.2"}},
		{Server: "a", Addrs: []string{"100.64.0.1"}},
	}
	// Der eigene Server steht vorn; ein Server ohne Knotendaten (nur Schlüssel) hat nur den Namen.
	got := dataNetPDHAddrs("a", []string{"c", "a", "b"}, nodes)
	want := []string{"pdh-a", "100.64.0.1", "pdh-b", "100.64.0.2", "pdh-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Ziele = %v, erwartet %v", got, want)
	}
	// Ohne weitere Server genügt der eigene Name.
	if got := dataNetPDHAddrs("x", nil, nil); !reflect.DeepEqual(got, []string{"pdh-x"}) {
		t.Fatalf("allein: %v", got)
	}
}
