//go:build integration

// Speicher der berechneten Werte gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration ./internal/modules/calc/
package calc

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/formula"
	"pdh/pkg/database"
)

// env: Benutzer „Anna Muster“, CSV-Import-Verbindung, zwei Anlagen; die Uhr
// des Speichers ist gestellt (heute = 23.11.2026).
type env struct {
	pool                           *pgxpool.Pool
	s                              *Store
	uid, conn, assetA, assetB, sfx string
	today, now                     time.Time
}

func day(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }

func ptr(n int) *int { return &n }

func calcEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", ""),
		today: day(23), now: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
	e.s = NewStore(pool)
	e.s.Today = func() time.Time { return e.today }
	e.s.Now = func() time.Time { return e.now }
	e.uid = e.id(t, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Anna', 'Muster', 'admin', '', '') RETURNING id::text`, "cv"+e.sfx)
	e.conn = e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'csv', $1, '{}'::jsonb) RETURNING id::text`, "Quelle "+e.sfx)
	e.assetA = e.id(t, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse A "+e.sfx)
	e.assetB = e.id(t, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse B "+e.sfx)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE infrastructure_id = ANY($1::uuid[])`, []string{e.assetA, e.assetB})
		_, _ = pool.Exec(c, `DELETE FROM import_mappings WHERE infrastructure_id = ANY($1::uuid[])`, []string{e.assetA, e.assetB})
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE name LIKE '%' || $1 AND kind <> 'calc'`, e.sfx)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = ANY($1::uuid[])`, []string{e.assetA, e.assetB})
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, e.uid)
	})
	return e
}

func (e *env) id(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// src legt eine Zuordnung an der CSV-Verbindung an; value "" und at Null =
// hat noch nie einen Wert geliefert.
func (e *env) src(t *testing.T, asset, name, value string, at time.Time) string {
	t.Helper()
	id := e.id(t, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by)
		VALUES ($1::uuid, $2, $3::uuid, $4, '', $5::uuid) RETURNING id::text`, e.conn, "spalte-"+name, asset, name+" "+e.sfx, e.uid)
	if !at.IsZero() {
		e.set(t, id, &value, at)
	}
	return id
}

// set stellt Wert und Zeitpunkt einer Zuordnung (value nil = kein Wert).
func (e *env) set(t *testing.T, id string, value *string, at time.Time) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE import_mappings SET last_value = $2, last_received_at = $3 WHERE id = $1::uuid`, id, value, at); err != nil {
		t.Fatal(err)
	}
}

func (e *env) values(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM calc_values cv JOIN import_mappings m ON m.id = cv.mapping_id
		WHERE m.infrastructure_id = ANY($1::uuid[])`, []string{e.assetA, e.assetB}).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) save(t *testing.T, in SaveInput) SaveResult {
	t.Helper()
	res, err := e.s.Save(context.Background(), in, e.uid)
	if err != nil {
		t.Fatalf("Save %q: %v", in.Name, err)
	}
	return res
}

func (e *env) get(t *testing.T, id string) *Value {
	t.Helper()
	v, err := e.s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return v
}

// sum: zwei Quellen A und B an Anlage A und der berechnete Wert „Summe“ (A + b).
func (e *env) sum(t *testing.T) (a, b string, in SaveInput, res SaveResult) {
	t.Helper()
	a = e.src(t, e.assetA, "A", "12,4", e.now.Add(-2*time.Second))
	b = e.src(t, e.assetA, "B", "11.9", e.now.Add(-3*time.Second))
	in = SaveInput{Name: "Summe " + e.sfx, InfraID: e.assetA, Unit: "kW", Decimals: 1, MaxAgeMinutes: ptr(10),
		Formula: "A + b", Inputs: []InputRef{{Short: "A", MappingID: a}, {Short: "B", MappingID: b}}}
	res = e.save(t, in)
	in.ID = res.ID
	return a, b, in, res
}

func statuses(v *Value) string {
	var s []string
	for _, x := range v.Versions {
		s = append(s, x.Status+" "+x.ValidFrom.Format("02.01.")+" "+x.Formula)
	}
	return strings.Join(s, " | ")
}

func TestMigrationBuiltinConnection(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	var n int
	var direction string
	var enabled bool
	if err := e.pool.QueryRow(ctx, `SELECT count(*), min(direction), bool_and(enabled) FROM import_export_connections WHERE kind = 'calc'`).Scan(&n, &direction, &enabled); err != nil {
		t.Fatal(err)
	}
	if n != 1 || direction != "import" || !enabled {
		t.Fatalf("eingebaute Verbindung: %d %q %v", n, direction, enabled)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO import_export_connections (direction, kind, name) VALUES ('import', 'calc', 'Zweite')`); err == nil {
		t.Fatal("eine zweite Verbindung der Art calc darf es nicht geben")
	}
	id, err := e.s.ConnectionID(ctx)
	if err != nil || id == "" {
		t.Fatalf("ConnectionID: %q %v", id, err)
	}
}

