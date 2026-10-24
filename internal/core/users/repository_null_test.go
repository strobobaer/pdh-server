package users

import (
	"os"
	"strings"
	"testing"
)

// department/phone können in älteren Daten NULL sein; die Abfragen lesen sie in
// string – ohne COALESCE scheitert z. B. die Anmeldung mit „benutzer nicht gefunden“.
func TestUserSelectsCoalesceTextColumns(t *testing.T) {
	b, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "INSERT") || !strings.Contains(l, "role, ") {
			continue
		}
		if strings.Contains(l, " department,") || strings.Contains(l, " phone,") {
			t.Errorf("repository.go:%d liest department/phone ohne COALESCE: %s", i+1, l)
		}
	}
}
