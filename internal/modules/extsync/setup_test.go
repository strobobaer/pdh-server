package extsync

import (
	"strings"
	"testing"
)

func setupProfile() *Profile {
	return &Profile{TargetType: RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"MeldungNr"},
		ChangeKind: changeKindRowVersion, ChangeColumn: "RV", TimeZone: "UTC"}
}

func setupComments() *CommentTable {
	return &CommentTable{Schema: "dbo", Table: "MeldungKommentare", FKColumn: "MeldungNr", TextColumn: "Text", AuthorColumn: "Verfasser", TimeColumn: "Zeit"}
}

// setupInfo: Meldungstabelle mit Primärschlüssel MeldungNr.
func setupInfo() *TableInfo {
	info := meldungenInfo()
	info.UniqueKeys = [][]string{{"MeldungNr"}}
	return info
}

func TestCheckSource(t *testing.T) {
	d := mustDialect(t)
	info := setupInfo()
	if err := CheckSource(d, info, setupProfile()); err != nil {
		t.Fatalf("gültige Quelle abgelehnt: %v", err)
	}
	cases := []struct {
		name string
		edit func(p *Profile)
		want string
	}{
		{"kein Schlüssel", func(p *Profile) { p.KeyColumns = nil }, "Schlüsselspalte wählen"},
		{"unbekannter Schlüssel", func(p *Profile) { p.KeyColumns = []string{"Gibtsnicht"} }, "gibt es in der Tabelle nicht"},
		{"Schlüssel nicht eindeutig", func(p *Profile) { p.KeyColumns = []string{"Titel"} }, "nicht eindeutig"},
		{"rowversion auf Textspalte", func(p *Profile) { p.ChangeColumn = "Titel" }, "keine rowversion-Spalte"},
		{"Zeitstempel auf Textspalte", func(p *Profile) { p.ChangeKind, p.ChangeColumn = changeKindTimestamp, "Titel" }, "keine Datums-/Zeitspalte"},
		{"unbekannte Änderungsspalte", func(p *Profile) { p.ChangeKind, p.ChangeColumn = changeKindTimestamp, "X" }, "gibt es in der Tabelle nicht"},
		{"ohne Erkennung mit Spalte", func(p *Profile) { p.ChangeKind = changeKindNone }, "keine Änderungsspalte"},
		{"unbekannte Art", func(p *Profile) { p.ChangeKind = "magie" }, "unbekannt"},
		{"Anlagedatum auf Textspalte", func(p *Profile) { p.CreatedColumn = "Titel" }, "keine Datums-/Zeitspalte"},
		{"Anlagedatum unbekannt", func(p *Profile) { p.CreatedColumn = "X" }, "gibt es in der Tabelle nicht"},
		{"Zeitzone unbekannt", func(p *Profile) { p.TimeZone = "Mond/Krater" }, "Zeitzone"},
		{"Filter verboten", func(p *Profile) { p.FilterSQL = "1=1; DELETE FROM x" }, ""},
	}
	for _, c := range cases {
		p := setupProfile()
		c.edit(p)
		err := CheckSource(d, info, p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, erwartet Text %q", c.name, err, c.want)
		}
	}
	p := setupProfile()
	p.ChangeKind, p.ChangeColumn, p.CreatedColumn = changeKindTimestamp, "Geaendert", "Faellig"
	if err := CheckSource(d, info, p); err != nil {
		t.Errorf("Zeitstempel und Anlagedatum: %v", err)
	}
	if err := CheckSource(d, nil, p); err == nil {
		t.Error("ohne gelesenen Aufbau muss die Prüfung scheitern")
	}
}

func TestChangeOptionsAndTimeColumns(t *testing.T) {
	d := mustDialect(t)
	opts := ChangeOptions(d, setupInfo())
	if len(opts) != 4 || opts[0] != (ChangeOption{changeKindRowVersion, "RV"}) || opts[len(opts)-1].Kind != changeKindNone {
		t.Fatalf("Auswahl: %+v", opts)
	}
	if got := TimeColumns(d, setupInfo()); len(got) != 2 || got[0] != "Faellig" || got[1] != "Geaendert" {
		t.Fatalf("Zeitspalten: %v", got)
	}
	if got := ChangeOptions(d, guidInfo()); len(got) != 1 || got[0].Kind != changeKindNone {
		t.Fatalf("Tabelle ohne Änderungsspalte: %+v", got)
	}
}

func TestOutLockReason(t *testing.T) {
	info := setupInfo()
	info.Columns = append(info.Columns, Column{Name: "Summe", Type: "int", IsComputed: true})
	p := setupProfile()
	for col, want := range map[string]string{
		"Titel": "", "": "", "MeldungNr": "Schlüsselspalte", "RV": "rowversion", "Summe": "berechnete",
	} {
		got := OutLockReason(info, p, col, true)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("Spalte %q: %q, erwartet %q", col, got, want)
		}
	}
	if got := OutLockReason(info, p, "Titel", false); !strings.Contains(got, "nicht erlaubt") {
		t.Errorf("ohne Schreiben erlaubt: %q", got)
	}
}

