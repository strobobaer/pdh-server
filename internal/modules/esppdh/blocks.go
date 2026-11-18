package esppdh

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Bausteine: Was der Anwender an ein Gerät anschließt. Er trägt nur ein, was er wissen kann
// (Pin, Zangentyp, Impulse je kWh); die ESPHome-Konfiguration entsteht daraus in yaml.go.

// Arten der Bausteine in Paket 1.
const (
	KindS0      = "s0"
	KindSCT013  = "sct013"
	KindPZEM    = "pzem"
	KindCounter = "counter"
)

// Block ist ein Baustein eines Geräts; Params enthält die Felder seiner BlockDef (leer = Vorgabe).
//
// Nr ist die feste Nummer des Bausteins je Art (ab 1, je Gerät und Art eindeutig). Sie bestimmt
// Thema und Kennungen (esppdh/<kennung>/<art>-<Nr>/…). Der Aufrufer vergibt sie einmal beim
// Anlegen (NextBlockNr) und ändert sie nie – auch nicht, wenn andere Bausteine gelöscht oder
// verschoben werden. Sonst wanderten Werte nach dem nächsten Bau auf ein anderes Thema.
type Block struct {
	Kind   string
	Nr     int
	Params map[string]string
}

// NextBlockNr: Nummer für einen neuen Baustein der Art kind – höchste vorhandene Nummer + 1.
func NextBlockNr(blocks []Block, kind string) int {
	n := 0
	for _, b := range blocks {
		if b.Kind == kind && b.Nr > n {
			n = b.Nr
		}
	}
	return n + 1
}

// BlockDef beschreibt eine Art von Baustein für das Formular.
type BlockDef struct {
	Kind, Label, Help string
	Fields            []Field
}

// Field ist ein Eingabefeld eines Bausteins. Type: pin|number|select|text. Bei Pin-Feldern
// sagt Analog, ob nur Pins zur Wahl stehen, die analog messen (Board.PinsFor).
type Field struct {
	Key, Label, Type, Default, Help string
	Required                        bool
	Options                         []string
	Analog                          bool

	min, max float64 // Zahlenfeld: erlaubter Bereich
	integer  bool    // Zahlenfeld: nur ganze Zahlen (kein Tausenderpunkt-Raten)

	// Feld zählt nur, wenn Feld onlyIfKey den Wert onlyIfValue hat (davor stehen); sonst wird es
	// weder geprüft noch übernommen. Ist es dann leer, fehlt es.
	onlyIfKey, onlyIfValue string
}

// Zangentypen des SCT-013. Die Typen mit Spannungsausgang haben die Bürde eingebaut
// (A je V Effektivwert); beim Typ mit Stromausgang (Übersetzung 2000:1) rechnet sich der
// Faktor aus dem Bürdenwiderstand: I = U · 2000 / R.
const clampCurrentOut = "100A/50mA"

var clampAmpsPerVolt = map[string]float64{"5A/1V": 5, "30A/1V": 30, "50A/1V": 50}

const sct013Turns = 2000.0