func TestSaveCreateAndGet(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a, _, _, res := e.sum(t)
	if !res.Created || res.Version != "created" || res.ID == "" || res.VersionID == "" {
		t.Fatalf("Ergebnis: %+v", res)
	}
	v := e.get(t, res.ID)
	if v.Name != "Summe "+e.sfx || v.InfraID != e.assetA || v.InfraPath != "Presse A "+e.sfx || v.Unit != "kW" || v.Decimals != 1 || v.MaxAgeMinutes == nil || *v.MaxAgeMinutes != 10 {
		t.Errorf("Wert: %+v", v)
	}
	if len(v.Versions) != 1 || v.Current() == nil {
		t.Fatalf("Fassungen: %s", statuses(v))
	}
	cur := v.Current()
	if cur.Status != StatusCurrent || cur.CreatedBy != "Anna Muster" || !cur.ValidFrom.Equal(day(23)) || cur.Formula != "A + b" || len(cur.Inputs) != 2 {
		t.Errorf("Fassung: %+v", cur)
	}
	if in := cur.Inputs[0]; in.Short != "A" || in.MappingID != a || in.Label != "Presse A "+e.sfx+" › A "+e.sfx || in.Value != "12,4" || in.Gone || in.Calc {
		t.Errorf("Eingangswert: %+v", in)
	}
	var conn, ref string
	if err := e.pool.QueryRow(ctx, `SELECT connection_id::text, source_ref FROM import_mappings WHERE id = $1::uuid`, res.ID).Scan(&conn, &ref); err != nil {
		t.Fatal(err)
	}
	want, _ := e.s.ConnectionID(ctx)
	if conn != want || !strings.HasPrefix(ref, "calc:") {
		t.Errorf("Zuordnung: Verbindung %s, Quelle %q", conn, ref)
	}
	list, err := e.s.List(ctx)
	found := false
	for _, x := range list {
		found = found || x.ID == res.ID
	}
	if err != nil || !found {
		t.Errorf("List findet den Wert nicht: %v", err)
	}
	if _, err := e.s.Get(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("eine gewöhnliche Zuordnung ist kein berechneter Wert: %v", err)
	}
}