func TestMissingRequired(t *testing.T) {
	p := setupProfile()
	if got := MissingRequired(p, nil); len(got) != 1 || got[0].Key != "title" {
		t.Fatalf("ohne Zuordnung: %+v", got)
	}
	if got := MissingRequired(p, []FieldMap{{PDHField: "title", ForeignColumn: "Titel", Direction: DirOut}}); len(got) != 1 {
		t.Fatalf("nur hinaus zählt nicht: %+v", got)
	}
	if got := MissingRequired(p, []FieldMap{{PDHField: "title", ForeignColumn: "Titel", Direction: DirBoth}}); len(got) != 0 {
		t.Fatalf("zugeordnet: %+v", got)
	}
	if got := MissingRequired(p, []FieldMap{{PDHField: "title", FixedValue: fixedStr("Meldung"), Direction: DirIn}}); len(got) != 0 {
		t.Fatalf("fester Wert: %+v", got)
	}
	p.Defaults = map[string]string{"title": "Aus dem Fremdsystem"}
	if got := MissingRequired(p, nil); len(got) != 0 {
		t.Fatalf("Vorgabewert: %+v", got)
	}
}

func TestCheckMaps(t *testing.T) {
	d := mustDialect(t)
	info, kom := setupInfo(), kommentareInfo()
	good := func() []FieldMap {
		return []FieldMap{
			{PDHField: "title", ForeignColumn: "Titel", Direction: DirBoth},
			{PDHField: "priority", ForeignColumn: "Prio", Direction: DirIn, Translate: map[string]string{"hoch": "high", "Mittel": "MEDIUM"}},
			{PDHField: "status", FixedValue: fixedStr("open"), Direction: DirIn},
		}
	}
	p := setupProfile()
	p.CommentTable = setupComments()
	if errs := CheckMaps(d, info, kom, p, good(), true); len(errs) != 0 {
		t.Fatalf("gültige Zuordnung: %+v", errs)
	}
	cases := []struct {
		name  string
		edit  func(p *Profile, m []FieldMap) []FieldMap
		allow bool
		kom   *TableInfo
		want  string
	}{
		{"unbekanntes Feld", func(_ *Profile, m []FieldMap) []FieldMap { m[0].PDHField = "gibtsnicht"; return m }, true, kom, "gibt es im Ziel nicht"},
		{"doppelt", func(_ *Profile, m []FieldMap) []FieldMap { return append(m, m[0]) }, true, kom, "mehrfach"},
		{"unbekannte Spalte", func(_ *Profile, m []FieldMap) []FieldMap { m[0].ForeignColumn = "X"; return m }, true, kom, "gibt es in der Tabelle nicht"},
		{"Richtung", func(_ *Profile, m []FieldMap) []FieldMap { m[0].Direction = "quer"; return m }, true, kom, "Richtung"},
		{"leer", func(_ *Profile, m []FieldMap) []FieldMap { m[0].ForeignColumn = ""; return m }, true, kom, "weder Spalte noch fester Wert"},
		{"fester Wert hinaus", func(_ *Profile, m []FieldMap) []FieldMap { m[2].Direction = DirBoth; return m }, true, kom, "nur hereinkommend"},
		{"fester Wert ungültig", func(_ *Profile, m []FieldMap) []FieldMap { m[2].FixedValue = fixedStr("fertig"); return m }, true, kom, "kein gültiger Wert"},
		{"Übersetzung ungültig", func(_ *Profile, m []FieldMap) []FieldMap { m[1].Translate["egal"] = "wichtig"; return m }, true, kom, "keinem gültigen Wert"},
		{"hinaus ohne Schreiben", func(p *Profile, m []FieldMap) []FieldMap { p.CommentTable = nil; return m }, false, kom, "nicht erlaubt"},
		{"hinaus auf Schlüssel", func(_ *Profile, m []FieldMap) []FieldMap { m[0].ForeignColumn = "MeldungNr"; return m }, true, kom, "Schlüsselspalte"},
		{"Kommentare ohne Schreiben", func(_ *Profile, m []FieldMap) []FieldMap { m[0].Direction = DirIn; return m }, false, kom, "Kommentare: Schreiben"},
		{"Kommentartabelle fehlt", func(_ *Profile, m []FieldMap) []FieldMap { return m }, true, nil, "gibt es nicht"},
		{"Kommentartabelle = Meldungstabelle", func(_ *Profile, m []FieldMap) []FieldMap { return m }, true, info, "Meldungstabelle selbst"},
		{"Kommentarspalte unbekannt", func(p *Profile, m []FieldMap) []FieldMap { p.CommentTable.AuthorColumn = "X"; return m }, true, kom, "Spalte ‚X‘"},
		{"Kommentartext fehlt", func(p *Profile, m []FieldMap) []FieldMap { p.CommentTable.TextColumn = ""; return m }, true, kom, "Spalte für Text fehlt"},
		{"zusammengesetzter Schlüssel", func(p *Profile, m []FieldMap) []FieldMap {
			p.KeyColumns = []string{"MeldungNr", "Titel"}
			m[0].Direction = DirIn
			return m
		}, true, kom, "genau einer Schlüsselspalte"},
	}
	for _, c := range cases {
		p := setupProfile()
		p.CommentTable = setupComments()
		maps := c.edit(p, good())
		errs := CheckMaps(d, info, c.kom, p, maps, c.allow)
		found := false
		for _, e := range errs {
			if strings.Contains(e.Message, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %+v, erwartet Text %q", c.name, errs, c.want)
		}
	}
	if errs := CheckMaps(d, nil, nil, p, good(), true); len(errs) != 1 {
		t.Errorf("ohne Aufbau: %+v", errs)
	}
}
