package web

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeModbusValueOrders(t *testing.T) {
	cases := []struct {
		regs       []uint16
		typ, order string
		want       string
	}{
		{[]uint16{0x1234, 0x5678}, "uint32", "abcd", "305419896"},
		{[]uint16{0x5678, 0x1234}, "uint32", "cdab", "305419896"},
		{[]uint16{0x3412, 0x7856}, "uint32", "badc", "305419896"},
		{[]uint16{0x7856, 0x3412}, "uint32", "dcba", "305419896"},
		{[]uint16{0x42F6, 0xE979}, "float32", "abcd", "123.456"},
		{[]uint16{0xE979, 0x42F6}, "float32", "cdab", "123.456"},
		{[]uint16{0xFFFF}, "int16", "abcd", "-1"},
		{[]uint16{0x1234}, "uint16", "badc", "13330"},
		{[]uint16{0x1234}, "uint16", "cdab", "4660"},
		{[]uint16{0x1234}, "uint16", "dcba", "13330"},
		{[]uint16{0xFFFF, 0xFFFE}, "int32", "abcd", "-2"},
		{[]uint16{0x3FF8, 0, 0, 0}, "float64", "abcd", "1.5"},
		{[]uint16{0, 0, 0, 0x3FF8}, "float64", "cdab", "1.5"},
		{[]uint16{0x0001, 0, 0, 0}, "uint64", "abcd", "281474976710656"},
		{[]uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF}, "int64", "dcba", "-1"},
		{[]uint16{0x7FC0, 0x0000}, "float32", "abcd", "NaN"},
		{[]uint16{0x7F80, 0x0000}, "float32", "abcd", "+Inf"},
		{[]uint16{0xFF80, 0x0000}, "float32", "abcd", "-Inf"},
		{[]uint16{0x0102, 0x0304, 0x0506, 0x0708}, "uint64", "abcd", "72623859790382856"},
		{[]uint16{0x0708, 0x0506, 0x0304, 0x0102}, "uint64", "cdab", "72623859790382856"},
		{[]uint16{0x0201, 0x0403, 0x0605, 0x0807}, "uint64", "badc", "72623859790382856"},
		{[]uint16{0x0807, 0x0605, 0x0403, 0x0201}, "uint64", "dcba", "72623859790382856"},
		{[]uint16{0x1234, 0x5678, 0x9999}, "uint32", "abcd", "305419896"}, // Überhang bleibt unbeachtet
	}
	for _, c := range cases {
		got, err := decodeModbusValue(c.regs, c.typ, c.order)
		if err != nil || got != c.want {
			t.Errorf("%v %s %s = %q, %v; erwartet %q", c.regs, c.typ, c.order, got, err, c.want)
		}
	}
	for _, bad := range []struct {
		regs       []uint16
		typ, order string
	}{{[]uint16{1}, "float32", "abcd"}, {[]uint16{1}, "int8", "abcd"}, {[]uint16{1, 2}, "uint32", "xyzw"}, {nil, "uint16", "abcd"}, {[]uint16{1, 2, 3}, "float64", "abcd"}} {
		if _, err := decodeModbusValue(bad.regs, bad.typ, bad.order); err == nil {
			t.Errorf("kein Fehler bei %+v", bad)
		}
	}
}

func TestDecodeModbusValueKeepsInput(t *testing.T) {
	regs := []uint16{0x1234, 0x5678}
	if _, err := decodeModbusValue(regs, "uint32", "dcba"); err != nil {
		t.Fatal(err)
	}
	if regs[0] != 0x1234 || regs[1] != 0x5678 {
		t.Errorf("Eingabe verändert: %v", regs)
	}
}

func TestEncodeModbusRoundTrip(t *testing.T) {
	signed := map[string]bool{"int16": true, "int32": true, "int64": true, "float32": true, "float64": true}
	for _, typ := range modbusCodecTypes {
		for _, o := range modbusOrders {
			vals := []string{"0", "1", "42"}
			if signed[typ] {
				vals = append(vals, "-7")
			}
			if typ == "float32" || typ == "float64" {
				vals = append(vals, "21.5")
			}
			for _, v := range vals {
				regs, err := encodeModbusValue(v, typ, o.Value)
				if err != nil {
					t.Errorf("encode %s %s %s: %v", v, typ, o.Value, err)
					continue
				}
				if len(regs) != int(modbusRegisterCount(typ)) {
					t.Errorf("%s: %d Register", typ, len(regs))
				}
				got, err := decodeModbusValue(regs, typ, o.Value)
				if err != nil || got != v {
					t.Errorf("Rundlauf %s %s %s = %q, %v", v, typ, o.Value, got, err)
				}
			}
		}
	}
	regs, err := encodeModbusValue("123.456", "float32", "cdab")
	if err != nil || !reflect.DeepEqual(regs, []uint16{0xE979, 0x42F6}) {
		t.Errorf("123.456 cdab = %#v, %v", regs, err)
	}
}