func TestSaveValidation(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a := e.src(t, e.assetA, "A", "1", e.now)
	b := e.src(t, e.assetA, "B", "2", e.now)
	base := func() SaveInput {
		return SaveInput{Name: "Prüfung " + e.sfx, InfraID: e.assetA, Decimals: 2, Formula: "A + B",
			Inputs: []InputRef{{Short: "A", MappingID: a}, {Short: "B", MappingID: b}}}
	}
	many := make([]InputRef, MaxInputs+1)
	for i := range many {
		many[i] = InputRef{Short: "X" + strings.Repeat("a", i%5) + string(rune('A'+i%26)) + string(rune('A'+i/26)), MappingID: a}
	}
	var (
		sn *ShortNameError
		ui *UnknownInputError
		ig *InputGoneError
		se *formula.SyntaxError
	)
	cases := []struct {
		name string
		mod  func(in *SaveInput)
		ok   func(err error) bool
	}{
		{"Name zu kurz", func(in *SaveInput) { in.Name = "x" }, func(err error) bool { return errors.Is(err, ErrBadName) }},
		{"keine Anlage", func(in *SaveInput) { in.InfraID = "" }, func(err error) bool { return errors.Is(err, ErrNoInfra) }},
		{"Anlage unbekannt", func(in *SaveInput) { in.InfraID = "00000000-0000-7000-8000-000000000001" }, func(err error) bool { return errors.Is(err, ErrNoInfra) }},
		{"Einheit zu lang", func(in *SaveInput) { in.Unit = strings.Repeat("k", 21) }, func(err error) bool { return errors.Is(err, ErrBadUnit) }},
		{"Nachkommastellen", func(in *SaveInput) { in.Decimals = 7 }, func(err error) bool { return errors.Is(err, ErrBadDecimals) }},
		{"Höchstalter 0", func(in *SaveInput) { in.MaxAgeMinutes = ptr(0) }, func(err error) bool { return errors.Is(err, ErrBadMaxAge) }},
		{"Höchstalter zu groß", func(in *SaveInput) { in.MaxAgeMinutes = ptr(44641) }, func(err error) bool { return errors.Is(err, ErrBadMaxAge) }},
		{"Kurzname Funktion", func(in *SaveInput) { in.Inputs[0].Short = "MIN" }, func(err error) bool { return errors.As(err, &sn) }},
		{"Kurzname Ziffer", func(in *SaveInput) { in.Inputs[0].Short = "1A" }, func(err error) bool { return errors.As(err, &sn) }},
		{"Kurzname doppelt", func(in *SaveInput) { in.Inputs[1].Short = "a" }, func(err error) bool { return errors.As(err, &sn) }},
		{"unbekannter Name", func(in *SaveInput) { in.Formula = "A + C" }, func(err error) bool { return errors.As(err, &ui) && ui.Short == "C" }},
		{"Zuordnung fehlt", func(in *SaveInput) { in.Inputs[1].MappingID = "00000000-0000-7000-8000-000000000002" }, func(err error) bool { return errors.As(err, &ig) && ig.Short == "B" }},
		{"Zuordnung ungültig", func(in *SaveInput) { in.Inputs[1].MappingID = "nichts" }, func(err error) bool { return errors.As(err, &ig) }},
		{"zu viele", func(in *SaveInput) { in.Inputs = many; in.Formula = "1" }, func(err error) bool { return errors.Is(err, ErrTooManyInputs) }},
		{"Schreibfehler", func(in *SaveInput) { in.Formula = "A +" }, func(err error) bool { return errors.As(err, &se) }},
		{"Formel leer", func(in *SaveInput) { in.Formula = "  " }, func(err error) bool { return errors.As(err, &se) }},
		{"gestern", func(in *SaveInput) { in.ValidFrom = day(22) }, func(err error) bool { return errors.Is(err, ErrPastDate) }},
	}
	for _, c := range cases {
		in := base()
		c.mod(&in)
		_, err := e.s.Save(ctx, in, e.uid)
		if err == nil || !c.ok(err) || !UserError(err) {
			t.Errorf("%s: %v", c.name, err)
		}
		if verr := e.s.Validate(ctx, &in); verr == nil || !c.ok(verr) {
			t.Errorf("%s (Validate): %v", c.name, verr)
		}
		if n := e.values(t); n != 0 {
			t.Fatalf("%s: es wurde etwas gespeichert (%d)", c.name, n)
		}
	}
	// Validate prüft, schreibt aber nichts; nicht benutzte Eingangswerte sind erlaubt.
	in := base()
	in.Formula = " [A] * 2 "
	if err := e.s.Validate(ctx, &in); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if in.Formula != "[A] * 2" || !in.ValidFrom.Equal(day(23)) {
		t.Errorf("Validate trimmt und setzt heute: %q %v", in.Formula, in.ValidFrom)
	}
	if n := e.values(t); n != 0 {
		t.Fatalf("Validate hat gespeichert (%d)", n)
	}
}