// BlockDefs: alle Bausteine zur Auswahl, in fester Reihenfolge.
func BlockDefs() []BlockDef {
	return []BlockDef{
		{
			Kind: KindS0, Label: "S0-Zähler",
			Help: "Impulsausgang (S0) eines Stromzählers. Liefert Leistung (W) und Zählerstand (kWh) seit dem Start des Geräts.",
			Fields: []Field{
				{Key: "pin", Label: "Pin", Type: "pin", Required: true, Help: "Anschluss für S0+; S0− an GND."},
				{Key: "pulses", Label: "Impulse je kWh", Type: "number", Default: "1000", Required: true,
					Help: "Steht auf dem Zähler, z. B. 1000 imp/kWh.", min: 1, max: 100000, integer: true},
			},
		},
		{
			Kind: KindSCT013, Label: "Stromzange SCT-013",
			Help: "Stromzange um einen Leiter. Liefert Strom (A) und Scheinleistung (VA = Strom × Netzspannung).",
			Fields: []Field{
				{Key: "pin", Label: "Analog-Pin", Type: "pin", Required: true, Analog: true,
					Help: "Die Zange braucht eine Vorspannung auf halber Versorgung (Spannungsteiler mit Kondensator)."},
				{Key: "clamp", Label: "Zangentyp", Type: "select", Required: true,
					Options: []string{"5A/1V", "30A/1V", "50A/1V", clampCurrentOut}, Help: "Steht auf der Zange."},
				// Bemessung: Vorspannung 1,65 V, Analog-Pin misst bis etwa 3,1 V (ESP32, -S3; D1 mini
				// 3,2 V) → Spitze höchstens 1,45 V. Spitzenstrom der Zange I·√2/2000 → R ≤ 1,45·2000/(√2·I)
				// ≈ 2050/I, gerundet 2000/I (100 A → 20 Ω). ESP32-C3 misst nur bis etwa 2,5 V → ≈ 1200/I.
				// Größer schneidet hohe Ströme ab und treibt den Pin bei vollem Strom über seine Grenzen.
				{Key: "burden", Label: "Bürdenwiderstand (Ω)", Type: "number",
					Help: "Nur bei 100A/50mA. Höchstens 2000 ÷ größter Strom in A, z. B. 20 bei 100 A (ESP32-C3: 1200 ÷ Strom).", min: 1, max: 10000,
					onlyIfKey: "clamp", onlyIfValue: clampCurrentOut},
				{Key: "voltage", Label: "Netzspannung (V)", Type: "number", Default: "230", Required: true,
					Help: "Spannung zwischen Leiter und N, meist 230.", min: 1, max: 1000},
			},
		},
		{
			Kind: KindPZEM, Label: "PZEM-004T",
			Help: "Energiemesser PZEM-004T (Version 3) über seine serielle Schnittstelle. Liefert Spannung, Strom, Leistung und Energie.",
			Fields: []Field{
				{Key: "rx_pin", Label: "RX-Pin", Type: "pin", Required: true, Help: "An TX des PZEM."},
				{Key: "tx_pin", Label: "TX-Pin", Type: "pin", Required: true, Help: "An RX des PZEM."},
				{Key: "address", Label: "Adresse", Type: "number", Default: "1", Required: true,
					Help: "Mehrere PZEM an denselben Pins brauchen verschiedene Adressen.", min: 1, max: 247, integer: true},
			},
		},
		{
			Kind: KindCounter, Label: "Stückzähler",
			Help: "Zählt Impulse, z. B. einer Lichtschranke. Liefert die Stückzahl seit dem Start des Geräts.",
			Fields: []Field{
				{Key: "pin", Label: "Pin", Type: "pin", Required: true,
					Help: "Kontakt oder Sensorausgang zwischen Pin und GND (der Pin hat einen Pull-up)."},
				{Key: "debounce", Label: "Entprellzeit (ms)", Type: "number", Default: "50", Required: true,
					Help: "Kürzere Impulse und Prellen werden nicht gezählt.", min: 0, max: 10000, integer: true},
				// Der Pin hat einen Pull-up; Kontakt oder Sensor ziehen ihn beim Schalten auf GND.
				// Schalten = Pin fällt (fallend), Loslassen = Pin steigt (steigend).
				{Key: "edge", Label: "Flanke", Type: "select", Default: "fallend", Required: true,
					Options: []string{"steigend", "fallend"},
					Help:    "fallend: zählt, wenn Kontakt oder Sensor schaltet (Teil kommt an). steigend: zählt, wenn er wieder öffnet (Teil ist durch)."},
			},
		},
	}
}

func blockDef(kind string) (BlockDef, bool) {
	for _, d := range BlockDefs() {
		if d.Kind == kind {
			return d, true
		}
	}
	return BlockDef{}, false
}

// resolvedBlock: geprüfter Baustein mit eingesetzten Vorgaben, einheitlich geschriebenen Pins
// und bereinigten Zahlen.
type resolvedBlock struct {
	def BlockDef
	nr  int               // feste Nummer je Art (Block.Nr) – Thema <art>-<nr>
	p   map[string]string // Werte je Feld
	bus int               // PZEM: Kennung des seriellen Anschlusses = kleinste Nr der PZEM an diesen Pins
}

// ValidateBlocks prüft die Bausteine eines Geräts auf der Platine b. Rückgabe: Meldungen auf
// Deutsch, leer = in Ordnung. Bei BoardOther prüft sie Pins nur auf Schreibweise und
// Doppelbelegung, nicht gegen eine Liste.
func ValidateBlocks(b Board, blocks []Block) []string {
	_, msgs := resolveBlocks(b, blocks)
	return msgs
}

