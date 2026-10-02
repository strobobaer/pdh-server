package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

var testLabel = labelItem{PartID: "p1", PartNumber: "4711-A", Name: "Rillenkugellager 6205-2RSH ^~", Category: "Lager", Unit: "Stk",
	MinQty: "4", Location: "Halle 1 › Regal A › Fach 3", LocationLeaf: "Fach 3", URL: "https://pdh.local/inventory/p1"}

func TestZPLPartLabel(t *testing.T) {
	z := zebraSettingsFrom(map[string]string{"dpi": "300", "label_width_mm": "100", "label_height_mm": "50", "darkness": "20", "speed": "5", "media": "direct"})
	zpl := zplPartLabel(testLabel, z, true, 3)
	for _, want := range []string{"~SD20^XA^CI28^MTD", "^PW1181", "^LL591", "^PR5", "^PQ3", "^BQN,2,", "^FDMA,https://pdh.local/inventory/p1^FS", "Fach 3", "Nr. 4711-A", "Min 4 Stk", "^XZ"} {
		if !strings.Contains(zpl, want) {
			t.Errorf("ZPL enthält %q nicht:\n%s", want, zpl)
		}
	}
	if strings.Contains(zpl, "6205-2RSH ^~") || strings.Count(zpl, "^XA") != 1 {
		t.Error("Steuerzeichen im Text nicht entschärft")
	}
	if test := zplTestLabel("Lager", "https://pdh.local/admin/printers", zebraSettingsFrom(nil)); !strings.Contains(test, "PDH Testetikett") || !strings.Contains(test, "^MTT") {
		t.Errorf("Testetikett: %s", test)
	}
}

func TestZebraStatusParsing(t *testing.T) {
	// ~HS laut ZPL-Handbuch: Papier leer, Kopf offen, Thermotransfer
	hs := "\x02030,1,0,0591,000,0,0,0,000,0,0,0\x03\r\n\x02001,0,1,0,1,2,6,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"
	st, err := parseZebraHS(splitSTXETX(hs))
	if err != nil {
		t.Fatal(err)
	}
	if p := strings.Join(st.Problems(), ","); p != "Druckkopf offen,Etiketten leer" || !st.ThermalTransfer || st.LabelLengthDots != 591 {
		t.Errorf("Status falsch: %+v / %s", st, p)
	}
	ok, _ := parseZebraHS(splitSTXETX("\x02030,0,0,0400,000,0,0,0,000,0,0,0\x03\x02001,0,0,1,0,2,6,0,00000000,1,000\x03"))
	if len(ok.Problems()) != 0 {
		t.Errorf("Farbband leer bei Thermodirekt gemeldet: %v", ok.Problems())
	}
	if _, err := parseZebraHS([]string{"x"}); err == nil {
		t.Error("kaputte Antwort angenommen")
	}
	m, fw, dpi := parseZebraHI([]string{"ZT411-300dpi,V92.21.39Z,12,8176KB"})
	if m != "ZT411-300dpi" || fw != "V92.21.39Z" || dpi != 300 {
		t.Errorf("~HI: %q %q %d", m, fw, dpi)
	}
}

// Ein Schein-Zebra auf 127.0.0.1: beantwortet ~HI und ~HS und nimmt ZPL an.
func TestZebraQueryAndChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("kein lokaler Port:", err)
	}
	defer ln.Close()
	got := make(chan string, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				n, _ := c.Read(buf)
				in := string(buf[:n])
				switch {
				case in == "": // Erreichbarkeitspruefung: verbinden ohne Daten
				case strings.HasPrefix(in, "~HI"):
					c.Write([]byte("\x02ZT411-203dpi,V92.21.39Z,8,8176KB\x03\r\n"))
				case strings.HasPrefix(in, "~HS"):
					c.Write([]byte("\x02030,0,0,0400,000,0,0,0,000,0,0,0\x03\r\n\x02001,0,0,0,1,2,6,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"))
				default:
					got <- in
				}
			}(c)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p := printerView{ID: "z1", Kind: "zebra", Name: "Lager", Enabled: true, Config: map[string]string{"host": host, "port": port, "dpi": "203", "label_width_mm": "100", "label_height_mm": "50"}}
	rep := runPrinterChecks(context.Background(), p)
	if rep.Fail != 0 {
		t.Errorf("Prüfung meldet Fehler: %+v", rep.Items)
	}
	var zustand string
	for _, it := range rep.Items {
		if it.Name == "Zustand" {
			zustand = it.Status + " " + it.Detail
		}
	}
	if zustand != "ok bereit" {
		t.Errorf("Zustand: %q", zustand)
	}
	if err := rawSend(context.Background(), host, port, []byte(zplPartLabel(testLabel, zebraSettingsFrom(p.Config), false, 1))); err != nil {
		t.Fatal(err)
	}
	if in := <-got; !strings.Contains(in, "^XA") {
		t.Errorf("Drucker hat kein ZPL bekommen: %q", in)
	}
	// falsche Aufloesung eingestellt → Fehler
	p.Config["dpi"] = "300"
	if rep := runPrinterChecks(context.Background(), p); rep.Fail == 0 {
		t.Error("abweichende Auflösung nicht gemeldet")
	}
}

