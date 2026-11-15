package tiamqtt

// Erzeugen der SCL-Quellen für das TIA Portal (ab V17, S7-1500/S7-1200,
// optimierter Zugriff) aus dem gelesenen Datenbaustein und seiner
// Aufteilung auf Nachrichten. Die Vorlagen liegen in templates/; alle
// Anwendertexte gehen nur über SCLString, SCLName und sclIdent hinein.

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// Variant ist die Bauart des Bausteins.
type Variant string

const (
	VariantStandalone Variant = "standalone" // eigenständig: TCON/TSEND/TRCV, ohne TLS
	VariantLMQTT      Variant = "lmqtt"      // mit Siemens-Bibliothek LMQTT, TLS möglich
)

// Options sind die Einstellungen für die erzeugten Bausteine.
type Options struct {
	Variant Variant
	Family  Family

	BrokerHost string
	BrokerPort int // 0 = 1883 (mit TLS 8883)
	UseTLS     bool

	ClientID, Username, Password string
	IncludePassword              bool // sonst steht PasswordPlaceholder in der Konfiguration

	Topic            string
	HeartbeatSeconds int // 0–3600, 0 = aus
	KeepAliveSeconds int // 10–600, 0 = 60
	Retain           bool
	HwID, ConnID     int // 0 = 64 bzw. 1

	GeneratedBy string    // im Dateikopf
	GeneratedAt time.Time // im Dateikopf; 0 = jetzt
}

// File ist eine erzeugte Datei.
type File struct {
	Name    string
	Content []byte
}

const (
	maxTopicBytes  = 244 // + "/heartbeat" = 254, passt in String[254]
	maxConfigBytes = 254
	rxSize         = 64
	txExtra        = 1 + 3 + 2 + maxConfigBytes + 1 // Kopf, Restlänge, Topic-Länge, Topic, Reserve

	msgNoTLS        = "Die eigenständige Variante kann kein TLS – bitte LMQTT wählen"
	msgNeedIPv4     = "Die eigenständige Variante braucht die IPv4-Adresse des Brokers"
	msgVariant      = "Unbekannte Variante – eigenständig oder LMQTT wählen"
	msgTopicEmpty   = "Bitte ein Topic angeben"
	msgTopicLong    = "Das Topic darf höchstens 244 Byte lang sein"
	msgTopicWild    = "Das Topic darf keine Platzhalter + oder # enthalten"
	msgPort         = "Der Port muss zwischen 1 und 65535 liegen"
	msgHeartbeat    = "Der Heartbeat muss zwischen 0 und 3600 Sekunden liegen"
	msgKeepAlive    = "Die Keep-Alive-Zeit muss zwischen 10 und 600 Sekunden liegen"
	msgHwID         = "Die Hardware-Kennung muss zwischen 1 und 65535 liegen"
	msgConnID       = "Die Verbindungsnummer muss zwischen 1 und 4095 liegen"
	msgLayout       = "Die Aufteilung passt nicht zum Datenbaustein"
	msgClientIDLong = "Client-ID ist zu lang (höchstens 254 Zeichen)"
)

// LMQTTInfoURL ist der Beitrag zur Siemens-Bibliothek LMQTT (vor dem Einbau
// im Siemens-Portal prüfen, Beitrag „LMQTT“).
const LMQTTInfoURL = "https://support.industry.siemens.com/cs/ww/de/view/109748872"

//go:embed templates/*.tmpl
var templateFS embed.FS

var variantLabels = map[Variant]string{
	VariantStandalone: "eigenständig (TCON/TSEND/TRCV, ohne TLS)",
	VariantLMQTT:      "mit Siemens-Bibliothek LMQTT (TLS möglich)",
}

var familyLabels = map[Family]string{FamilyS71500: "S7-1500", FamilyS71200: "S7-1200"}

