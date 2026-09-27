package web

import (
	"database/sql"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopcua/opcua/ua"
)

func testExportMappingsForSQL() []ExportMappingView {
	return []ExportMappingView{
		{ID: "1", FieldName: "temp", SourceValue: "21.4", InfrastructureName: "Werk › Halle 1", SourceName: "Sensor 1"},
		{ID: "2", FieldName: "druck", SourceValue: "3.2", InfrastructureName: "Werk › Halle 2", SourceName: "Sensor 2"},
	}
}

func TestWriteSQLExportSqlite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.db")
	config := map[string]string{"file_path": path, "table_name": "export_log"}
	if err := writeSQLExport("sqlite", config, testExportMappingsForSQL()); err != nil {
		t.Fatalf("writeSQLExport: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT field_name, value, source FROM export_log ORDER BY field_name`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var field, value, source string
		if err := rows.Scan(&field, &value, &source); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, field+"="+value)
	}
	want := []string{"druck=3.2", "temp=21.4"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteSQLExportAppendsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.db")
	config := map[string]string{"file_path": path, "table_name": "log"}
	if err := writeSQLExport("sqlite", config, testExportMappingsForSQL()[:1]); err != nil {
		t.Fatalf("writeSQLExport (1st run): %v", err)
	}
	if err := writeSQLExport("sqlite", config, testExportMappingsForSQL()[:1]); err != nil {
		t.Fatalf("writeSQLExport (2nd run): %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 accumulated rows across two export runs, got %d", count)
	}
}

func TestWriteSQLExportNoTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.db")
	config := map[string]string{"file_path": path}
	if err := writeSQLExport("sqlite", config, testExportMappingsForSQL()); err == nil {
		t.Fatal("expected error for missing table_name")
	}
}

func TestWriteCSVExportDefaultComma(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := writeCSVExport(path, "", testExportMappingsForSQL()); err != nil {
		t.Fatalf("writeCSVExport: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 data rows, got %d", len(rows))
	}
	if rows[0][0] != "Feld" || rows[0][1] != "Wert" {
		t.Fatalf("unexpected header: %+v", rows[0])
	}
	if rows[1][0] != "temp" || rows[1][1] != "21.4" {
		t.Fatalf("unexpected row: %+v", rows[1])
	}
}

func TestWriteCSVExportSemicolonDelimiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := writeCSVExport(path, ";", testExportMappingsForSQL()); err != nil {
		t.Fatalf("writeCSVExport: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	reader := csv.NewReader(f)
	reader.Comma = ';'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(rows) != 3 || rows[1][0] != "temp" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestWriteCSVExportOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := writeCSVExport(path, "", testExportMappingsForSQL()); err != nil {
		t.Fatalf("writeCSVExport (1st run): %v", err)
	}
	if err := writeCSVExport(path, "", testExportMappingsForSQL()[:1]); err != nil {
		t.Fatalf("writeCSVExport (2nd run): %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected the 2nd run to overwrite (header + 1 row), got %d rows", len(rows))
	}
}

func TestWriteModbusValueInvalidRegisterType(t *testing.T) {
	if err := writeModbusValue(nil, modbusReading{RegisterType: "discrete", DataType: "bool"}, "true"); err == nil {
		t.Fatal("expected error: discrete inputs are not writable")
	}
	if err := writeModbusValue(nil, modbusReading{RegisterType: "input", DataType: "uint16"}, "1"); err == nil {
		t.Fatal("expected error: input registers are not writable")
	}
}

func TestWriteModbusValueInvalidValue(t *testing.T) {
	// Fails on value parsing before ever touching the (nil) client.
	if err := writeModbusValue(nil, modbusReading{RegisterType: "coil"}, "not-a-bool"); err == nil {
		t.Fatal("expected error for invalid bool value")
	}
	if err := writeModbusValue(nil, modbusReading{RegisterType: "holding", DataType: "uint16"}, "not-a-number"); err == nil {
		t.Fatal("expected error for invalid numeric value")
	}
	if err := writeModbusValue(nil, modbusReading{RegisterType: "holding", DataType: "unknown"}, "1"); err == nil {
		t.Fatal("expected error for unknown data type")
	}
}

func TestCoerceOPCUAVariant(t *testing.T) {
	cases := []struct {
		current interface{}
		value   string
		want    interface{}
	}{
		{float64(0), "21.4", float64(21.4)},
		{float32(0), "21.4", float32(21.4)},
		{int32(0), "-5", int32(-5)},
		{uint16(0), "42", uint16(42)},
		{true, "false", false},
		{"placeholder", "hello", "hello"},
	}
	for _, c := range cases {
		currentVariant := ua.MustVariant(c.current)
		got, err := coerceOPCUAVariant(currentVariant, c.value)
		if err != nil {
			t.Errorf("coerceOPCUAVariant(%T, %q): %v", c.current, c.value, err)
			continue
		}
		if got.Value() != c.want {
			t.Errorf("coerceOPCUAVariant(%T, %q) = %v (%T), want %v (%T)", c.current, c.value, got.Value(), got.Value(), c.want, c.want)
		}
	}
}

func TestCoerceOPCUAVariantInvalidValue(t *testing.T) {
	currentVariant := ua.MustVariant(float64(0))
	if _, err := coerceOPCUAVariant(currentVariant, "not-a-number"); err == nil {
		t.Fatal("expected error for non-numeric value against a numeric node")
	}
}
