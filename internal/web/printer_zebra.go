package web

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"
)

// Zebra ZT4xx (ZT410/ZT411/ZT420/ZT421): Etiketten als ZPL ueber TCP 9100.
// Status ueber ~HS (Papier, Pause, Kopf offen, Farbband …), Modell und
// Aufloesung ueber ~HI. Texte in UTF-8 (^CI28).

func mmToDots(mm float64, dpi int) int { return int(math.Round(mm / 25.4 * float64(dpi))) }

// zplText: Feldinhalt ohne Steuerzeichen (^ und ~ beginnen ZPL-Befehle).
func zplText(s string) string {
	r := strings.NewReplacer("^", " ", "~", "-", "\r", " ", "\n", " ", "\\", "/")
	return strings.TrimSpace(r.Replace(s))
}

type zebraSettings struct {
	DPI             int
	WidthMM         float64
	HeightMM        float64
	Darkness        int  // 0–30
	Speed           int  // Zoll/s, 2–12
	ThermalTransfer bool // Farbband (true) oder Thermodirekt
}

func zebraSettingsFrom(config map[string]string) zebraSettings {
	atof := func(k string, def float64) float64 {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(config[k]), ",", "."), 64); err == nil && v > 0 {
			return v
		}
		return def
	}
	atoi := func(k string, def, lo, hi int) int {
		if v, err := strconv.Atoi(strings.TrimSpace(config[k])); err == nil && v >= lo && v <= hi {
			return v
		}
		return def
	}
	return zebraSettings{
		DPI: atoi("dpi", 203, 150, 600), WidthMM: atof("label_width_mm", 100), HeightMM: atof("label_height_mm", 50),
		Darkness: atoi("darkness", 15, 0, 30), Speed: atoi("speed", 4, 2, 12), ThermalTransfer: config["media"] != "direct",
	}
}

func (z zebraSettings) header(b *bytes.Buffer, copies int) {
	media := "D"
	if z.ThermalTransfer {
		media = "T"
	}
	// ~SD: absolute Schwaerzung (00–30), ^MD waere relativ zur Druckereinstellung
	fmt.Fprintf(b, "~SD%02d^XA^CI28^MT%s^PW%d^LL%d^LH0,0^PR%d", z.Darkness, media, mmToDots(z.WidthMM, z.DPI), mmToDots(z.HeightMM, z.DPI), z.Speed)
	if copies > 1 {
		fmt.Fprintf(b, "^PQ%d", copies)
	}
}

// qrMagnification: Modulgroesse, damit der QR-Code in avail Punkte passt.
func qrMagnification(url string, avail int) int {
	size := 33
	if code, err := qr.Encode(url, qr.M); err == nil {
		size = code.Size
	}
	mag := avail / (size + 2)
	if mag < 1 {
		mag = 1
	}
	if mag > 10 {
		mag = 10
	}
	return mag
}

// zplPartLabel: Lagerplatz-Etikett (QR links, Texte rechts) wie die Druckansicht.
func zplPartLabel(it labelItem, z zebraSettings, showCat bool, copies int) string {
	var b bytes.Buffer
	z.header(&b, copies)
	w, h := mmToDots(z.WidthMM, z.DPI), mmToDots(z.HeightMM, z.DPI)
	m := mmToDots(1.5, z.DPI)
	qrAvail := h - 2*m
	mag := qrMagnification(it.URL, qrAvail)
	if it.URL != "" {
		// ^BQ setzt den Code etwas tiefer an – oben bleibt dafuer der Rand
		fmt.Fprintf(&b, "^FO%d,%d^BQN,2,%d^FDMA,%s^FS", m, m/2, mag, zplText(it.URL))
	}
	x := m + qrAvail + m
	if it.URL == "" {
		x = m
	}
	tw := w - x - m
	big := int(float64(h) * 0.17)
	mid := int(float64(h) * 0.12)
	small := int(float64(h) * 0.085)
	y := m
	loc := it.LocationLeaf
	if loc == "" {
		loc = "ohne Lagerplatz"
	}
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,1,0,L^FD%s^FS", x, y, big, big, tw, zplText(loc))
	y += big + m/2
	fmt.Fprintf(&b, "^FO%d,%d^GB%d,%d,%d^FS", x, y, tw, 2, 2)
	y += m
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,2,0,L^FD%s^FS", x, y, mid, mid, tw, zplText(it.Name))
	y += 2*mid + m/2
	if showCat && it.Category != "" && y+small < h-big-m {
		fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,1,0,L^FD%s^FS", x, y, small, small, tw, zplText(it.Category))
	}
	by := h - m - mid
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,1,0,L^FDNr. %s^FS", x, by, mid, mid, tw/2+tw/6, zplText(it.PartNumber))
	minTxt := "Min " + it.MinQty
	if it.Unit != "" {
		minTxt += " " + it.Unit
	}
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,1,0,R^FD%s^FS", x, by, mid, mid, tw, zplText(minTxt))
	b.WriteString("^XZ\n")
	return b.String()
}