func TestVersions(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a, b, in, res := e.sum(t)

	// gleiche Formel noch einmal: keine neue Fassung, aber die Angaben zum Wert ändern sich sofort
	in.Unit, in.InfraID, in.Formula = "MW", e.assetB, " A + b "
	in.Inputs = []InputRef{{Short: "b", MappingID: b}, {Short: "a", MappingID: a}}
	if r := e.save(t, in); r.Version != "unchanged" || r.Created {
		t.Fatalf("gleiche Formel: %+v", r)
	}
	v := e.get(t, res.ID)
	if len(v.Versions) != 1 || v.Unit != "MW" || v.InfraID != e.assetB {
		t.Fatalf("nach unverändert: %s, %+v", statuses(v), v)
	}

	// andere Formel heute: die neue gilt, die alte ist abgelöst
	e.now = e.now.Add(time.Minute)
	in.Formula = "A + B + 1"
	if r := e.save(t, in); r.Version != "created" {
		t.Fatalf("neue Formel: %+v", r)
	}
	if got := statuses(e.get(t, res.ID)); got != "gilt 23.11. A + B + 1 | abgelöst 23.11. A + b" {
		t.Fatalf("Fassungen: %s", got)
	}

	// geplante Fassung
	in.Formula, in.ValidFrom = "A * 2", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	planned := e.save(t, in)
	if got := statuses(e.get(t, res.ID)); planned.Version != "created" || !strings.HasPrefix(got, "geplant 01.12. A * 2 | gilt 23.11. A + B + 1") {
		t.Fatalf("geplant: %+v %s", planned, got)
	}
	// dieselbe geplante Formel noch einmal: unverändert
	if r := e.save(t, in); r.Version != "unchanged" {
		t.Errorf("gleiche geplante Formel: %+v", r)
	}
	// noch eine für denselben Tag: abgelehnt
	in.Formula = "A * 3"
	var pe *PlannedExistsError
	if _, err := e.s.Save(ctx, in, e.uid); !errors.As(err, &pe) || !pe.Date.Equal(in.ValidFrom) {
		t.Fatalf("zweite geplante Fassung: %v", err)
	}
	// die geplante ändern (auch das Datum)
	in.VersionID, in.ValidFrom = planned.VersionID, time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	if r := e.save(t, in); r.Version != "updated" || r.VersionID != planned.VersionID {
		t.Fatalf("geplante ändern: %+v", r)
	}
	v = e.get(t, res.ID)
	if got := statuses(v); !strings.HasPrefix(got, "geplant 02.12. A * 3 | gilt") {
		t.Fatalf("nach Ändern: %s", got)
	}
	// die geltende lässt sich nicht ändern oder löschen
	cur := v.Current()
	in.VersionID, in.ValidFrom = cur.ID, day(23)
	if _, err := e.s.Save(ctx, in, e.uid); !errors.Is(err, ErrVersionFixed) {
		t.Errorf("geltende ändern: %v", err)
	}
	if err := e.s.DeleteVersion(ctx, res.ID, cur.ID); !errors.Is(err, ErrVersionFixed) {
		t.Errorf("geltende löschen: %v", err)
	}
	// am Stichtag gilt die geplante
	e.today = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	if got := statuses(e.get(t, res.ID)); !strings.HasPrefix(got, "gilt 02.12. A * 3 | abgelöst 23.11. A + B + 1 | abgelöst") {
		t.Fatalf("am Stichtag: %s", got)
	}
	e.today = day(23)
	if err := e.s.DeleteVersion(ctx, res.ID, planned.VersionID); err != nil {
		t.Fatalf("geplante löschen: %v", err)
	}
	if got := statuses(e.get(t, res.ID)); strings.Contains(got, "geplant") {
		t.Errorf("nach Löschen: %s", got)
	}
}

func TestLastVersionStays(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a := e.src(t, e.assetA, "A", "1", e.now)
	res := e.save(t, SaveInput{Name: "Später " + e.sfx, InfraID: e.assetA, Formula: "A", ValidFrom: day(30),
		Inputs: []InputRef{{Short: "A", MappingID: a}}})
	v := e.get(t, res.ID)
	if v.Current() != nil || statuses(v) != "geplant 30.11. A" {
		t.Fatalf("nur geplant: %s", statuses(v))
	}
	if err := e.s.DeleteVersion(ctx, res.ID, res.VersionID); !errors.Is(err, ErrLastVersion) {
		t.Errorf("einzige Fassung löschen: %v", err)
	}
}

