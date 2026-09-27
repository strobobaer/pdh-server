package web

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestParseModbusReading(t *testing.T) {
	r, err := parseModbusReading("holding:100:float32")
	if err != nil {
		t.Fatalf("parseModbusReading: %v", err)
	}
	if r.RegisterType != "holding" || r.Address != 100 || r.DataType != "float32" {
		t.Fatalf("unexpected reading: %+v", r)
	}
}

func TestParseModbusReadingInvalid(t *testing.T) {
	if _, err := parseModbusReading("not-a-valid-source-ref"); err == nil {
		t.Fatal("expected error for malformed source_ref")
	}
	if _, err := parseModbusReading("holding:notanumber:float32"); err == nil {
		t.Fatal("expected error for non-numeric address")
	}
}

func TestModbusRegisterCount(t *testing.T) {
	cases := map[string]uint16{"uint16": 1, "int16": 1, "uint32": 2, "int32": 2, "float32": 2}
	for dataType, want := range cases {
		if got := modbusRegisterCount(dataType); got != want {
			t.Errorf("modbusRegisterCount(%q) = %d, want %d", dataType, got, want)
		}
	}
}

func TestFormatModbusRegisters(t *testing.T) {
	float32Bytes := make([]byte, 4)
	binary.BigEndian.PutUint32(float32Bytes, math.Float32bits(21.4))

	cases := []struct {
		dataType string
		data     []byte
		want     string
	}{
		{"uint16", []byte{0x00, 0x2A}, "42"},
		{"int16", []byte{0xFF, 0xFF}, "-1"},
		{"uint32", []byte{0x00, 0x00, 0x00, 0x2A}, "42"},
		{"int32", []byte{0xFF, 0xFF, 0xFF, 0xFF}, "-1"},
		{"float32", float32Bytes, "21.4"},
	}
	for _, c := range cases {
		got, err := formatModbusRegisters(c.data, c.dataType)
		if err != nil {
			t.Errorf("formatModbusRegisters(%q): %v", c.dataType, err)
			continue
		}
		if got != c.want {
			t.Errorf("formatModbusRegisters(%q) = %q, want %q", c.dataType, got, c.want)
		}
	}
}

func TestFormatModbusRegistersTooFewBytes(t *testing.T) {
	if _, err := formatModbusRegisters([]byte{0x00}, "uint16"); err == nil {
		t.Fatal("expected error for insufficient data")
	}
	if _, err := formatModbusRegisters([]byte{0x00, 0x00}, "float32"); err == nil {
		t.Fatal("expected error for insufficient data")
	}
}

func TestOpenModbusClientNoHost(t *testing.T) {
	if _, _, err := openModbusClient(map[string]string{}); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestOpenModbusClientUnreachable(t *testing.T) {
	_, _, err := openModbusClient(map[string]string{"host": "127.0.0.1", "port": "1"})
	if err == nil {
		t.Fatal("expected error for unreachable modbus host")
	}
}
