package directory

import (
	"strings"
	"testing"
)

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(n))
	}
	return out
}

// Spaltenliste, Parameter und Datums-Platzhalter muessen deckungsgleich
// sein - ein Versatz wuerde Werte in falsche Spalten schreiben.
func TestFieldListsConsistent(t *testing.T) {
	names := splitNames(fieldNames)
	if len(names) != fieldCount {
		t.Fatalf("fieldNames: %d Spalten, fieldCount %d", len(names), fieldCount)
	}
	if n := len(fieldArgs(&Fields{})); n != fieldCount {
		t.Fatalf("fieldArgs: %d Werte, fieldCount %d", n, fieldCount)
	}
	for i, n := range names {
		isDate := strings.Contains(fieldValue(i+1), "::date")
		wantDate := n == "contract_valid_until" || n == "last_evaluation_at"
		if isDate != wantDate {
			t.Errorf("Spalte %d (%s): Datums-Platzhalter=%v", i+1, n, isDate)
		}
	}
	// Reihenfolge von fieldArgs stichprobenartig gegen fieldNames pruefen
	f := Fields{SupportHotline: "hotline", SparePartsEmail: "et@x", Notes: "notiz", Currency: "EUR"}
	args := fieldArgs(&f)
	for i, n := range names {
		switch n {
		case "support_hotline", "spare_parts_email", "notes", "currency":
			if s, _ := args[i].(string); s == "" {
				t.Errorf("Spalte %s bekommt nicht den erwarteten Wert (Index %d)", n, i)
			}
		}
	}
}

func TestPartnerColumnsMatchScan(t *testing.T) {
	// 43 Basis-Spalten + 6 Support-/Ersatzteil-Kontaktfelder
	cols := 0
	depth := 0
	for _, r := range partnerColumns {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				cols++
			}
		}
	}
	if cols+1 != 49 {
		t.Fatalf("partnerColumns hat %d Spalten, scanPartner erwartet 49", cols+1)
	}
}

func TestNormalize(t *testing.T) {
	f := Fields{Name: "  ACME ", Website: "acme.de", IBAN: "de12 3456", Rating: "b", Currency: ""}
	if err := f.Normalize(); err != nil {
		t.Fatal(err)
	}
	if f.Name != "ACME" || f.Website != "https://acme.de" || f.IBAN != "DE123456" || f.Rating != "B" ||
		f.Currency != "EUR" || f.Kind != KindManufacturer || f.ApprovalStatus != StatusApproved {
		t.Errorf("unerwartet normalisiert: %+v", f)
	}
	for _, bad := range []Fields{{}, {Name: "x", Kind: "foo"}, {Name: "x", Rating: "D"}, {Name: "x", Currency: "EURO"}} {
		b := bad
		if err := b.Normalize(); err == nil {
			t.Errorf("Fehler erwartet für %+v", bad)
		}
	}
}