func TestCycle(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a := e.src(t, e.assetA, "A", "1", e.now)
	v1 := e.save(t, SaveInput{Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "A", Inputs: []InputRef{{Short: "A", MappingID: a}}})
	v2 := e.save(t, SaveInput{Name: "Zwei " + e.sfx, InfraID: e.assetA, Formula: "A", Inputs: []InputRef{{Short: "A", MappingID: a}}})
	// Zwei nutzt Eins erst in einer geplanten Fassung
	e.save(t, SaveInput{ID: v2.ID, Name: "Zwei " + e.sfx, InfraID: e.assetA, Formula: "V1 * 2", ValidFrom: day(30),
		Inputs: []InputRef{{Short: "V1", MappingID: v1.ID}}})
	loop := SaveInput{ID: v1.ID, Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "V2 + 1", Inputs: []InputRef{{Short: "V2", MappingID: v2.ID}}}
	var ce *CycleError
	_, err := e.s.Save(ctx, loop, e.uid)
	if !errors.As(err, &ce) || len(ce.Names) != 3 || ce.Names[0] != ce.Names[2] ||
		!strings.Contains(err.Error(), "Eins "+e.sfx) || !strings.Contains(err.Error(), "Zwei "+e.sfx) || strings.Contains(err.Error(), v1.ID) {
		t.Fatalf("Kreis: %v", err)
	}
	if err := e.s.CheckFormula(ctx, v1.ID, loop.Formula, loop.Inputs); !errors.As(err, &ce) {
		t.Errorf("CheckFormula: %v", err)
	}
	if got := statuses(e.get(t, v1.ID)); got != "gilt 23.11. A" {
		t.Errorf("nichts gespeichert: %s", got)
	}
	// sich selbst als Eingangswert
	self := SaveInput{ID: v1.ID, Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "X", Inputs: []InputRef{{Short: "X", MappingID: v1.ID}}}
	if _, err := e.s.Save(ctx, self, e.uid); !errors.As(err, &ce) {
		t.Errorf("Selbstbezug: %v", err)
	}
	// ohne Kreis geht es
	if err := e.s.CheckFormula(ctx, "", "V1 + V2", []InputRef{{Short: "V1", MappingID: v1.ID}, {Short: "V2", MappingID: v2.ID}}); err != nil {
		t.Errorf("neuer Wert aus beiden: %v", err)
	}
}

func TestUsersAndDelete(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a, b, in, res := e.sum(t)
	name := "Summe " + e.sfx
	one := func(got []string, err error) bool { return err == nil && len(got) == 1 && got[0] == name }
	if !one(e.s.UsersOfMapping(ctx, a)) || !one(e.s.UsersOfConnection(ctx, e.conn)) || !one(e.s.UsersOfSourcePrefix(ctx, e.conn, "spalte-")) {
		t.Fatal("Summe sollte als Nutzer von A gemeldet werden")
	}
	if got, _ := e.s.UsersOfSourcePrefix(ctx, e.conn, "anders-"); len(got) != 0 {
		t.Errorf("anderer Anfang: %v", got)
	}
	// Zwei nutzt Summe
	two := e.save(t, SaveInput{Name: "Anteil " + e.sfx, InfraID: e.assetA, Formula: "S / 2", Inputs: []InputRef{{Short: "S", MappingID: res.ID}}})
	var iu *InUseError
	if err := e.s.Delete(ctx, res.ID); !errors.As(err, &iu) || len(iu.Users) != 1 || iu.Users[0] != "Anteil "+e.sfx {
		t.Fatalf("genutzten Wert löschen: %v", err)
	}
	u, err := e.s.Usage(ctx, res.ID)
	if err != nil || len(u.Values) != 1 || u.Values[0] != "Anteil "+e.sfx {
		t.Errorf("Usage: %+v %v", u, err)
	}
	// neue geltende Fassung ohne A: A hat keinen Nutzer mehr, die abgelöste Fassung behält die Bezeichnung
	e.now = e.now.Add(time.Minute)
	in.Formula, in.Inputs = "B * 2", []InputRef{{Short: "B", MappingID: b}}
	e.save(t, in)
	if got, _ := e.s.UsersOfMapping(ctx, a); len(got) != 0 {
		t.Fatalf("A hat noch Nutzer: %v", got)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM import_mappings WHERE id = $1::uuid`, a); err != nil {
		t.Fatal(err)
	}
	v := e.get(t, res.ID)
	old := v.Versions[len(v.Versions)-1]
	if old.Status != StatusSuperseded || len(old.Inputs) != 2 || !old.Inputs[0].Gone || old.Inputs[0].MappingID != "" || old.Inputs[0].Label != "Presse A "+e.sfx+" › A "+e.sfx {
		t.Errorf("abgelöste Fassung: %+v", old)
	}
	if err := e.s.Delete(ctx, two.ID); err != nil {
		t.Fatalf("Anteil löschen: %v", err)
	}
	if err := e.s.Delete(ctx, res.ID); err != nil {
		t.Fatalf("Summe löschen: %v", err)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM calc_versions WHERE mapping_id = ANY($1::uuid[])`, []string{res.ID, two.ID}).Scan(&n)
	if n != 0 || e.values(t) != 0 {
		t.Errorf("Reste nach dem Löschen: %d Fassungen, %d Werte", n, e.values(t))
	}
	if err := e.s.Delete(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("gewöhnliche Zuordnung über Delete: %v", err)
	}
}