// ZipName ist der Name der ZIP-Datei zum Herunterladen.
func ZipName(dbName string, v Variant) string {
	suffix := "lmqtt"
	if v == VariantStandalone {
		suffix = "eigenstaendig"
	}
	return "PDH_MQTT_" + plainName(dbName) + "_" + suffix + ".zip"
}

// Zip packt die Dateien in dieser Reihenfolge.
func Zip(files []File) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := time.Now()
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalize prüft die Einstellungen und setzt Vorgaben.
func (o Options) normalize() (Options, error) {
	if _, ok := variantLabels[o.Variant]; !ok {
		return o, errors.New(msgVariant)
	}
	if _, ok := familyLabels[o.Family]; !ok {
		return o, errors.New(msgBadFamily)
	}
	o.BrokerHost = strings.TrimSpace(o.BrokerHost)
	if o.Variant == VariantStandalone {
		if o.UseTLS {
			return o, errors.New(msgNoTLS)
		}
		if ip := net.ParseIP(o.BrokerHost); ip == nil || ip.To4() == nil || strings.Contains(o.BrokerHost, ":") {
			return o, errors.New(msgNeedIPv4)
		}
	}
	if o.BrokerHost == "" || len(o.BrokerHost) > maxConfigBytes {
		return o, errors.New("Bitte die Adresse des Brokers angeben (höchstens 254 Zeichen)")
	}
	if o.BrokerPort == 0 {
		o.BrokerPort = 1883
		if o.UseTLS {
			o.BrokerPort = 8883
		}
	}
	if o.BrokerPort < 1 || o.BrokerPort > 65535 {
		return o, errors.New(msgPort)
	}
	switch {
	case o.Topic == "":
		return o, errors.New(msgTopicEmpty)
	case len(o.Topic) > maxTopicBytes:
		return o, errors.New(msgTopicLong)
	case strings.ContainsAny(o.Topic, "+#"):
		return o, errors.New(msgTopicWild)
	}
	if o.HeartbeatSeconds < 0 || o.HeartbeatSeconds > 3600 {
		return o, errors.New(msgHeartbeat)
	}
	if o.KeepAliveSeconds == 0 {
		o.KeepAliveSeconds = 60
	}
	if o.KeepAliveSeconds < 10 || o.KeepAliveSeconds > 600 {
		return o, errors.New(msgKeepAlive)
	}
	if o.HwID == 0 {
		o.HwID = 64
	}
	if o.HwID < 1 || o.HwID > 65535 {
		return o, errors.New(msgHwID)
	}
	if o.ConnID == 0 {
		o.ConnID = 1
	}
	if o.ConnID < 1 || o.ConnID > 4095 {
		return o, errors.New(msgConnID)
	}
	if len(o.ClientID) > maxConfigBytes {
		return o, errors.New(msgClientIDLong)
	}
	for name, s := range map[string]string{"Benutzer": o.Username, "Kennwort": o.Password} {
		if len(s) > maxConfigBytes {
			return o, fmt.Errorf("%s ist länger als 254 Byte", name)
		}
	}
	if !o.IncludePassword {
		o.Password = PasswordPlaceholder
	}
	if o.GeneratedAt.IsZero() {
		o.GeneratedAt = time.Now()
	}
	return o, nil
}

// genData ist der Inhalt für die Vorlagen.
type genData struct {
	O              Options
	DB             string // Name des eigenen Datenbausteins (roh, nur in Kommentaren und LIESMICH)
	DBIdent        string // "Name" für den Ersatz-DB
	Base           string // Name in Dateinamen
	TypeName       string
	FBName         string
	KonfigName     string
	PufferName     string
	HeartbeatTopic string
	VariantLabel   string
	FamilyLabel    string
	At             string
	Limit          int
	Parts          int
	PLMax          int
	TXSize         int
	TXMax          int
	RXMax          int
	TypeBody       string
	BeginLines     []string
	PartsCode      string
	ReadmeParts    []readmePart
	Skipped        []Skipped
	Dropped        []string
	HasRetain      bool
	Placeholder    string
	InfoURL        string
}

