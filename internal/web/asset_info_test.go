package web

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetLabelTexts(t *testing.T) {
	part := testLabel
	if !strings.HasPrefix(part.numText(), "Nr. ") || !strings.HasPrefix(part.rightText(), "Min ") {
		t.Errorf("Ersatzteil-Etikett verändert: %q / %q", part.numText(), part.rightText())
	}
	a := labelItem{Asset: true, LocationLeaf: "Presse 3", PartNumber: "SN-77", Right: "QR scannen", URL: "https://pdh/a/x"}
	if a.numText() != "SN-77" || a.rightText() != "QR scannen" || a.headText() != "Presse 3" {
		t.Errorf("Anlagen-Etikett: %q / %q / %q", a.numText(), a.rightText(), a.headText())
	}
	zpl := zplPartLabel(a, zebraSettings{DPI: 203, WidthMM: 62, HeightMM: 29}, true, 1)
	for _, want := range []string{"^FDPresse 3^FS", "^FDSN-77^FS", "^FDQR scannen^FS", "https://pdh/a/x"} {
		if !strings.Contains(zpl, want) {
			t.Errorf("ZPL enthält %q nicht", want)
		}
	}
	if strings.Contains(zpl, "Min ") || strings.Contains(zpl, "Nr. ") {
		t.Error("Anlagen-Etikett zeigt Ersatzteil-Felder")
	}
}

func TestAssetLabelsPage(t *testing.T) {
	c, _ := loadTestTemplates(t).Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "labels.gohtml")); err != nil {
		t.Fatal(err)
	}
	item := labelItem{Asset: true, LocationLeaf: "Presse 3", Name: "Anlage", Category: "Halle 2 › Linie 1", PartNumber: "SN-77", NumKey: "Serien-Nr.", RightKey: "Störung?", Right: "QR scannen"}
	data := LabelsPageData{Size: labelSizeByKey("62x50"), Sizes: labelSizes, Labels: []labelItem{item}, Copies: 1, Pages: [][]*labelItem{{&item}}, ShowCat: true,
		Action: "/a/labels", PrintURL: "/a/labels/print", Assets: true, Printers: []labelPrintOption{{ID: "z1", Name: "Zebra", Kind: "zebra"}}}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "labels-page", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`action="/a/labels"`, `"/a/labels/print"`, "Serien-Nr.", "SN-77", "QR scannen", "Halle 2 › Linie 1", ">Pfad <"} {
		if !strings.Contains(out, want) {
			t.Errorf("Anlagen-Etiketten enthalten %q nicht", want)
		}
	}
	for _, miss := range []string{"Mindestmenge", "Teile-Nr."} {
		if strings.Contains(out, miss) {
			t.Errorf("Anlagen-Etiketten zeigen %q", miss)
		}
	}
}

func TestAssetInfoPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := AssetInfoData{
		Asset: assetInfoView{Kind: "infra", ID: "11111111-1111-1111-1111-111111111111", Name: "Presse 3", TypeLabel: "Anlage", TypeIcon: "ti-settings",
			Path: "Halle 2 › Presse 3", ParentID: "22222222-2222-2222-2222-222222222222", ParentName: "Halle 2", SubCount: 1, DetailURL: "/infrastructure/11111111-1111-1111-1111-111111111111"},
		Groups: []assetOpenGroup{
			{Key: "fault", Label: "Störungen", Icon: "ti-alert-triangle", NewLabel: "Störung melden", NewURL: "/assignments/new?type=fault&infra=1",
				Items: []assetOpenItem{{ID: "f1", Title: "Hydraulik undicht", URL: "/faults/f1", StatusLabel: "Offen", Due: "01.10.2026", Overdue: true, Where: "Pumpe"}}},
			{Key: "maintenance", Label: "Wartungen", NewHint: "Für eine Wartung muss das Gerät einer Anlage zugeordnet sein."},
		},
		Children: []assetChild{{ID: "33333333-3333-3333-3333-333333333333", Name: "Pumpe", TypeIcon: "ti-plug"}},
		Total:    1, Sub: true, URL: "https://pdh/a/11111111-1111-1111-1111-111111111111", CanLabel: true,
	}
	out := renderPage(t, tmpl, "asset_info", d)
	for _, want := range []string{"Presse 3", "Störung melden", `data-rx-edit="/faults/f1"`, `data-rx-done="fault:f1"`, `data-rx-done-label="Beheben"`, "überfällig seit", "Pumpe", `href="/a/22222222-2222-2222-2222-222222222222"`,
		`href="/a/labels?infra=11111111-1111-1111-1111-111111111111"`, "mit allen Unteranlagen", "Für eine Wartung muss", "nur diese Anlage"} {
		if !strings.Contains(out, want) {
			t.Errorf("Infoseite enthält %q nicht", want)
		}
	}
	checkScripts(t, "asset-info", out)
}

func TestLoginNext(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/a/123": "/a/123", "/faults/1?tab=work": "/faults/1?tab=work",
		"//evil.example": "/", "/\\evil.example": "/", "https://evil.example": "/", "/login/forgot": "/", "/a/1\r\nX: y": "/",
	} {
		if got := loginNext(in); got != want {
			t.Errorf("loginNext(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