// Ein Schein-IPP-Drucker: beantwortet Get-Printer-Attributes und Print-Job.
func TestIPPRoundTrip(t *testing.T) {
	var lastOp uint16
	var gotPDF []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastOp = binary.BigEndian.Uint16(body[2:4])
		_, attrs, _ := parseIPPResponse(body) // gleiches Format wie die Anfrage
		if v := attrs["printer-uri"]; len(v) == 0 || !strings.HasPrefix(v[0].String(), "ipp://127.0.0.1:") {
			t.Errorf("printer-uri fehlt: %v", attrs["printer-uri"])
		}
		resp := &ippWriter{}
		resp.Write([]byte{0x02, 0x00, 0x00, 0x00})
		resp.Write(body[4:8])
		resp.WriteByte(ippTagOperation)
		resp.str(ippTagCharset, "attributes-charset", "utf-8")
		if lastOp == ippOpGetPrinterAttrs {
			resp.WriteByte(0x04) // printer-attributes
			resp.integer(ippTagEnum, "printer-state", 3)
			resp.str(ippTagKeyword, "printer-state-reasons", "toner-low-warning")
			resp.str(ippTagText, "printer-make-and-model", "Brother MFC-L8900CDW")
			resp.attr(ippTagBoolean, "printer-is-accepting-jobs", []byte{1})
			resp.str(ippTagMimeMediaType, "document-format-supported", "application/octet-stream")
			resp.str(ippTagMimeMediaType, "", "application/pdf")
			resp.str(ippTagName, "marker-names", "Black Toner")
			resp.str(ippTagName, "", "Cyan Toner")
			resp.integer(ippTagInteger, "marker-levels", 8)
			resp.integer(ippTagInteger, "", 60)
		} else {
			i := bytes.Index(body, []byte("%PDF"))
			if i >= 0 {
				gotPDF = body[i:]
			}
		}
		resp.WriteByte(ippTagEnd)
		w.Header().Set("Content-Type", "application/ipp")
		w.Write(resp.Bytes())
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := map[string]string{"host": u.Hostname(), "port": u.Port(), "protocol": "ipp", "paper": "A4"}
	info, err := ippGetPrinter(context.Background(), ippTargetFrom(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if info.State != 3 || info.MakeModel != "Brother MFC-L8900CDW" || len(info.Formats) != 2 || strings.Join(info.Markers, "|") != "Black Toner 8 %|Cyan Toner 60 %" || ippReasonText(info.Reasons[0]) != "Toner fast leer" {
		t.Errorf("Druckerinfo falsch: %+v", info)
	}
	rep := runPrinterChecks(context.Background(), printerView{Kind: "network", Name: "Büro", Enabled: true, Config: cfg})
	if rep.Fail != 0 || rep.Warn < 2 { // Toner-Warnung + Zustandsgrund
		t.Errorf("Prüfung: %+v", rep.Items)
	}
	pdf, err := networkTestPDF("Büro", "EG", "https://pdh.local/admin/printers", cfg)
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("Testseite: %v", err)
	}
	if err := sendToNetworkPrinter(context.Background(), cfg, "PDH Testseite", pdf, 2); err != nil {
		t.Fatal(err)
	}
	if lastOp != ippOpPrintJob || !bytes.Equal(gotPDF, pdf) {
		t.Errorf("Druckauftrag nicht angekommen (op %x, %d Bytes)", lastOp, len(gotPDF))
	}
}

func TestLabelsPDFAndDymoXML(t *testing.T) {
	for _, key := range []string{"62x29", "a4-70x37", "89x36"} {
		pdf, err := labelsPDF([]labelItem{testLabel, testLabel}, labelSizeByKey(key), 2, 1, true)
		if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			t.Errorf("%s: %v", key, err)
		}
	}
	if _, err := labelsPDF(nil, labelSizeByKey("62x29"), 1, 0, true); err == nil {
		t.Error("leere Auswahl ergab ein PDF")
	}
	for _, typ := range dymoLabelTypes {
		x := dymoPartLabel(testLabel, typ, "", true)
		if err := xml.Unmarshal([]byte(x), new(struct{ XMLName xml.Name })); err != nil {
			t.Errorf("%s: kein gültiges XML: %v", typ.Key, err)
		}
		if !strings.Contains(x, "<PaperName>"+typ.PaperName+"</PaperName>") || !strings.Contains(x, "<Type>QRCode</Type>") || !strings.Contains(x, "Fach 3") {
			t.Errorf("%s: Inhalt fehlt", typ.Key)
		}
	}
	tx := dymoTestLabel("Dymo <Lager>", "https://pdh.local", dymoLabelTypeByKey("99012"), "Eigenes Papier")
	if err := xml.Unmarshal([]byte(tx), new(struct{ XMLName xml.Name })); err != nil || !strings.Contains(tx, "Dymo &lt;Lager&gt;") || !strings.Contains(tx, "<PaperName>Eigenes Papier</PaperName>") {
		t.Errorf("Testetikett: %v", err)
	}
}

