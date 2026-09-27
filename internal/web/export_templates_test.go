package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func newFormRequest(t *testing.T, values url.Values) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/export/templates", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	return req
}

func TestExportTemplateFormValuesValid(t *testing.T) {
	req := newFormRequest(t, url.Values{
		"kind": {"pdf"}, "name": {"Monatsbericht"}, "title": {"Werk Augsburg"}, "orientation": {"L"},
	})
	kind, name, title, sheetName, orientation, ok, notice := exportTemplateFormValues(req)
	if !ok {
		t.Fatalf("expected ok=true, got notice=%q", notice)
	}
	if kind != "pdf" || name != "Monatsbericht" || title != "Werk Augsburg" || sheetName != "" || orientation != "L" {
		t.Errorf("unexpected values: kind=%q name=%q title=%q sheetName=%q orientation=%q", kind, name, title, sheetName, orientation)
	}
}

func TestExportTemplateFormValuesDefaultsOrientation(t *testing.T) {
	req := newFormRequest(t, url.Values{"kind": {"pdf"}, "name": {"Bericht"}})
	_, _, _, _, orientation, ok, _ := exportTemplateFormValues(req)
	if !ok || orientation != "P" {
		t.Fatalf("expected default orientation P, got %q (ok=%v)", orientation, ok)
	}
}

func TestExportTemplateFormValuesInvalidKind(t *testing.T) {
	req := newFormRequest(t, url.Values{"kind": {"csv"}, "name": {"Test"}})
	_, _, _, _, _, ok, notice := exportTemplateFormValues(req)
	if ok {
		t.Fatal("expected ok=false for unsupported kind")
	}
	if notice == "" {
		t.Error("expected a notice message")
	}
}

func TestExportTemplateFormValuesNameTooShort(t *testing.T) {
	req := newFormRequest(t, url.Values{"kind": {"excel"}, "name": {"a"}})
	_, _, _, _, _, ok, _ := exportTemplateFormValues(req)
	if ok {
		t.Fatal("expected ok=false for too-short name")
	}
}

func TestExportTemplateFormValuesExcel(t *testing.T) {
	req := newFormRequest(t, url.Values{"kind": {"excel"}, "name": {"Werte-Export"}, "sheet_name": {"Werte"}})
	kind, _, _, sheetName, _, ok, _ := exportTemplateFormValues(req)
	if !ok || kind != "excel" || sheetName != "Werte" {
		t.Errorf("unexpected result: kind=%q sheetName=%q ok=%v", kind, sheetName, ok)
	}
}