func TestUsage(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	_, _, _, res := e.sum(t)
	exp := e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('export', 'csv', $1, '{}'::jsonb) RETURNING id::text`, "Ablage "+e.sfx)
	if _, err := e.pool.Exec(ctx, `INSERT INTO export_mappings (connection_id, import_mapping_id, field_name) VALUES ($1::uuid, $2::uuid, 'Summe_kW')`, exp, res.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, import_mapping_id, kind) VALUES ($1, $2, $3::uuid, $4::uuid, 'instant')`,
		"CV-"+e.sfx, "Zähler "+e.sfx, e.assetA, res.ID); err != nil {
		t.Fatal(err)
	}
	u, err := e.s.Usage(ctx, res.ID)
	if err != nil || len(u.Values) != 0 || len(u.ExportFields) != 1 || u.ExportFields[0] != "Ablage "+e.sfx+" › Summe_kW" || len(u.Meters) != 1 || u.Meters[0] != "Zähler "+e.sfx {
		t.Errorf("Usage: %+v %v", u, err)
	}
}

func TestCandidates(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a, _, _, res := e.sum(t)
	e.src(t, e.assetB, "Drehzahl", "1500", e.now)
	// Wörter aus Anlagenpfad und Verbindung, Groß-/Kleinschreibung egal; der Wert selbst fehlt
	got, err := e.s.Candidates(ctx, "PRESSE quelle "+e.sfx, 50, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawA, sawSelf, sawB bool
	for _, c := range got {
		sawA = sawA || (c.ID == a && c.Value == "12,4" && c.InfraPath == "Presse A "+e.sfx && c.Connection == "Quelle "+e.sfx && c.ReceivedAt != nil && !c.Calc)
		sawSelf = sawSelf || c.ID == res.ID
		sawB = sawB || strings.HasPrefix(c.Name, "Drehzahl")
	}
	if !sawA || sawSelf || !sawB || len(got) != 3 {
		t.Errorf("Auswahl: A %v, selbst %v, andere Anlage %v (%d Treffer)", sawA, sawSelf, sawB, len(got))
	}
	// ein Wort aus der Bezeichnung grenzt ein
	if got, _ := e.s.Candidates(ctx, "drehzahl "+e.sfx, 50, res.ID); len(got) != 1 || !strings.HasPrefix(got[0].Name, "Drehzahl") {
		t.Errorf("Suche nach der Bezeichnung: %+v", got)
	}
	// ohne Ausschluss erscheint der berechnete Wert, als solcher gekennzeichnet
	got, _ = e.s.Candidates(ctx, "summe "+e.sfx, 50, "")
	if len(got) != 1 || !got[0].Calc || got[0].ID != res.ID {
		t.Errorf("berechneter Wert in der Auswahl: %+v", got)
	}
	if got, _ := e.s.Candidates(ctx, e.sfx, 2, ""); len(got) != 2 {
		t.Errorf("Grenze: %d", len(got))
	}
}

func TestFormulas(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a, _, _, res := e.sum(t)
	later := e.save(t, SaveInput{Name: "Später " + e.sfx, InfraID: e.assetA, Formula: "A * 2", ValidFrom: day(30),
		Inputs: []InputRef{{Short: "A", MappingID: a}}})
	f, err := e.s.Formulas(ctx)
	if err != nil || f[res.ID] != "A + b" || f[later.ID] != "A * 2" {
		t.Errorf("Formeln: %q %q %v", f[res.ID], f[later.ID], err)
	}
}
