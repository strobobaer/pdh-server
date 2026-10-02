package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteEnvFileKeepsCommentsAndQuotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pdh.env")
	orig := "# Kopf\nPDH_SERVER_PORT=8090\n\n# Mail\nexport PDH_SMTP_HOST=mail.example.com # alt\nPDH_OTHER='x y'\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteEnvFile(path, map[string]string{
		"PDH_SMTP_HOST":     "smtp.firma.de",
		"PDH_SMTP_PASSWORD": `pa ss"wo#rd\x`,
		"PDH_EMPTY":         "",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"# Kopf", "# Mail", "PDH_SERVER_PORT=8090", "PDH_SMTP_HOST=smtp.firma.de", "PDH_OTHER='x y'"} {
		if !strings.Contains(s, want) {
			t.Errorf("Datei enthält %q nicht:\n%s", want, s)
		}
	}
	vals, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vals["PDH_SMTP_PASSWORD"] != `pa ss"wo#rd\x` {
		t.Errorf("Passwort: %q", vals["PDH_SMTP_PASSWORD"])
	}
	if vals["PDH_OTHER"] != "x y" || vals["PDH_EMPTY"] != "" || vals["PDH_SERVER_PORT"] != "8090" {
		t.Errorf("Werte: %v", vals)
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("Rechte zu offen: %v", st.Mode())
	}
	if err := WriteEnvFile(path, map[string]string{"BAD KEY": "x"}); err == nil {
		t.Error("ungültiger Name akzeptiert")
	}
}

func TestParseEnvLine(t *testing.T) {
	cases := map[string][2]string{
		`A=1`:                          {"A", "1"},
		`export B="x\"y"`:              {"B", `x"y`},
		`C=wert # kommentar`:           {"C", "wert"},
		`D="mit # raute"`:              {"D", "mit # raute"},
		string(rune(0xFEFF)) + "E=bom": {"E", "bom"},
		`F=a=b`:                        {"F", "a=b"},
	}
	for line, want := range cases {
		k, v, ok := parseEnvLine(line)
		if !ok || k != want[0] || v != want[1] {
			t.Errorf("%q → %q=%q (%v)", line, k, v, ok)
		}
	}
	for _, line := range []string{"# nur Kommentar", "", "ohne gleich", "1X=a"} {
		if _, _, ok := parseEnvLine(line); ok {
			t.Errorf("%q sollte ignoriert werden", line)
		}
	}
}

func TestRandomSecret(t *testing.T) {
	a, b := RandomSecret(32), RandomSecret(32)
	if len(a) != 64 || a == b {
		t.Errorf("Secret %q / %q", a, b)
	}
}

func TestEmptyProcessEnvDoesNotOverride(t *testing.T) {
	t.Setenv("PDH_TEST_EMPTY_FROM_OUTSIDE", "")
	t.Setenv("PDH_TEST_SET_FROM_OUTSIDE", "x")
	t.Setenv("PDH_ENV_FILE", filepath.Join(t.TempDir(), "pdh.env"))
	origOnce = sync.Once{}
	t.Cleanup(func() { origOnce = sync.Once{} })
	if FromProcessEnv("PDH_TEST_EMPTY_FROM_OUTSIDE") {
		t.Error("leere Variable von außen verdeckt die Datenbank")
	}
	if !FromProcessEnv("PDH_TEST_SET_FROM_OUTSIDE") {
		t.Error("gesetzte Variable von außen hat keinen Vorrang mehr")
	}
}
