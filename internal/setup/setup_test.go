package setup

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCode(t *testing.T) {
	w := &wizard{code: newCode()}
	if len(w.code) != 9 || w.code[4] != '-' {
		t.Fatalf("Code %q", w.code)
	}
	if err := w.checkCode(strings.ToLower(strings.ReplaceAll(w.code, "-", ""))); err != nil {
		t.Errorf("Code ohne Bindestrich/klein abgelehnt: %v", err)
	}
	for i := 0; i < 10; i++ {
		_ = w.checkCode("FALSCH")
	}
	if err := w.checkCode(w.code); err == nil {
		t.Error("nach 10 Fehlversuchen nicht gesperrt")
	}
}

func TestAdminFromForm(t *testing.T) {
	ok := url.Values{"first_name": {"Max"}, "last_name": {"Muster"}, "username": {"Admin"}, "email": {"max@firma.de"},
		"password": {"sehrgeheim1"}, "password2": {"sehrgeheim1"}}
	r := httptest.NewRequest("POST", "/setup/finish", strings.NewReader(ok.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a, err := adminFromForm(r)
	if err != nil || a.username != "admin" {
		t.Fatalf("%v %+v", err, a)
	}
	bad := []url.Values{
		{"first_name": {"M"}, "last_name": {"M"}, "username": {"ad"}, "email": {"a@b.de"}, "password": {"sehrgeheim1"}, "password2": {"sehrgeheim1"}},
		{"first_name": {"M"}, "last_name": {"M"}, "username": {"admin"}, "email": {"kein"}, "password": {"sehrgeheim1"}, "password2": {"sehrgeheim1"}},
		{"first_name": {"M"}, "last_name": {"M"}, "username": {"admin"}, "email": {"a@b.de"}, "password": {"kurz"}, "password2": {"kurz"}},
		{"first_name": {"M"}, "last_name": {"M"}, "username": {"admin"}, "email": {"a@b.de"}, "password": {"sehrgeheim1"}, "password2": {"anders12345"}},
		{"first_name": {"M"}, "last_name": {"M"}, "username": {"pdh-system"}, "email": {"a@b.de"}, "password": {"sehrgeheim1"}, "password2": {"sehrgeheim1"}},
	}
	for _, v := range bad {
		r := httptest.NewRequest("POST", "/setup/finish", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, err := adminFromForm(r); err == nil {
			t.Errorf("akzeptiert: %v", v)
		}
	}
}

func TestSetupTemplateRenders(t *testing.T) {
	path := filepath.Join("..", "..", "web", "templates", "setup.gohtml")
	tmpl, err := template.New("setup.gohtml").Funcs(template.FuncMap{"list": func(v ...string) []string { return v }}).ParseFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []pageData{
		{NeedDB: true, DBError: "connection refused", Form: map[string]string{}, EnvFile: ".env", PublicURL: "http://pdh:8090", DefaultDBHost: "localhost"},
		{NeedDB: false, Form: map[string]string{"username": "chef"}, EnvFile: ".env"},
		{Done: true, DoneNotes: []string{"Datenbank pdh angelegt"}, Form: map[string]string{}},
		{NeedDB: true, DBFromEnv: true, Form: map[string]string{}},
	} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "setup", d); err != nil {
			t.Fatalf("%+v: %v", d, err)
		}
		out := buf.String()
		switch {
		case d.Done:
			if !strings.Contains(out, "Einrichtung abgeschlossen") || !strings.Contains(out, "Datenbank pdh angelegt") {
				t.Error("Abschlussseite unvollständig")
			}
		case d.DBFromEnv:
			if strings.Contains(out, `name="admin_password"`) || !strings.Contains(out, "von außen vorgegeben") {
				t.Error("Docker-Hinweis fehlt / Eingaben trotzdem angeboten")
			}
		case d.NeedDB:
			for _, want := range []string{"Einrichtungscode", "Automatisch anlegen", "connection refused", `name="new_db"`, `name="password2"`, "http://pdh:8090"} {
				if !strings.Contains(out, want) {
					t.Errorf("Assistent enthält %q nicht", want)
				}
			}
		default:
			if strings.Contains(out, "Automatisch anlegen") || !strings.Contains(out, `value="chef"`) {
				t.Error("Nur-Administrator-Modus falsch")
			}
		}
	}
}