func TestEncodeModbusRoundTripLimits(t *testing.T) {
	cases := []struct{ v, typ string }{
		{"65535", "uint16"}, {"-32768", "int16"}, {"32767", "int16"},
		{"4294967295", "uint32"}, {"-2147483648", "int32"}, {"2147483647", "int32"},
		{"18446744073709551615", "uint64"}, {"-9223372036854775808", "int64"}, {"9223372036854775807", "int64"},
		{"1e+300", "float64"},
	}
	for _, c := range cases {
		for _, o := range modbusOrders {
			regs, err := encodeModbusValue(c.v, c.typ, o.Value)
			if err != nil {
				t.Errorf("%s %s: %v", c.v, c.typ, err)
				continue
			}
			got, err := decodeModbusValue(regs, c.typ, o.Value)
			if err != nil {
				t.Errorf("decode %s: %v", c.typ, err)
				continue
			}
			if c.typ != "float64" && got != c.v {
				t.Errorf("%s %s %s: %q", c.v, c.typ, o.Value, got)
			}
		}
	}
}

func TestEncodeModbusValueComma(t *testing.T) {
	a, err := encodeModbusValue("21,5", "float32", "abcd")
	b, err2 := encodeModbusValue("21.5", "float32", "abcd")
	if err != nil || err2 != nil || !reflect.DeepEqual(a, b) {
		t.Errorf("Komma: %v %v / %v %v", a, err, b, err2)
	}
	for _, c := range []struct{ v, typ string }{
		{"21,5", "int16"}, {"70000", "uint16"}, {"abc", "float32"}, {"", "uint16"}, {"-1", "uint32"},
		{"32768", "int16"}, {"-32769", "int16"}, {"4294967296", "uint32"}, {"1,5", "uint64"},
		{"1e40", "float32"}, {"1e999", "float64"}, {"NaN", "float32"}, {"Inf", "float64"}, {"-Inf", "float32"},
		{"+Infinity", "float64"}, {"1.2.3", "float32"},
	} {
		if regs, err := encodeModbusValue(c.v, c.typ, "abcd"); err == nil {
			t.Errorf("%q als %s ergab %v statt Fehler", c.v, c.typ, regs)
		}
	}
	if _, err := encodeModbusValue("1", "int8", "abcd"); err == nil {
		t.Error("unbekannter Typ")
	}
	if _, err := encodeModbusValue("1", "uint32", "xyzw"); err == nil {
		t.Error("unbekannte Reihenfolge")
	}
}

func TestModbusRegisterRows(t *testing.T) {
	rows := modbusRegisterRows("holding", []uint16{0x42F6, 0xE979}, 100)
	if len(rows) != 2 {
		t.Fatalf("%d Zeilen", len(rows))
	}
	r0 := rows[0]
	if r0.Address != 100 || r0.Hex != "42F6" {
		t.Fatalf("Zeile 0: %+v", r0)
	}
	has := func(r ModbusRegisterRow, want ModbusInterp) bool {
		for _, in := range r.Interps {
			if in == want {
				return true
			}
		}
		return false
	}
	for _, want := range []ModbusInterp{
		{"float32", "abcd", "123.456", "holding:100:float32"},
		{"uint16", "abcd", "17142", "holding:100:uint16"},
	} {
		if !has(r0, want) {
			t.Errorf("fehlt: %+v", want)
		}
	}
	var cdab bool
	for _, in := range r0.Interps {
		if in.Type == "float32" && in.Order == "cdab" {
			cdab = in.Ref == "holding:100:float32:cdab"
		}
		if in.Type == "uint16" && (in.Order == "cdab" || in.Order == "dcba") {
			t.Errorf("16 Bit nur abcd/badc: %+v", in)
		}
		if strings.HasSuffix(in.Type, "64") {
			t.Errorf("64 Bit passt nicht in 2 Register: %+v", in)
		}
	}
	if !cdab {
		t.Error("float32 cdab fehlt")
	}
	for _, in := range rows[1].Interps {
		if in.Type != "uint16" && in.Type != "int16" {
			t.Errorf("Zeile 1 nur 16 Bit: %+v", in)
		}
	}
	if rows[1].Address != 101 {
		t.Errorf("Adresse %d", rows[1].Address)
	}
	// Alle Typen bei vier Registern
	four := modbusRegisterRows("input", make([]uint16, 4), 65532)
	if n := len(four[0].Interps); n != 28 { // 2 Typen×2 + 3×4 + 3×4
		t.Errorf("Deutungen Zeile 0: %d", n)
	}
	if four[3].Address != 65535 {
		t.Errorf("letzte Adresse %d", four[3].Address)
	}
}

func TestFormatModbusRegistersUnchanged(t *testing.T) {
	got, err := formatModbusRegisters([]byte{0x42, 0xF6, 0xE9, 0x79}, "float32")
	if err != nil || got != "123.456" {
		t.Errorf("float32 = %q, %v", got, err)
	}
}
