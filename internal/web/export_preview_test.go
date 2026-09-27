package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func testExportMappings() []ExportMappingView {
	return []ExportMappingView{
		{ID: "1", FieldName: "Temperatur", SourceValue: "21.4", InfrastructureName: "Werk › Halle 1", SourceName: "Sensor 1", SourceReceivedAt: "27.09.2026 16:00:00"},
		{ID: "2", FieldName: "Druck", SourceValue: "3.2", InfrastructureName: "Werk › Halle 2", SourceName: "Sensor 2", SourceReceivedAt: "27.09.2026 16:01:00"},
	}
}

func TestWriteExcelExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "export.xlsx")
	if err := writeExcelExport(path, "Werte", testExportMappings()); err != nil {
		t.Fatalf("writeExcelExport: %v", err)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows("Werte")
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (header + 2 data), got %d: %+v", len(rows), rows)
	}
	if rows[0][0] != "Feld" || rows[0][1] != "Wert" {
		t.Errorf("unexpected header: %+v", rows[0])
	}
	if rows[1][0] != "Temperatur" || rows[1][1] != "21.4" {
		t.Errorf("unexpected row 1: %+v", rows[1])
	}
	if rows[2][0] != "Druck" || rows[2][1] != "3.2" {
		t.Errorf("unexpected row 2: %+v", rows[2])
	}
}

func TestWriteExcelExportDefaultSheetName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.xlsx")
	if err := writeExcelExport(path, "", testExportMappings()); err != nil {
		t.Fatalf("writeExcelExport: %v", err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if _, err := f.GetRows("Export"); err != nil {
		t.Fatalf("expected default sheet name 'Export': %v", err)
	}
}

func TestWritePDFExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "export.pdf")
	if err := writePDFExport(path, "Monatsbericht", testExportMappings()); err != nil {
		t.Fatalf("writePDFExport: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("expected non-empty PDF file")
	}
	header := make([]byte, 5)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if _, err := f.Read(header); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(header) != "%PDF-" {
		t.Fatalf("expected PDF header, got %q", header)
	}
}

func TestWritePDFExportDefaultTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.pdf")
	if err := writePDFExport(path, "", testExportMappings()); err != nil {
		t.Fatalf("writePDFExport: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}