type readmePart struct {
	No       int
	MaxBytes int
	Values   []readmeValue
}

type readmeValue struct {
	Path, JSONPath, Type string
}

// Generate erzeugt die Dateien in Import-Reihenfolge.
func Generate(db *DBSource, l *Layout, o Options) ([]File, error) {
	o, err := o.normalize()
	if err != nil {
		return nil, err
	}
	if db == nil || l == nil || len(l.Parts) == 0 {
		return nil, errors.New(msgLayout)
	}
	if o.ClientID == "" {
		o.ClientID = "pdh-sps-" + plainName(db.Name)
	}
	if len(o.ClientID) > maxConfigBytes {
		return nil, errors.New(msgClientIDLong)
	}
	base := plainName(db.Name)
	d := &genData{
		O:              o,
		DB:             db.Name,
		DBIdent:        sclIdent(db.Name),
		Base:           base,
		TypeName:       SCLName("PDH_" + base + "_Typ"),
		FBName:         SCLName("PDH_MQTT_" + base),
		KonfigName:     SCLName("PDH_" + base + "_Konfig"),
		PufferName:     SCLName("PDH_" + base + "_Puffer"),
		HeartbeatTopic: o.Topic + "/heartbeat",
		VariantLabel:   variantLabels[o.Variant],
		FamilyLabel:    familyLabels[o.Family],
		At:             o.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC"),
		Limit:          l.Limit,
		Parts:          len(l.Parts),
		PLMax:          l.Limit - 1,
		TXSize:         l.Limit + txExtra,
		RXMax:          rxSize - 1,
		Skipped:        l.Skipped,
		HasRetain:      db.HasRetain,
		Placeholder:    PasswordPlaceholder,
		InfoURL:        LMQTTInfoURL,
	}
	d.TXMax = d.TXSize - 1

	sent := map[string]bool{}
	for _, lf := range l.Leaves {
		segs, ok := sclPathSegments(lf.Path)
		if !ok {
			return nil, errors.New(msgLayout)
		}
		sent[pathKey(segs)] = true
	}
	tb := &typeBuilder{sent: sent}
	tb.vars(db.Vars, "", "      ")
	if len(tb.lines) == 0 {
		return nil, errors.New(msgLayout)
	}
	d.TypeBody = strings.Join(tb.lines, "\n") + "\n"
	d.Dropped = tb.dropped
	d.BeginLines, d.Dropped = beginLines(db.Begin, sent, d.Dropped)

	var pc strings.Builder
	for _, p := range l.Parts {
		code, err := partCode(p)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&pc, "\t%s%d:\n%s", strings.Repeat(" ", 16), p.No, code)
		rp := readmePart{No: p.No, MaxBytes: p.MaxBytes}
		for _, lf := range p.Leaves {
			rp.Values = append(rp.Values, readmeValue{Path: lf.Path, JSONPath: lf.JSONPath, Type: typeName(lf.Kind, lf.StrLen)})
		}
		d.ReadmeParts = append(d.ReadmeParts, rp)
	}
	d.PartsCode = pc.String()

	t, err := loadTemplates(o.Variant)
	if err != nil {
		return nil, err
	}
	exec := func(names ...string) ([]byte, error) {
		var b bytes.Buffer
		for _, n := range names {
			if err := t.ExecuteTemplate(&b, n, d); err != nil {
				return nil, err
			}
		}
		return toTIAText(b.Bytes()), nil
	}
	specs := []struct {
		name  string
		parts []string
	}{
		{"1_PDH_" + base + "_Typ.udt", []string{"udt"}},
		{"2_PDH_" + base + "_Konfig.db", []string{"konfig", "extra_db"}},
		{"3_PDH_MQTT_" + base + ".scl", []string{"fcs", "fb"}},
		{"4_" + base + "_aus_Typ.db", []string{"ersatz"}},
		{"LIESMICH.txt", []string{"liesmich"}},
	}
	var files []File
	for _, s := range specs {
		c, err := exec(s.parts...)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: s.name, Content: c})
	}
	return files, nil
}