// zplTestLabel: Testetikett mit Rahmen, Texten und QR-Code.
func zplTestLabel(printerName, url string, z zebraSettings) string {
	var b bytes.Buffer
	z.header(&b, 1)
	w, h := mmToDots(z.WidthMM, z.DPI), mmToDots(z.HeightMM, z.DPI)
	m := mmToDots(1.5, z.DPI)
	fmt.Fprintf(&b, "^FO%d,%d^GB%d,%d,%d^FS", m, m, w-2*m, h-2*m, 3)
	f := int(float64(h) * 0.14)
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FDPDH Testetikett^FS", 2*m, 2*m, f, f)
	s := int(float64(h) * 0.09)
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FD%s^FS", 2*m, 2*m+f+m, s, s, zplText(printerName))
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FD%s · %d dpi · %.0f × %.0f mm^FS", 2*m, 2*m+f+s+2*m, s, s, time.Now().Format("02.01.2006 15:04"), z.DPI, z.WidthMM, z.HeightMM)
	fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FDÄÖÜ äöü ß – Umlaute ok?^FS", 2*m, 2*m+f+2*s+3*m, s, s)
	if url != "" {
		avail := h / 2
		fmt.Fprintf(&b, "^FO%d,%d^BQN,2,%d^FDMA,%s^FS", w-avail-2*m, h-avail-2*m, qrMagnification(url, avail), zplText(url))
	}
	b.WriteString("^XZ\n")
	return b.String()
}

// rawSend schickt Daten an einen Drucker (Port 9100 / JetDirect).
func rawSend(ctx context.Context, host, port string, data []byte) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("Drucker nicht erreichbar: %w", err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("Senden fehlgeschlagen: %w", err)
	}
	return nil
}

// zebraQuery sendet einen Abfragebefehl (~HS, ~HI) und liest die Antwort
// (Bloecke zwischen STX und ETX).
func zebraQuery(ctx context.Context, host, port, cmd string, blocks int) ([]string, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tmp := make([]byte, 512)
	for strings.Count(buf.String(), "\x03") < blocks {
		n, err := conn.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	return splitSTXETX(buf.String()), nil
}

func splitSTXETX(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, 0x02)
		if i < 0 {
			return out
		}
		j := strings.IndexByte(s[i:], 0x03)
		if j < 0 {
			return out
		}
		out = append(out, s[i+1:i+j])
		s = s[i+j+1:]
	}
}

type zebraStatus struct {
	PaperOut, Paused, HeadOpen, RibbonOut, BufferFull, CorruptRAM, UnderTemp, OverTemp bool
	ThermalTransfer                                                                    bool
	LabelLengthDots, FormatsInBuffer, LabelsRemaining                                  int
}

// parseZebraHS wertet die Antwort auf ~HS aus (ZPL-Handbuch, Host Status Return).
func parseZebraHS(blocks []string) (zebraStatus, error) {
	var st zebraStatus
	if len(blocks) < 2 {
		return st, fmt.Errorf("unvollständige Statusantwort (%d von 3 Teilen)", len(blocks))
	}
	a := strings.Split(blocks[0], ",")
	b := strings.Split(blocks[1], ",")
	if len(a) < 12 || len(b) < 9 {
		return st, fmt.Errorf("unbekanntes Statusformat")
	}
	flag := func(f []string, i int) bool { return strings.TrimSpace(f[i]) == "1" }
	num := func(f []string, i int) int { n, _ := strconv.Atoi(strings.TrimSpace(f[i])); return n }
	st.PaperOut, st.Paused = flag(a, 1), flag(a, 2)
	st.LabelLengthDots, st.FormatsInBuffer = num(a, 3), num(a, 4)
	st.BufferFull, st.CorruptRAM, st.UnderTemp, st.OverTemp = flag(a, 5), flag(a, 9), flag(a, 10), flag(a, 11)
	st.HeadOpen, st.RibbonOut, st.ThermalTransfer = flag(b, 2), flag(b, 3), flag(b, 4)
	st.LabelsRemaining = num(b, 8)
	return st, nil
}

// Problems: was den Druck verhindert (leer = bereit).
func (s zebraStatus) Problems() []string {
	var p []string
	if s.HeadOpen {
		p = append(p, "Druckkopf offen")
	}
	if s.PaperOut {
		p = append(p, "Etiketten leer")
	}
	if s.RibbonOut && s.ThermalTransfer {
		p = append(p, "Farbband leer")
	}
	if s.Paused {
		p = append(p, "pausiert (Pause-Taste)")
	}
	if s.BufferFull {
		p = append(p, "Empfangspuffer voll")
	}
	if s.CorruptRAM {
		p = append(p, "Speicherfehler – Drucker neu starten")
	}
	if s.UnderTemp {
		p = append(p, "zu kalt")
	}
	if s.OverTemp {
		p = append(p, "Druckkopf zu heiß")
	}
	return p
}

// parseZebraHI: "ZT411-203dpi,V92.21.39Z,8,8176KB" → Modell, Firmware, dpi.
func parseZebraHI(blocks []string) (model, firmware string, dpi int) {
	if len(blocks) == 0 {
		return "", "", 0
	}
	f := strings.Split(blocks[0], ",")
	model = strings.TrimSpace(f[0])
	if len(f) > 1 {
		firmware = strings.TrimSpace(f[1])
	}
	if i := strings.Index(model, "-"); i > 0 {
		d := strings.TrimSuffix(strings.ToLower(model[i+1:]), "dpi")
		dpi, _ = strconv.Atoi(d)
	}
	return
}
