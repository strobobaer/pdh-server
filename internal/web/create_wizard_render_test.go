package web

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Leitstand: Anlegen nur noch ueber den Erstellungs-Assistenten im
// Leitstand-Modus – ohne Zuweisung, mit Melder, Verteilung an die Broker.
func TestGlobalDashboardUsesBoardWizard(t *testing.T) {
	render := func(priv bool) string {
		t.Helper()
		c, err := loadTestTemplates(t).Clone()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "global_dashboard.gohtml", GlobalDashboardPageData{CanCreatePrivileged: priv, DefaultDueDaysTicket: 3, DefaultDueDaysTask: 5, DefaultDueDaysMaintenance: 7}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	out := render(false)
	for _, want := range []string{`id="crw"`, `const BOARD =  true `, `id="crw-rep"`, `id="crw-infra-list"`, "Wer meldet?", "an die <b>Broker</b>",
		"window.PDH_CREATE_BOARD", "['fault', 'ticket']", "item.status_key === 'open'", ".cw-overlay{", "tabler-icons"} {
		if !strings.Contains(out, want) {
			t.Errorf("Leitstand enthält %q nicht", want)
		}
	}
	for _, bad := range []string{`id="create-form"`, `id="crw-people"`, `id="crw-resp"`, "Wer kümmert sich darum?", "infra-picker-widget"} {
		if strings.Contains(out, bad) {
			t.Errorf("Leitstand enthält noch %q", bad)
		}
	}
	if !strings.Contains(render(true), "['fault', 'ticket', 'task', 'maintenance']") {
		t.Error("Admins/Manager sehen Aufgaben und Wartungen nicht")
	}
	checkScripts(t, "leitstand", out)

	// normale Seiten: Assistent mit Zuweisung
	page := renderPage(t, loadTestTemplates(t), "tickets", TicketsPageData{})
	for _, want := range []string{`const BOARD =  false `, `id="crw-people"`, "Wer kümmert sich darum?", "openFromURL", "[data-create]"} {
		if !strings.Contains(page, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(page, `id="crw-rep"`) || strings.Contains(page, `id="new-ticket-form"`) {
		t.Error("Seite enthält Leitstand-Felder oder das alte Formular")
	}
	checkScripts(t, "tickets", page)
}

// checkScripts prueft alle Inline-Skripte der Seite mit node --check (falls vorhanden).
func checkScripts(t *testing.T, name, html string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Log("node nicht gefunden – Skriptprüfung übersprungen")
		return
	}
	re := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	dir := t.TempDir()
	for i, m := range re.FindAllStringSubmatch(html, -1) {
		f := filepath.Join(dir, name+"-"+strconv.Itoa(i)+".js")
		if err := os.WriteFile(f, []byte(m[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("%s: Skript %d fehlerhaft: %s", name, i, out)
		}
	}
}


// Mehrsprachigkeit Stufe 3: beide Assistenten erscheinen in der gewaehlten
// Sprache – auch die Texte im Skript – und bleiben gueltiges JavaScript.
func TestWizardsTranslated(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	page := renderPage(t, bindLang(c, "en"), "tickets", TicketsPageData{})
	for _, want := range []string{"What would you like to create?", "Who takes care of it?", `F("Create %s", t.label)`,
		"Which material was used?", "How long was the work?", `F("Complete %s", i.Label)`, "<b>Done</b> books the material"} {
		if !strings.Contains(page, want) {
			t.Errorf("englische Seite enthält %q nicht", want)
		}
	}
	for _, bad := range []string{"Was möchtest du anlegen?", "Welches Material wurde verwendet?", "' anlegen'", "' abschließen'"} {
		if strings.Contains(page, bad) {
			t.Errorf("englische Seite enthält noch %q", bad)
		}
	}
	checkScripts(t, "tickets-en", page)

	b, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	b = bindLang(b, "tr")
	if _, err := b.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := b.ExecuteTemplate(&buf, "global_dashboard.gohtml", GlobalDashboardPageData{}); err != nil {
		t.Fatal(err)
	}
	board := buf.String()
	for _, want := range []string{`<html lang="tr">`, "Kim bildiriyor?", "<b>brokerlara</b>"} {
		if !strings.Contains(board, want) {
			t.Errorf("türkischer Leitstand enthält %q nicht", want)
		}
	}
	checkScripts(t, "leitstand-tr", board)
}

func TestCompletionMessagesTranslated(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	in := completionRequest{Comment: "ok"}
	err := validateCompletion(&in, 0, 0, false)
	if err == nil {
		t.Fatal("kurzer Kommentar wurde angenommen")
	}
	if got := tr("en", err.Error()); got != "Please briefly describe what was done (comment)." {
		t.Errorf("Meldung nicht übersetzt: %q", got)
	}
	if got := brokerMessage("ro", "An Broker verteilt: Ana, Ion"); got != "Distribuit brokerilor: Ana, Ion" {
		t.Errorf("Broker-Meldung: %q", got)
	}
	if got := brokerMessage("de", ""); got != "" {
		t.Errorf("leere Broker-Meldung: %q", got)
	}
}