func resolveBlocks(b Board, blocks []Block) ([]resolvedBlock, []string) {
	if len(blocks) == 0 {
		return nil, []string{"Bitte mindestens einen Baustein wählen."}
	}
	type use struct {
		owner string // wer den Pin belegt; PZEM an denselben Pins teilen sich einen Anschluss
		block int
	}
	var (
		msgs  []string
		out   []resolvedBlock
		nrs   = map[string]int{} // "art#nummer" → Baustein
		pins  = map[string]use{}
		buses = map[string]int{} // "rx/tx" → kleinste Nummer der PZEM an diesen Pins
		addrs = map[string]int{} // "rx/tx#adresse" → Baustein
	)
	for i, blk := range blocks {
		no := i + 1
		def, ok := blockDef(blk.Kind)
		if !ok {
			msgs = append(msgs, fmt.Sprintf("Baustein %d: Art „%s“ gibt es nicht.", no, short(blk.Kind)))
			continue
		}
		prefix := fmt.Sprintf("Baustein %d (%s): ", no, def.Label)
		switch key := fmt.Sprintf("%s#%d", def.Kind, blk.Nr); {
		case blk.Nr < 1:
			msgs = append(msgs, prefix+"Die Nummer des Bausteins fehlt.")
		case nrs[key] > 0:
			msgs = append(msgs, prefix+fmt.Sprintf("Nummer %d ist schon vergeben (Baustein %d).", blk.Nr, nrs[key]))
		default:
			nrs[key] = no
		}
		p, fmsgs := resolveFields(b, def, blk.Params)
		r := resolvedBlock{def: def, nr: blk.Nr, p: p}
		for _, m := range fmsgs {
			msgs = append(msgs, prefix+m)
		}
		if len(fmsgs) > 0 {
			continue // Pins unklar: Belegung erst prüfen, wenn die Felder stimmen
		}
		owner := fmt.Sprintf("baustein-%d", no)
		if def.Kind == KindPZEM {
			if p["rx_pin"] == p["tx_pin"] {
				msgs = append(msgs, prefix+"RX und TX dürfen nicht derselbe Pin sein.")
				continue
			}
			key := p["rx_pin"] + "/" + p["tx_pin"]
			owner = "pzem-" + key
			if cur, ok := buses[key]; !ok || blk.Nr < cur {
				buses[key] = blk.Nr
			}
			if first, ok := addrs[key+"#"+p["address"]]; ok {
				msgs = append(msgs, prefix+fmt.Sprintf("Adresse %s ist an denselben Pins schon vergeben (Baustein %d).", p["address"], first))
			} else {
				addrs[key+"#"+p["address"]] = no
			}
		}
		for _, f := range def.Fields {
			if f.Type != "pin" {
				continue
			}
			pin := p[f.Key]
			if u, ok := pins[pin]; ok {
				if u.owner != owner {
					msgs = append(msgs, fmt.Sprintf("%s ist mehrfach belegt (Baustein %d und %d).", pin, u.block, no))
				}
				continue
			}
			pins[pin] = use{owner: owner, block: no}
		}
		out = append(out, r)
	}
	// Kennung des Anschlusses aus der kleinsten Nummer: unabhängig von der Reihenfolge
	for i := range out {
		if out[i].def.Kind == KindPZEM {
			out[i].bus = buses[out[i].p["rx_pin"]+"/"+out[i].p["tx_pin"]]
		}
	}
	if b.maxUARTs > 0 && len(buses) > b.maxUARTs {
		msgs = append(msgs, fmt.Sprintf("Diese Platine hat nur %d serielle Anschlüsse für PZEM-004T. "+
			"Mehrere PZEM können sich einen Anschluss teilen: gleiche RX- und TX-Pins, verschiedene Adressen.", b.maxUARTs))
	}
	return out, msgs
}