func loadTemplates(v Variant) (*template.Template, error) {
	// cfgstr: Deklaration und Startwert eines Textes in der Konfiguration –
	// LMQTT erwartet WString, die eigenständige Variante sendet String-Bytes.
	cfgstr := func(s string, max int) (string, error) {
		if v == VariantLMQTT {
			lit, err := SCLWString(s, max)
			return "WString[254] := " + lit, err
		}
		lit, err := SCLString(s, max)
		return "String[254] := " + lit, err
	}
	funcs := template.FuncMap{
		"sclstr":  SCLString,
		"cfgstr":  cfgstr,
		"sclname": SCLName,
		"comment": sclComment,
		"text":    sclComment,
	}
	return template.New("").Funcs(funcs).ParseFS(templateFS, "templates/common.tmpl", "templates/"+string(v)+".tmpl")
}

// toTIAText macht UTF-8 mit BOM und CRLF daraus.
func toTIAText(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
	return append([]byte{0xEF, 0xBB, 0xBF}, b...)
}

// ---------------------------------------------------------------- Datentyp

type typeBuilder struct {
	sent    map[string]bool
	lines   []string
	dropped []string
}

func (tb *typeBuilder) has(path string) bool {
	segs, ok := sclPathSegments(path)
	return ok && tb.sent[pathKey(segs)]
}

// vars schreibt die gesendeten Variablen als Deklarationen.
func (tb *typeBuilder) vars(vs []*Var, prefix, indent string) {
	for _, v := range vs {
		path := sclPath(prefix, v.Name)
		var decl string
		switch v.Kind {
		case KindStruct:
			before := len(tb.lines)
			tb.lines = append(tb.lines, indent+sclIdent(v.Name)+" : Struct")
			tb.vars(v.Members, path, indent+"   ")
			if len(tb.lines) == before+1 {
				tb.lines = tb.lines[:before]
				continue
			}
			tb.lines = append(tb.lines, indent+"END_STRUCT;")
			continue
		case KindArray:
			if !tb.has(path + "[" + strconv.Itoa(v.Lo) + "]") {
				continue
			}
			decl = fmt.Sprintf("Array[%d..%d] of %s", v.Lo, v.Hi, typeName(v.Elem.Kind, v.Elem.StrLen))
		default:
			if !tb.has(path) {
				continue
			}
			decl = typeName(v.Kind, v.StrLen)
		}
		line := indent + sclIdent(v.Name) + " : " + decl
		if v.Init != "" {
			if val, ok := safeValue(v.Init); ok {
				line += " := " + val
			} else {
				tb.dropped = append(tb.dropped, "Startwert von "+path+": "+oneLine(v.Init))
			}
		}
		tb.lines = append(tb.lines, line+";")
	}
}

func typeName(k Kind, strLen int) string {
	switch k {
	case KindBool:
		return "Bool"
	case KindInt:
		return "Int"
	case KindDInt:
		return "DInt"
	case KindUInt:
		return "UInt"
	case KindUDInt:
		return "UDInt"
	case KindWord:
		return "Word"
	case KindDWord:
		return "DWord"
	case KindReal:
		return "Real"
	case KindLReal:
		return "LReal"
	case KindString:
		return "String[" + strconv.Itoa(strLen) + "]"
	case KindDTL:
		return "DTL"
	case KindDT:
		return "Date_And_Time"
	}
	return "?"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(sclComment(s)), " ")
}

