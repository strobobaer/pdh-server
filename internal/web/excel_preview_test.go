package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestColumnLetter(t *testing.T) {
	cases := map[int]string{0: "A", 1: "B", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for n, want := range cases {
		if got := columnLetter(n); got != want {
			t.Errorf("columnLetter(%d) = %q, want %q", n, got, want)
		}
	}
}

func writeTestWorkbook(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	rows := [][]string{
		{"Name", "Wert"},
		{"Temperatur Halle 1", "21.4"},
		{"Temperatur Halle 2", "19.8"},
	}
	for i, row := range rows {
		for j, cell := range row {
			ref, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName: %v", err)
			}
			if err := f.SetCellValue("Sheet1", ref, cell); err != nil {
				t.Fatalf("SetCellValue: %v", err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "test.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	return path
}

func TestReadExcelPreviewWithHeader(t *testing.T) {
	path := writeTestWorkbook(t)
	cols, rows, err := readExcelPreview(path, "", true)
	if err != nil {
		t.Fatalf("readExcelPreview: %v", err)
	}
	if len(cols) != 2 || cols[0].Label != "Name" || cols[1].Label != "Wert" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 2 || rows[0][0] != "Temperatur Halle 1" || rows[0][1] != "21.4" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestReadExcelPreviewWithoutHeader(t *testing.T) {
	path := writeTestWorkbook(t)
	cols, rows, err := readExcelPreview(path, "", false)
	if err != nil {
		t.Fatalf("readExcelPreview: %v", err)
	}
	if len(cols) != 2 || cols[0].Label != "A" || cols[1].Label != "B" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 data rows (no header stripped), got %d", len(rows))
	}
}

func TestReadExcelPreviewMissingFile(t *testing.T) {
	if _, _, err := readExcelPreview(filepath.Join(t.TempDir(), "missing.xlsx"), "", true); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadExcelPreviewNoPath(t *testing.T) {
	if _, _, err := readExcelPreview("", "", true); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func writeTestCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.csv")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestReadCSVPreviewWithHeaderCommaDelimiter(t *testing.T) {
	path := writeTestCSV(t, "Name,Wert\nTemperatur Halle 1,21.4\nTemperatur Halle 2,19.8\n")
	cols, rows, err := readCSVPreview(path, "", true)
	if err != nil {
		t.Fatalf("readCSVPreview: %v", err)
	}
	if len(cols) != 2 || cols[0].Label != "Name" || cols[1].Label != "Wert" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 2 || rows[0][0] != "Temperatur Halle 1" || rows[0][1] != "21.4" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestReadCSVPreviewSemicolonDelimiter(t *testing.T) {
	path := writeTestCSV(t, "Name;Wert\nDruck;4.2\n")
	cols, rows, err := readCSVPreview(path, ";", true)
	if err != nil {
		t.Fatalf("readCSVPreview: %v", err)
	}
	if len(cols) != 2 || cols[0].Label != "Name" || cols[1].Label != "Wert" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 1 || rows[0][0] != "Druck" || rows[0][1] != "4.2" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestReadCSVPreviewWithoutHeader(t *testing.T) {
	path := writeTestCSV(t, "1,2\n3,4\n5,6\n")
	cols, rows, err := readCSVPreview(path, "", false)
	if err != nil {
		t.Fatalf("readCSVPreview: %v", err)
	}
	if len(cols) != 2 || cols[0].Label != "A" || cols[1].Label != "B" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 data rows (no header stripped), got %d", len(rows))
	}
}

func TestReadCSVPreviewMissingFile(t *testing.T) {
	if _, _, err := readCSVPreview(filepath.Join(t.TempDir(), "missing.csv"), "", true); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadCSVPreviewNoPath(t *testing.T) {
	if _, _, err := readCSVPreview("", "", true); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestCsvDelimiterRune(t *testing.T) {
	if got := csvDelimiterRune(""); got != ',' {
		t.Fatalf("csvDelimiterRune(\"\") = %q, want ','", got)
	}
	if got := csvDelimiterRune(";"); got != ';' {
		t.Fatalf("csvDelimiterRune(\";\") = %q, want ';'", got)
	}
}