// resolveFields prüft die Felder eines Bausteins; Meldungen ohne Bausteinnummer.
func resolveFields(b Board, def BlockDef, params map[string]string) (map[string]string, []string) {
	p := map[string]string{}
	var msgs []string
	for _, f := range def.Fields {
		if f.onlyIfKey != "" && p[f.onlyIfKey] != f.onlyIfValue {
			continue // zählt bei dieser Auswahl nicht: weder prüfen noch übernehmen
		}
		v := strings.TrimSpace(params[f.Key])
		if v == "" {
			v = f.Default
		}
		if v == "" {
			switch {
			case f.onlyIfKey != "":
				msgs = append(msgs, fmt.Sprintf("„%s“ fehlt (nötig bei %s).", f.Label, f.onlyIfValue))
			case f.Required:
				msgs = append(msgs, fmt.Sprintf("„%s“ fehlt.", f.Label))
			}
			continue
		}
		switch f.Type {
		case "pin":
			pin, ok := normPin(v)
			if !ok {
				msgs = append(msgs, fmt.Sprintf("„%s“ ist ungültig.", f.Label))
				continue
			}
			if allowed := b.PinsFor(f); b.Key != BoardOther && !slices.Contains(allowed, pin) {
				if f.Analog {
					msgs = append(msgs, fmt.Sprintf("%s kann auf dieser Platine nicht analog messen. Möglich: %s.", pin, strings.Join(allowed, ", ")))
				} else {
					msgs = append(msgs, fmt.Sprintf("%s ist auf dieser Platine nicht nutzbar. Möglich: %s.", pin, strings.Join(allowed, ", ")))
				}
				continue
			}
			p[f.Key] = pin
		case "number":
			if !f.integer && ambiguousDecimal(v) {
				msgs = append(msgs, fmt.Sprintf("„%s“: „%s“ ist nicht eindeutig – bitte ohne Tausenderpunkt, Dezimalstellen mit Komma (z. B. 1000 oder 22,5).", f.Label, short(v)))
				continue
			}
			n, ok := parseNumber(v, f.integer)
			if !ok || n < f.min || n > f.max {
				what := "eine Zahl"
				if f.integer {
					what = "eine ganze Zahl"
				}
				msgs = append(msgs, fmt.Sprintf("„%s“ muss %s von %s bis %s sein.", f.Label, what, fnum(f.min), fnum(f.max)))
				continue
			}
			p[f.Key] = fnum(n)
		case "select":
			if !slices.Contains(f.Options, v) {
				msgs = append(msgs, fmt.Sprintf("„%s“: „%s“ gibt es nicht. Möglich: %s.", f.Label, short(v), strings.Join(f.Options, ", ")))
				continue
			}
			p[f.Key] = v
		default:
			p[f.Key] = v
		}
	}
	return p, msgs
}

var (
	pinDigits   = regexp.MustCompile(`^[0-9]{1,3}$`)
	pinName     = regexp.MustCompile(`^([A-Z]{1,8})([0-9]{1,3})$`)
	intRe       = regexp.MustCompile(`^[0-9]{1,9}$`)
	decRe       = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,6})?$`)
	thousandsRe = regexp.MustCompile(`\.[0-9]{3}($|[.,])`)
)

// ambiguousDecimal: Dezimalzahl, bei der ein Punkt ein deutscher Tausenderpunkt sein kann –
// beide Trennzeichen („1.000,5“) oder ein Punkt mit genau drei Ziffern danach („1.000“ wäre
// sonst 1). Ein Komma ist immer das Dezimalzeichen („22,5“), ein Punkt sonst auch („22.5“).
func ambiguousDecimal(v string) bool {
	return strings.Contains(v, ",") && strings.Contains(v, ".") || thousandsRe.MatchString(v)
}

// normPin schreibt einen Pin einheitlich: „gpio04“ und „4“ werden zu „GPIO4“, „d5“ zu „D5“.
// Andere Zeichen als Buchstaben und Ziffern sind ungültig.
func normPin(v string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(v))
	if pinDigits.MatchString(s) {
		n, _ := strconv.Atoi(s)
		return "GPIO" + strconv.Itoa(n), true
	}
	m := pinName.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	n, _ := strconv.Atoi(m[2])
	return m[1] + strconv.Itoa(n), true
}

// parseNumber liest eine Zahl; Dezimalzahlen dürfen ein Komma haben. Ganze Zahlen nur aus
// Ziffern – „1.000“ ist dort ein Fehler, keine 1.
func parseNumber(v string, integer bool) (float64, bool) {
	if integer {
		if !intRe.MatchString(v) {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		return float64(n), err == nil
	}
	v = strings.Replace(v, ",", ".", 1)
	if !decRe.MatchString(v) {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// fnum schreibt eine Zahl ohne Exponent und ohne Rundungsrauschen (zehn gültige Stellen):
// 30·3,2 → „96“, 1/30000 → „0.00003333333333“.
func fnum(x float64) string {
	if x == 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return "0"
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 10, 64), 64)
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// short kürzt eine Eingabe für eine Meldung (ungültiges UTF-8 wird zu „�“).
func short(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
