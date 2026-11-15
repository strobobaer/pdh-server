package tiamqtt

import (
	"archive/zip"
	"bytes"
	"flag"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "Golden-Dateien neu schreiben")

func TestSCLStringEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"abc", "'abc'"},
		{"it's", "'it$'s'"},
		{"$", "'$$'"},
		{"ä", "'$C3$A4'"},
		{"a\nb", "'a$0Ab'"},
		{"\t", "'$09'"},
		{"\x7F", "'$7F'"},
		{`"x"`, `'"x"'`},
		{"", "''"},
	} {
		got, err := SCLString(tc.in, 254)
		if err != nil || got != tc.want {
			t.Errorf("SCLString(%q) = %q, %v; erwartet %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := SCLString(strings.Repeat("x", 254), 254); err != nil {
		t.Errorf("254 Byte: %v", err)
	}
	if _, err := SCLString(strings.Repeat("x", 255), 254); err == nil {
		t.Error("255 Byte bei max 254: kein Fehler")
	}
	// ä zählt als zwei Byte
	if _, err := SCLString(strings.Repeat("ä", 128), 254); err == nil {
		t.Error("256 Byte (Umlaute) bei max 254: kein Fehler")
	}
}

func TestSCLName(t *testing.T) {
	for in, want := range map[string]string{
		"PDH_MQTT_Daten":  `"PDH_MQTT_Daten"`,
		"PDH_MQTT_Zähler": `"PDH_MQTT_Z_hler"`,
		`a"b c`:           `"a_b_c"`,
		"x'; END":         `"x___END"`,
		"":                `"_"`,
	} {
		if got := SCLName(in); got != want {
			t.Errorf("SCLName(%q) = %s, erwartet %s", in, got, want)
		}
	}
}

func goldenOptions() Options {
	return Options{
		Variant: VariantStandalone, Family: FamilyS71500,
		BrokerHost: "192.168.0.10", BrokerPort: 1883,
		ClientID: "pdh-sps-Daten", Username: "sps", Password: "geheim",
		Topic: "sps/linie1/Daten", HeartbeatSeconds: 60, KeepAliveSeconds: 60,
		HwID: 64, ConnID: 1,
		GeneratedBy: "PDH-Test", GeneratedAt: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
	}
}

// datenPlus ist Daten.db mit einer langen Zeichenkette und einer Struktur
// mit Umlauten, damit bei 1024 Byte zwei Teile entstehen.
func datenPlus(t *testing.T) *DBSource {
	t.Helper()
	db := loadDaten(t)
	db.Vars = append(db.Vars,
		&Var{Name: "Bemerkung", Kind: KindString, StrLen: 254, Line: 40},
		&Var{Name: "Zähler", Kind: KindStruct, Line: 41, Members: []*Var{
			{Name: "Stück", Kind: KindUDInt, Line: 42, Init: "0"},
			{Name: "it's", Kind: KindBool, Line: 43},
		}},
	)
	return db
}

func generateDaten(t *testing.T, o Options) []File {
	t.Helper()
	db := datenPlus(t)
	l, err := Plan(db, o.Family, MinLimit)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Generate(db, l, o)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return files
}

func fileByName(t *testing.T, files []File, name string) string {
	t.Helper()
	for _, f := range files {
		if f.Name == name {
			return string(f.Content)
		}
	}
	t.Fatalf("Datei %s fehlt", name)
	return ""
}

func TestGenerateStandaloneGolden(t *testing.T) {
	files := generateDaten(t, goldenOptions())
	dir := filepath.Join("testdata", "golden", "standalone")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if *update {
			if err := os.WriteFile(path, f.Content, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (mit -update neu schreiben)", err)
		}
		if !bytes.Equal(f.Content, want) {
			t.Errorf("%s weicht von der Golden-Datei ab (mit -update neu schreiben und Diff prüfen)", f.Name)
		}
	}
	// keine veralteten Golden-Dateien
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(files) {
		t.Errorf("%d Golden-Dateien, %d erzeugt", len(entries), len(files))
	}
}

func TestZipContents(t *testing.T) {
	files := generateDaten(t, goldenOptions())
	want := []string{"1_PDH_Daten_Typ.udt", "2_PDH_MQTT_Daten.scl", "3_PDH_Daten_Konfig.db", "4_Daten_aus_Typ.db", "LIESMICH.txt"}
	b, err := Zip(files)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(want) {
		t.Fatalf("%d Dateien im ZIP", len(zr.File))
	}
	for i, zf := range zr.File {
		if zf.Name != want[i] {
			t.Errorf("Datei %d: %s, erwartet %s", i+1, zf.Name, want[i])
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		c, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.HasPrefix(c, []byte{0xEF, 0xBB, 0xBF}) {
			t.Errorf("%s: kein BOM", zf.Name)
		}
		if bytes.Count(c, []byte("\n")) != bytes.Count(c, []byte("\r\n")) || bytes.Contains(c, []byte("\r\r")) {
			t.Errorf("%s: Zeilenenden nicht durchgehend CRLF", zf.Name)
		}
		if !bytes.Equal(c, files[i].Content) {
			t.Errorf("%s: Inhalt im ZIP anders", zf.Name)
		}
	}
	if got := ZipName("Daten", VariantStandalone); got != "PDH_MQTT_Daten_eigenstaendig.zip" {
		t.Errorf("ZipName = %s", got)
	}
	if got := ZipName("Daten", VariantLMQTT); got != "PDH_MQTT_Daten_lmqtt.zip" {
		t.Errorf("ZipName = %s", got)
	}
	if got := ZipName("Zähler 1", VariantStandalone); got != "PDH_MQTT_Z_hler_1_eigenstaendig.zip" {
		t.Errorf("ZipName = %s", got)
	}
}

// sclLines liefert die Zeilen ohne Leerraum am Rand.
func sclLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\r\n") {
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

func countLines(s, line string) int {
	n := 0
	for _, l := range sclLines(s) {
		if l == line {
			n++
		}
	}
	return n
}

func TestGenerateNoRawUserText(t *testing.T) {
	o := goldenOptions()
	o.Topic = "a'; END_FUNCTION_BLOCK"
	o.Password = "x'y$z"
	o.IncludePassword = true
	o.ClientID = `sps"1`
	o.Username = "Bediener ä"
	o.GeneratedBy = "Max\r\nEND_FUNCTION_BLOCK"
	files := generateDaten(t, o)
	all := ""
	for _, f := range files {
		if f.Name == "LIESMICH.txt" {
			continue
		}
		all += string(f.Content)
		for _, raw := range []string{"a';", "x'y", "y$z", "Bediener ä"} {
			if strings.Contains(string(f.Content), raw) {
				t.Errorf("%s enthält %q unmaskiert", f.Name, raw)
			}
		}
	}
	for _, masked := range []string{"'a$'; END_FUNCTION_BLOCK'", "'x$'y$$z'", `'sps"1'`, "'Bediener $C3$A4'", "'a$'; END_FUNCTION_BLOCK/heartbeat'"} {
		if !strings.Contains(all, masked) {
			t.Errorf("%s fehlt", masked)
		}
	}
	scl := fileByName(t, files, "2_PDH_MQTT_Daten.scl")
	if n := countLines(scl, "END_FUNCTION_BLOCK"); n != 1 {
		t.Errorf("%d Zeilen END_FUNCTION_BLOCK, erwartet 1", n)
	}
	if n := countLines(scl, "END_FUNCTION"); n != 8 {
		t.Errorf("%d Zeilen END_FUNCTION, erwartet 8", n)
	}
	for _, f := range files {
		for _, l := range sclLines(string(f.Content)) {
			if l == "END_FUNCTION_BLOCK" && f.Name != "2_PDH_MQTT_Daten.scl" {
				t.Errorf("%s: END_FUNCTION_BLOCK als eigene Zeile", f.Name)
			}
		}
	}
}

func TestGeneratePasswordPlaceholder(t *testing.T) {
	o := goldenOptions()
	o.Password = "Sehr-Geheim-42"
	b, err := Zip(generateDaten(t, o))
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	found := false
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		c, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(c, []byte("Sehr-Geheim")) {
			t.Errorf("%s enthält das Kennwort", zf.Name)
		}
		if bytes.Contains(c, []byte("'"+PasswordPlaceholder+"'")) {
			found = true
		}
	}
	if !found {
		t.Error("Platzhalter fehlt")
	}
	o.IncludePassword = true
	db := fileByName(t, generateDaten(t, o), "3_PDH_Daten_Konfig.db")
	if !strings.Contains(db, "'Sehr-Geheim-42'") {
		t.Error("Kennwort fehlt trotz IncludePassword")
	}
}

func TestStandalonePacketConstants(t *testing.T) {
	scl := fileByName(t, generateDaten(t, goldenOptions()), "2_PDH_MQTT_Daten.scl")
	for _, want := range []string{
		":= 16#10;", "'MQTT'", "16#04", "16#02", "OR 16#80", "OR 16#40",
		":= 16#30;", "OR 16#01", "16#C0", "16#E0", "MOD 128", "TCON_IP_v4",
		"ConnectionType := 11", "ActiveEstablished := TRUE", "ADHOC := TRUE",
		"RD_SYS_T(", "R_TRIG", "TON", "#snap := #daten;", "UPPER_BOUND(",
	} {
		if !strings.Contains(scl, want) {
			t.Errorf("SCL enthält %q nicht", want)
		}
	}
	for _, fc := range []string{"PDH_J_Raw", "PDH_J_DInt", "PDH_J_UDInt", "PDH_J_Real", "PDH_J_LReal", "PDH_J_Str", "PDH_J_DTL"} {
		if !strings.Contains(scl, `FUNCTION "`+fc+`" : Void`) {
			t.Errorf("Hilfsbaustein %s fehlt", fc)
		}
	}
	// Reihenfolge: Hilfsbausteine vor dem FB, aufgerufene vor aufrufenden
	order := []string{`FUNCTION "PDH_J_Raw"`, `FUNCTION "PDH_J_Num"`, `FUNCTION "PDH_J_Real"`, `FUNCTION "PDH_J_DTL"`, `FUNCTION_BLOCK "PDH_MQTT_Daten"`}
	last := -1
	for _, s := range order {
		i := strings.Index(scl, s)
		if i < last {
			t.Errorf("%s steht zu früh", s)
		}
		last = i
	}
}

func TestStandaloneHeartbeatNoRetain(t *testing.T) {
	scl := fileByName(t, generateDaten(t, goldenOptions()), "2_PDH_MQTT_Daten.scl")
	m := regexp.MustCompile(`(?s)// --- Heartbeat ---(.*?)// --- Ende Heartbeat ---`).FindStringSubmatch(scl)
	if m == nil {
		t.Fatal("Heartbeat-Abschnitt nicht gefunden")
	}
	hb := m[1]
	if !strings.Contains(hb, "#pubHdr := 16#30;") {
		t.Error("Heartbeat setzt den PUBLISH-Kopf nicht fest auf 16#30")
	}
	if strings.Contains(strings.ToLower(hb), "retain") || strings.Contains(hb, "16#01") || strings.Contains(hb, "16#31") {
		t.Errorf("Heartbeat-Abschnitt hat einen Retain-Zweig:\n%s", hb)
	}
	if !strings.Contains(hb, ".heartbeatTopic") {
		t.Error("Heartbeat nutzt nicht heartbeatTopic")
	}
	// Retain nur im Teile-Abschnitt
	if n := strings.Count(scl, ".retain"); n != 1 {
		t.Errorf("%d Stellen mit .retain, erwartet 1", n)
	}
}

func TestGenerateRejects(t *testing.T) {
	db := loadDaten(t)
	l, err := Plan(db, FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mod  func(*Options)
		want string
	}{
		"TLS":       {func(o *Options) { o.UseTLS = true }, "Die eigenständige Variante kann kein TLS – bitte LMQTT wählen"},
		"Hostname":  {func(o *Options) { o.BrokerHost = "broker.local" }, "Die eigenständige Variante braucht die IPv4-Adresse des Brokers"},
		"IPv6":      {func(o *Options) { o.BrokerHost = "::1" }, "Die eigenständige Variante braucht die IPv4-Adresse des Brokers"},
		"Topic":     {func(o *Options) { o.Topic = "" }, "Topic"},
		"Lang":      {func(o *Options) { o.Topic = strings.Repeat("t", 245) }, "Topic"},
		"Port":      {func(o *Options) { o.BrokerPort = 70000 }, "Port"},
		"Heartbeat": {func(o *Options) { o.HeartbeatSeconds = 3601 }, "Heartbeat"},
		"KeepAlive": {func(o *Options) { o.KeepAliveSeconds = 5 }, "Keep-Alive"},
		"Variante":  {func(o *Options) { o.Variant = "x" }, "Variante"},
	} {
		o := goldenOptions()
		tc.mod(&o)
		_, err := Generate(db, l, o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, erwartet %q", name, err, tc.want)
		}
	}
}

func TestGenerateDefaults(t *testing.T) {
	o := goldenOptions()
	o.BrokerPort, o.KeepAliveSeconds, o.HwID, o.ConnID = 0, 0, 0, 0
	files := generateDaten(t, o)
	db := fileByName(t, files, "3_PDH_Daten_Konfig.db")
	for _, want := range []string{"brokerPort : UInt := 1883;", "keepAlive : UInt := 60;", "retain : Bool := FALSE;", "heartbeatTopic : String[254] := 'sps/linie1/Daten/heartbeat';"} {
		if !strings.Contains(db, want) {
			t.Errorf("Konfig-DB ohne %q", want)
		}
	}
	scl := fileByName(t, files, "2_PDH_MQTT_Daten.scl")
	for _, want := range []string{"hwId : HW_ANY := 64;", "connId : CONN_OUC := 1;", "heartbeatInterval : Time := T#60s;"} {
		if !strings.Contains(scl, want) {
			t.Errorf("FB ohne %q", want)
		}
	}
	o.HeartbeatSeconds = 0
	if scl := fileByName(t, generateDaten(t, o), "2_PDH_MQTT_Daten.scl"); !strings.Contains(scl, "heartbeatInterval : Time := T#0s;") {
		t.Error("Heartbeat 0 nicht als T#0s")
	}
}

func TestGenerateStartValues(t *testing.T) {
	src := "DATA_BLOCK \"Werte\"\r\n" +
		"   VAR\r\n" +
		"      A : Int := 5;\r\n" +
		"      B : Int := \"Konstante\";\r\n" +
		"      C : String[10] := 'x; END_TYPE';\r\n" +
		"      D : Char;\r\n" +
		"      \"Zähler\" : Real := 1.5;\r\n" +
		"      F : Array[0..2] of Int := [1, 2(0)];\r\n" +
		"   END_VAR\r\n" +
		"BEGIN\r\n" +
		"   A := 7;\r\n" +
		"   D := 'x';\r\n" +
		"   \"Zähler\" := 2.5;\r\n" +
		"   F[1] := 3;\r\n" +
		"   A := \"Andere\";\r\n" +
		"END_DATA_BLOCK\r\n"
	db, err := ParseDBSource([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Plan(db, FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	o := goldenOptions()
	files, err := Generate(db, l, o)
	if err != nil {
		t.Fatal(err)
	}
	udt := fileByName(t, files, "1_PDH_Werte_Typ.udt")
	for _, want := range []string{`"A" : Int := 5;`, `"B" : Int;`, `"C" : String[10] := 'x; END_TYPE';`, `"Zähler" : Real := 1.5;`, `"F" : Array[0..2] of Int := [1, 2(0)];`} {
		if !strings.Contains(udt, want) {
			t.Errorf("Datentyp ohne %q:\n%s", want, udt)
		}
	}
	if strings.Contains(udt, `"D"`) {
		t.Error("übersprungene Variable D im Datentyp")
	}
	ersatz := fileByName(t, files, "4_Werte_aus_Typ.db")
	for _, want := range []string{`"A" := 7;`, `"Zähler" := 2.5;`, `"F"[1] := 3;`, `DATA_BLOCK "Werte"`, `"PDH_Werte_Typ"`} {
		if !strings.Contains(ersatz, want) {
			t.Errorf("Ersatz-DB ohne %q:\n%s", want, ersatz)
		}
	}
	for _, bad := range []string{`D :=`, `"Andere"`} {
		if strings.Contains(ersatz, bad) {
			t.Errorf("Ersatz-DB enthält %q", bad)
		}
	}
	readme := fileByName(t, files, "LIESMICH.txt")
	for _, want := range []string{"Startwert von B", "D := 'x'", `A := "Andere"`, "Zeile 6: D – Typ Char wird nicht unterstützt"} {
		if !strings.Contains(readme, want) {
			t.Errorf("LIESMICH nennt %q nicht", want)
		}
	}
}

func TestGenerateReadme(t *testing.T) {
	readme := fileByName(t, generateDaten(t, goldenOptions()), "LIESMICH.txt")
	for _, want := range []string{
		"16#7000", "16#7003", "16#8001", "16#8005", "16#8006", "16#8007", "16#8008", "16#8009", "16#81xx",
		"values.Motor.Drehzahl", "values.Feld[0]", "Teil 1", "Teil 2",
		"Retain", "sps/linie1/Daten/heartbeat", PasswordPlaceholder,
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("LIESMICH ohne %q", want)
		}
	}
}

// ---------------------------------------------------------------- Zahlen

// sclNum bildet den Ablauf von "PDH_J_Num" in Go nach (nur LReal-Rechnung
// ohne fused multiply-add) und zeigt, dass er wie strconv.FormatFloat
// rundet.
func sclNum(x float64, digits int, maxAbs float64) string {
	if !(math.Abs(x) <= maxAbs) {
		return "null"
	}
	neg := x < 0
	a := math.Abs(x)
	top := 1.0
	for i := 0; i < digits; i++ {
		top *= 10
	}
	const magic = 4503599627370496.0
	var n float64
	e := 0
	if a != 0 {
		t := a
		// dieselben Konstanten wie im SCL-Code
		steps := []int{256, 128, 64, 32, 16, 8, 4, 2, 1}
		pows := []float64{1e256, 1e128, 1e64, 1e32, 1e16, 1e8, 1e4, 1e2, 1e1}
		lows := []float64{1e-255, 1e-127, 1e-63, 1e-31, 1e-15, 1e-7, 1e-3, 1e-1, 1}
		if t >= 1 {
			for i, s := range steps {
				if t >= pows[i] {
					t /= pows[i]
					e += s
				}
			}
		} else {
			for i, s := range steps {
				if t < lows[i] {
					t *= pows[i]
					e -= s
				}
			}
		}
		for pass := 0; pass < 3; pass++ {
			k := digits - 1 - e
			s, j := a, k
			if j < 0 {
				j = -j
			}
			if k > 300 {
				s, j = a*1e100, k-100
			}
			p := 1.0
			for i, st := range steps {
				if j >= st {
					p = float64(p * pows[i])
					j -= st
				}
			}
			exact := k <= 22 && k >= -22
			var y, lo float64
			if k >= 0 {
				y = float64(s * p)
				if exact {
					lo = twoProdErr(s, p, y)
				}
			} else {
				y = s / p
				if exact {
					ph := float64(y * p)
					pl := twoProdErr(y, p, ph)
					r := float64(s-ph) - pl
					lo = r / p
				}
			}
			tt := float64(y + magic)
			n = float64(tt - magic)
			f := float64(y - n)
			switch {
			case f == 0.5 && lo > 0, f == 0.5 && lo == 0 && isOdd(n):
				n++
			case f == -0.5 && lo < 0, f == -0.5 && lo == 0 && isOdd(n):
				n--
			}
			if n >= top {
				e++
			} else if n < top/10 {
				e--
			} else {
				break
			}
		}
		if e == 308 && n > 179769313486231.0 {
			return "null"
		}
	}
	hi := int64(n / 1e8)
	lo := int64(n - float64(hi)*1e8)
	if lo < 0 {
		hi--
		lo += 1e8
	} else if lo >= 1e8 {
		hi++
		lo -= 1e8
	}
	dig := make([]byte, digits)
	for i := digits - 1; i >= 0; i-- {
		if digits-1-i < 8 {
			dig[i] = byte(lo%10) + '0'
			lo /= 10
		} else {
			dig[i] = byte(hi%10) + '0'
			hi /= 10
		}
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte(dig[0])
	b.WriteByte('.')
	b.Write(dig[1:])
	b.WriteByte('E')
	if e < 0 {
		b.WriteByte('-')
		e = -e
	} else {
		b.WriteByte('+')
	}
	if e < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.Itoa(e))
	return b.String()
}

func isOdd(n float64) bool {
	h := n / 2
	const magic = 4503599627370496.0
	t := float64(h + magic)
	return float64(t-magic) != h
}

// twoProdErr ist der Rundungsfehler von a·b (Dekker), p = a·b gerundet.
func twoProdErr(a, b, p float64) float64 {
	const c = 134217729.0
	t := float64(c * a)
	ah := float64(t - float64(t-a))
	al := float64(a - ah)
	t = float64(c * b)
	bh := float64(t - float64(t-b))
	bl := float64(b - bh)
	return float64(float64(float64(float64(ah*bh)-p)+float64(ah*bl))+float64(al*bh)) + float64(al*bl)
}

func TestSCLNumberMatchesRender(t *testing.T) {
	realOut := func(v float32) string { return string(appendValue(nil, Leaf{Kind: KindReal}, v)) }
	lrealOut := func(v float64) string { return string(appendValue(nil, Leaf{Kind: KindLReal}, v)) }
	const maxReal = 3.4028234663852886e38
	rng := rand.New(rand.NewSource(1))
	edgeR := []float32{0, 1, -1, 1450, 0.1, 1e-45, -1e-45, 1.17549435e-38, math.MaxFloat32, -math.MaxFloat32,
		16777215, 10000005, 9999999.5, 999999.94, 123456.75, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))}
	for i := 0; i < 200000; i++ {
		edgeR = append(edgeR, math.Float32frombits(rng.Uint32()))
	}
	misses := 0
	for _, v := range edgeR {
		got := sclNum(float64(v), 7, maxReal)
		want := realOut(v)
		if got != want {
			misses++
			if misses < 10 {
				t.Errorf("Real %g (%08X): SCL %s, Render %s", v, math.Float32bits(v), got, want)
			}
		}
		if len(got) > ValueWidth(KindReal, 0) {
			t.Errorf("Real %g: %s zu lang", v, got)
		}
	}
	if misses > 0 {
		t.Errorf("Real: %d Abweichungen", misses)
	}

	edgeL := []float64{0, 1, -1, 1.5e3, 0.1, 5e-324, -5e-324, math.MaxFloat64, -math.MaxFloat64, LRealMaxJSON, -LRealMaxJSON,
		1.797693134862315e308, 2.2250738585072014e-308, 1e22, 1e23, 123456789012345.5, 0.30000000000000004,
		math.NaN(), math.Inf(1), math.Inf(-1)}
	for i := 0; i < 200000; i++ {
		edgeL = append(edgeL, math.Float64frombits(rng.Uint64()))
		// Werte im Bereich mit exakter Rundung (10^-8 … 10^36)
		edgeL = append(edgeL, (rng.Float64()*2-1)*math.Pow10(rng.Intn(44)-8))
	}
	misses, exactMisses := 0, 0
	for _, v := range edgeL {
		got := sclNum(v, 15, LRealMaxJSON)
		want := lrealOut(v)
		if got != want {
			misses++
			a := math.Abs(v)
			if a >= 1e-8 && a < 1e36 {
				exactMisses++
				if exactMisses < 10 {
					t.Errorf("LReal %v: SCL %s, Render %s", v, got, want)
				}
			}
			// außerhalb höchstens die letzte Stelle anders, nie unlesbar
			if got != "null" {
				g, err1 := strconv.ParseFloat(got, 64)
				w, err2 := strconv.ParseFloat(want, 64)
				if err1 != nil || err2 != nil || math.Abs(g-w) > math.Abs(w)*2e-14 {
					t.Errorf("LReal %v: SCL %s, Render %s", v, got, want)
				}
			} else if want != "null" {
				t.Errorf("LReal %v: SCL null, Render %s", v, want)
			}
		}
		if len(got) > ValueWidth(KindLReal, 0) {
			t.Errorf("LReal %v: %s zu lang", v, got)
		}
	}
	if exactMisses > 0 {
		t.Errorf("LReal: %d Abweichungen im Bereich 1E-8 … 1E36", exactMisses)
	}
	t.Logf("LReal: %d von %d Werten außerhalb 1E-8 … 1E36 in der letzten Stelle anders", misses, len(edgeL))
}
