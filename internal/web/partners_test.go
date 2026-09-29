package web

import (
	"bytes"
	"html/template"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/core/directory"
)

func TestSuggestOrderQty(t *testing.T) {
	cases := []struct {
		stock, min, reorder, supplierMin, want float64
	}{
		{2, 5, 10, 0, 10}, // Nachbestellmenge reicht
		{0, 5, 3, 0, 8},   // Fehlmenge + Nachbestellmenge
		{1, 2, 0, 0, 1},   // keine Nachbestellmenge -> Fehlmenge
		{4, 5, 2, 25, 25}, // Mindestabnahme Lieferant
		{0.5, 2, 1, 0, 3}, // aufrunden auf ganze Einheiten
		{5, 5, 0, 0, 1},   // genau auf Mindestbestand -> mindestens 1
	}
	for _, c := range cases {
		if got := suggestOrderQty(c.stock, c.min, c.reorder, c.supplierMin); got != c.want {
			t.Errorf("suggestOrderQty(%v,%v,%v,%v) = %v, want %v", c.stock, c.min, c.reorder, c.supplierMin, got, c.want)
		}
	}
}

func TestPurchaseCronSpec(t *testing.T) {
	if s := purchaseCronSpec(true, "06:30", true); s != "30 6 * * 1-5" {
		t.Errorf("spec: %q", s)
	}
	if s := purchaseCronSpec(true, "18:05", false); s != "5 18 * * *" {
		t.Errorf("spec: %q", s)
	}
	if s := purchaseCronSpec(false, "06:00", true); s != "" {
		t.Errorf("deaktiviert erwartet, got %q", s)
	}
}

func TestSecretRoundTrip(t *testing.T) {
	h := &Handler{jwtSecret: strings.Repeat("x", 32)}
	enc, err := encryptSecret(h.credentialKey(), "geheim!ÄÖÜ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "geheim") {
		t.Fatal("Klartext im Schlüsseltext")
	}
	plain, err := decryptSecret(h.credentialKey(), enc)
	if err != nil || plain != "geheim!ÄÖÜ" {
		t.Fatalf("roundtrip: %q %v", plain, err)
	}
	other := &Handler{jwtSecret: strings.Repeat("y", 32)}
	if _, err := decryptSecret(other.credentialKey(), enc); err == nil {
		t.Error("falscher Schlüssel darf nicht entschlüsseln")
	}
}

func TestValidPortalURLAndSafeReturn(t *testing.T) {
	if u, err := validPortalURL("shop.example.com/login"); err != nil || u != "https://shop.example.com/login" {
		t.Errorf("url: %q %v", u, err)
	}
	for _, bad := range []string{"javascript:alert(1)", "ftp://x", ""} {
		if _, err := validPortalURL(bad); err == nil {
			t.Errorf("%q sollte abgelehnt werden", bad)
		}
	}
	if safeReturn("/inventory/1") != "/inventory/1" || safeReturn("//evil.com") != "" || safeReturn("https://evil.com") != "" {
		t.Error("safeReturn")
	}
}

func TestPartnerAssignColumnWhitelist(t *testing.T) {
	if tbl, col, ok := partnerAssignColumn("infra", "service"); !ok || tbl != "infrastructure" || col != "service_partner_id" {
		t.Error("infra/service")
	}
	if _, _, ok := partnerAssignColumn("infra", "x; DROP TABLE"); ok {
		t.Error("unbekannte Rolle darf keine Spalte liefern")
	}
}

func loadTestTemplates(t *testing.T) *template.Template {
	t.Helper()
	root := filepath.Join("..", "..", "web", "templates")
	tmpl, err := template.New("base.gohtml").Funcs(TemplateFuncs()).ParseFiles(filepath.Join(root, "base.gohtml"))
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	if _, err := tmpl.ParseGlob(filepath.Join(root, "widgets", "*.gohtml")); err != nil {
		t.Fatalf("widgets: %v", err)
	}
	return tmpl
}

func renderPage(t *testing.T, tmpl *template.Template, page string, data interface{}) string {
	t.Helper()
	c, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", page+".gohtml")); err != nil {
		t.Fatalf("%s parse: %v", page, err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "base.gohtml", data); err != nil {
		t.Fatalf("%s render: %v", page, err)
	}
	return buf.String()
}

func TestPartnerPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)

	board := renderPage(t, tmpl, "directory", PartnerBoardData{
		CanEdit: true, Total: 2,
		Rows: []PartnerRow{{ID: "p1", Name: "ACME", KindLabel: "Hersteller", Active: true, ContractState: "soon", ContractUntil: "01.10.2026"}},
	})
	for _, want := range []string{"ACME", "/directory/p1", "Bestellvorschlag"} {
		if !strings.Contains(board, want) {
			t.Errorf("Board enthält %q nicht", want)
		}
	}

	// Neuanlage: keine Kontakte, keine Links
	renderPage(t, tmpl, "partner_detail", PartnerDetailData{IsNew: true, CanEdit: true, Tab: "master", LinkKinds: partnerLinkKinds})

	p := directory.Partner{ID: "p1"}
	p.Name, p.Kind, p.SupportHotline, p.SparePartsEmail = "ACME", "both", "0800 123", "teile@acme.de"
	detail := renderPage(t, tmpl, "partner_detail", PartnerDetailData{
		CanEdit: true, CanCredentials: true, Tab: "portals", Partner: p, LinkKinds: partnerLinkKinds,
		Links: []PartnerLinkView{
			{ID: "l1", Kind: "shop", Label: "Shop", URL: "https://shop.acme.de", Embed: true, Username: "einkauf", HasPassword: true},
			{ID: "l2", Kind: "support_portal", Label: "Support", URL: "https://support.acme.de"},
		},
		Infra: []PartnerInfraRow{{ID: "i1", Name: "Presse 1", Roles: []string{"manufacturer", "service"}}},
		Parts: []PartnerPartRow{{ID: "s1", Name: "Lager", IsSupplier: true, Preferred: true}},
	})
	for _, want := range []string{"0800 123", "teile@acme.de", "data-frame-external", "pdPassword('p1','l1'",
		`value="shop" selected`, "Servicepartner", "/infrastructure/i1"} {
		if !strings.Contains(detail, want) {
			t.Errorf("Detail enthält %q nicht", want)
		}
	}

	// Ohne Zugangsdaten-Recht keine Passwort-Endpunkte
	noCred := renderPage(t, tmpl, "partner_detail", PartnerDetailData{Tab: "portals", Partner: p, LinkKinds: partnerLinkKinds,
		Links: []PartnerLinkView{{ID: "l1", Kind: "shop", Label: "Shop", URL: "https://x", Username: "geheimuser", HasPassword: true}}})
	if strings.Contains(noCred, "pdPassword('") || strings.Contains(noCred, "geheimuser") {
		t.Error("Zugangsdaten ohne Berechtigung sichtbar")
	}

	group := &PurchaseBuyerGroup{BuyerName: "Eva", BuyerEmail: "eva@x", Positions: 1, Total: "10,00 €",
		Suppliers: []*PurchaseSupplierGroup{{PartnerID: "p1", Name: "ACME", Total: "10,00 €",
			Lines: []PurchaseLine{{PartID: "s1", Name: "Lager", OrderQty: "5", Unit: "Stück"}}}}}
	renderPage(t, tmpl, "purchase_report", PurchaseReportData{CanEdit: true, Groups: []*PurchaseBuyerGroup{group}})

	var mail bytes.Buffer
	mt, _ := tmpl.Clone()
	if err := mt.ExecuteTemplate(&mail, "purchase-report-mail", map[string]interface{}{"Group": group, "BaseURL": "https://pdh", "Date": "29.09.2026"}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if !strings.Contains(mail.String(), "https://pdh/inventory/s1") {
		t.Error("Mail ohne Teile-Link")
	}

}

func TestModulePagesParse(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, page := range []string{"infra_detail", "it_detail"} {
		c, _ := tmpl.Clone()
		if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", page+".gohtml")); err != nil {
			t.Errorf("%s: %v", page, err)
		}
	}
	// IT-Detail ohne Lieferant darf nicht an nil-Zeiger scheitern
	c, _ := tmpl.Clone()
	c.ParseFiles(filepath.Join("..", "..", "web", "templates", "it_detail.gohtml"))
	if err := c.ExecuteTemplate(io.Discard, "base.gohtml", ITDetailData{}); err != nil {
		t.Errorf("it_detail render: %v", err)
	}
}