// beginLines übernimmt Zuweisungen aus dem BEGIN-Teil, deren Ziel gesendet
// wird und deren Wert sicher ist; die anderen kommen nach dropped.
func beginLines(begin string, sent map[string]bool, dropped []string) ([]string, []string) {
	var out []string
	for _, st := range splitStatements(begin) {
		i := strings.Index(st, ":=")
		if i < 0 {
			dropped = append(dropped, "BEGIN: "+oneLine(st)+";")
			continue
		}
		segs, ok := sclPathSegments(st[:i])
		val, vok := safeValue(st[i+2:])
		if !ok || !vok || !sent[pathKey(segs)] {
			dropped = append(dropped, "BEGIN: "+oneLine(st)+";")
			continue
		}
		out = append(out, sclRef(segs)+" := "+val+";")
	}
	return out, dropped
}

// splitStatements zerlegt den BEGIN-Teil an ; außerhalb von Zeichenketten,
// ohne Kommentare.
func splitStatements(s string) []string {
	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			j := closingQuote(s, i, '\'')
			if j < 0 {
				j = len(s) - 1
			}
			cur.WriteString(s[i : j+1])
			i = j
		case strings.HasPrefix(s[i:], "//"):
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
			cur.WriteByte(' ')
		case strings.HasPrefix(s[i:], "(*"):
			if j := strings.Index(s[i+2:], "*)"); j >= 0 {
				i += j + 3
			} else {
				i = len(s)
			}
			cur.WriteByte(' ')
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// ---------------------------------------------------------------- Teile

const fcArgs = ", buf := #pl, pos := #pos, ovf := #ovf);"

// partCode schreibt die Aufrufe für den values-Inhalt eines Teils.
func partCode(p Part) (string, error) {
	ind := "\t" + strings.Repeat(" ", 20)
	var b strings.Builder
	for _, op := range p.Ops {
		if op.Kind == OpRaw {
			raw := op.Raw
			for raw != "" {
				n := min(len(raw), maxConfigBytes)
				lit, err := SCLString(raw[:n], maxConfigBytes)
				if err != nil {
					return "", err
				}
				b.WriteString(ind + `"PDH_J_Raw"(s := ` + lit + fcArgs + "\n")
				raw = raw[n:]
			}
			continue
		}
		lf := op.Leaf
		segs, ok := sclPathSegments(lf.Path)
		if !ok {
			return "", errors.New(msgLayout)
		}
		ref := "#snap." + sclRef(segs)
		var call string
		switch lf.Kind {
		case KindBool:
			b.WriteString(ind + "IF " + ref + " THEN\n")
			b.WriteString(ind + `    "PDH_J_Raw"(s := 'true'` + fcArgs + "\n")
			b.WriteString(ind + "ELSE\n")
			b.WriteString(ind + `    "PDH_J_Raw"(s := 'false'` + fcArgs + "\n")
			b.WriteString(ind + "END_IF;\n")
			continue
		case KindInt:
			call = `"PDH_J_DInt"(v := INT_TO_DINT(` + ref + `)`
		case KindDInt:
			call = `"PDH_J_DInt"(v := ` + ref
		case KindUInt:
			call = `"PDH_J_UDInt"(v := UINT_TO_UDINT(` + ref + `)`
		case KindUDInt:
			call = `"PDH_J_UDInt"(v := ` + ref
		case KindWord:
			call = `"PDH_J_UDInt"(v := WORD_TO_UDINT(` + ref + `)`
		case KindDWord:
			call = `"PDH_J_UDInt"(v := DWORD_TO_UDINT(` + ref + `)`
		case KindReal:
			call = `"PDH_J_Real"(v := ` + ref
		case KindLReal:
			call = `"PDH_J_LReal"(v := ` + ref
		case KindString:
			call = `"PDH_J_Str"(s := ` + ref
		case KindDTL:
			call = `"PDH_J_DTL"(v := ` + ref + `, zone := FALSE`
		case KindDT:
			call = `"PDH_J_DTL"(v := DT_TO_DTL(` + ref + `), zone := FALSE`
		default:
			return "", errors.New(msgLayout)
		}
		b.WriteString(ind + call + fcArgs + "\n")
	}
	return b.String(), nil
}