func TestValidatePrinterConfig(t *testing.T) {
	bad := []struct {
		kind string
		cfg  map[string]string
	}{
		{"zebra", map[string]string{}},
		{"zebra", map[string]string{"host": "http://192.168.1.5/"}},
		{"zebra", map[string]string{"host": "zebra1", "dpi": "250"}},
		{"zebra", map[string]string{"host": "zebra1", "label_width_mm": "300"}},
		{"dymo", map[string]string{}},
		{"network", map[string]string{"host": "drucker", "protocol": "lpd"}},
		{"network", map[string]string{"host": "drucker", "port": "70000"}},
	}
	for _, b := range bad {
		if err := validatePrinterConfig(b.kind, b.cfg); err == nil {
			t.Errorf("%s %v angenommen", b.kind, b.cfg)
		}
	}
	if err := validatePrinterConfig("zebra", map[string]string{"host": "192.168.1.50", "dpi": "203", "label_width_mm": "100", "label_height_mm": "50"}); err != nil {
		t.Error(err)
	}
	if s := ippTargetFrom(map[string]string{"host": "drucker", "protocol": "ipps"}); s.HTTPURL != "https://drucker:631/ipp/print" || s.PrinterURI != "ipps://drucker:631/ipp/print" {
		t.Errorf("IPPS-Ziel: %+v", s)
	}
}

func TestPrinterPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	page := renderPage(t, tmpl, "printers", PrintersPageData{
		Kinds: printerKinds, DymoLabelTypes: dymoLabelTypes,
		Printers: []printerView{
			{ID: "z1", Kind: "zebra", KindLabel: "Zebra", Name: "Lager", Enabled: true, IsDefault: true, Config: map[string]string{"host": "1.2.3.4"}, ConfigJSON: `{"host":"1.2.3.4"}`,
				Check: &checkReport{Items: []checkResult{{Group: "Drucker", Name: "Zustand", Status: "fail", Detail: "Etiketten leer"}}, Fail: 1}},
			{ID: "d1", Kind: "dymo", KindLabel: "Dymo", Name: "Ausgabe", Enabled: true, Config: map[string]string{"printer_name": "DYMO LabelWriter 450"}, ConfigJSON: `{}`},
		},
	})
	for _, want := range []string{"Zebra ZT4xx", "Dymo LabelWriter", "Netzwerkdrucker", "Etiketten leer", "prDymoCheck(this)", "window.pdhDymo", `action="/admin/printers/z1/test"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Druckerseite enthält %q nicht", want)
		}
	}
	checkScripts(t, "drucker", page)
}

func TestLabelsPageDirectPrint(t *testing.T) {
	c, _ := loadTestTemplates(t).Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "labels.gohtml")); err != nil {
		t.Fatal(err)
	}
	item := testLabel
	data := LabelsPageData{Size: labelSizeByKey("62x29"), Sizes: labelSizes, Labels: []labelItem{item}, Copies: 1, Pages: [][]*labelItem{{&item}},
		Printers: []labelPrintOption{{ID: "z1", Name: "Zebra Lager", Kind: "zebra", IsDefault: true}, {ID: "d1", Name: "Dymo", Kind: "dymo"}}}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "labels-page", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`id="lp-printer"`, `value="z1" data-kind="zebra" selected`, "Direkt drucken", "window.pdhDymo", "/inventory/labels/print"} {
		if !strings.Contains(out, want) {
			t.Errorf("Druckansicht enthält %q nicht", want)
		}
	}
	checkScripts(t, "etiketten-direkt", out)
	buf.Reset()
	data.Printers = nil
	_ = c.ExecuteTemplate(&buf, "labels-page", data)
	if strings.Contains(buf.String(), "lp-printer") {
		t.Error("ohne Drucker/Recht wird Direktdruck angezeigt")
	}
}
