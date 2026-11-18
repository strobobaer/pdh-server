package web

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Modbus-Codec: Register in Werte und Werte in Register umrechnen. Rein, ohne Netz.
// Geräte legen 32- und 64-Bit-Werte unterschiedlich in Register ab; vier Reihenfolgen
// (ABCD, CDAB, BADC, DCBA) decken die üblichen Fälle ab.

// modbusOrders: Byte- und Wortreihenfolgen (A = höchstwertiges Byte).
var modbusOrders = []KindOption{
	{"abcd", "ABCD – Standard"},
	{"cdab", "CDAB – Wörter getauscht (z. B. WAGO)"},
	{"badc", "BADC – Bytes getauscht"},
	{"dcba", "DCBA – little-endian"},
}

// modbusCodecTypes: alle Datentypen, die der Codec deutet.
var modbusCodecTypes = []string{"uint16", "int16", "uint32", "int32", "float32", "uint64", "int64", "float64"}

// modbusRegisterCount: Registeranzahl eines Datentyps (16 Bit 1, 32 Bit 2, 64 Bit 4, sonst 1).
func modbusRegisterCount(dataType string) uint16 {
	switch dataType {
	case "uint32", "int32", "float32":
		return 2
	case "uint64", "int64", "float64":
		return 4
	default:
		return 1
	}
}

func modbusKnownType(dataType string) bool {
	for _, t := range modbusCodecTypes {
		if t == dataType {
			return true
		}
	}
	return false
}

func modbusKnownOrder(order string) bool {
	for _, o := range modbusOrders {
		if o.Value == order {
			return true
		}
	}
	return false
}

// modbusReorder wendet die Reihenfolge auf Register an. Beide Schritte (Wörter umkehren,
// Bytepaare tauschen) sind selbstinvers, dieselbe Umformung gilt also zum Dekodieren
// und zum Kodieren. Ergebnis ist eine Kopie.
func modbusReorder(regs []uint16, order string) []uint16 {
	r := append([]uint16(nil), regs...)
	if order == "cdab" || order == "dcba" {
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
	}
	if order == "badc" || order == "dcba" {
		for i, v := range r {
			r[i] = v<<8 | v>>8
		}
	}
	return r
}

// decodeModbusValue deutet die ersten Register als Wert des Datentyps. Ganzzahlen
// erscheinen dezimal, Gleitkommazahlen kürzest möglich (NaN, +Inf, -Inf als Text).
func decodeModbusValue(regs []uint16, dataType, order string) (string, error) {
	if !modbusKnownType(dataType) {
		return "", errors.New("unbekannter Datentyp")
	}
	if !modbusKnownOrder(order) {
		return "", errors.New("unbekannte Reihenfolge")
	}
	n := int(modbusRegisterCount(dataType))
	if len(regs) < n {
		return "", errors.New("zu wenig Register")
	}
	r := modbusReorder(regs[:n], order)
	b := make([]byte, 0, 8)
	for _, v := range r {
		b = binary.BigEndian.AppendUint16(b, v)
	}
	switch dataType {
	case "uint16":
		return strconv.FormatUint(uint64(binary.BigEndian.Uint16(b)), 10), nil
	case "int16":
		return strconv.FormatInt(int64(int16(binary.BigEndian.Uint16(b))), 10), nil
	case "uint32":
		return strconv.FormatUint(uint64(binary.BigEndian.Uint32(b)), 10), nil
	case "int32":
		return strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(b))), 10), nil
	case "float32":
		return strconv.FormatFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(b))), 'f', -1, 32), nil
	case "uint64":
		return strconv.FormatUint(binary.BigEndian.Uint64(b), 10), nil
	case "int64":
		return strconv.FormatInt(int64(binary.BigEndian.Uint64(b)), 10), nil
	default: // float64
		return strconv.FormatFloat(math.Float64frombits(binary.BigEndian.Uint64(b)), 'f', -1, 64), nil
	}
}

// encodeModbusValue wandelt einen Wert (auch deutsch „21,5“) in Register. Wert außerhalb
// des Wertebereichs, nicht endliche Zahlen und Kommazahlen bei Ganzzahltypen sind Fehler.
func encodeModbusValue(value, dataType, order string) ([]uint16, error) {
	if !modbusKnownType(dataType) {
		return nil, errors.New("unbekannter Datentyp")
	}
	if !modbusKnownOrder(order) {
		return nil, errors.New("unbekannte Reihenfolge")
	}
	value = explorerDecimal(value)
	var u uint64
	switch dataType {
	case "uint16", "uint32", "uint64":
		bits := int(modbusRegisterCount(dataType)) * 16
		v, err := strconv.ParseUint(value, 10, bits)
		if err != nil {
			return nil, modbusValueErr(err, dataType)
		}
		u = v
	case "int16", "int32", "int64":
		bits := int(modbusRegisterCount(dataType)) * 16
		v, err := strconv.ParseInt(value, 10, bits)
		if err != nil {
			return nil, modbusValueErr(err, dataType)
		}
		u = uint64(v)
	case "float32", "float64":
		bits := 32
		if dataType == "float64" {
			bits = 64
		}
		f, err := strconv.ParseFloat(value, bits)
		if err != nil {
			return nil, modbusValueErr(err, dataType)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("nicht endliche Zahlen (NaN, Inf) werden nicht geschrieben")
		}
		if bits == 32 {
			u = uint64(math.Float32bits(float32(f)))
		} else {
			u = math.Float64bits(f)
		}
	}
	n := int(modbusRegisterCount(dataType))
	regs := make([]uint16, n)
	for i := 0; i < n; i++ { // höchstwertiges Register zuerst
		regs[i] = uint16(u >> (16 * uint(n-1-i)))
	}
	return modbusReorder(regs, order), nil
}

func modbusValueErr(err error, dataType string) error {
	if errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("Wert liegt außerhalb des Wertebereichs von %s", dataType)
	}
	return fmt.Errorf("Wert ist keine gültige Zahl für %s", dataType)
}

// ModbusInterp ist eine Deutung von Registern als Wert.
type ModbusInterp struct {
	Type  string `json:"type"`
	Order string `json:"order"`
	Value string `json:"value"`
	Ref   string `json:"ref,omitempty"`
}

// ModbusRegisterRow ist ein Register mit allen Deutungen, die ab dort noch passen.
type ModbusRegisterRow struct {
	Address uint16         `json:"address"`
	Hex     string         `json:"hex"`
	Interps []ModbusInterp `json:"interps"`
}

// modbusRegisterRows deutet jedes Register mit allen passenden Typen und Reihenfolgen.
// Bei 16 Bit gibt es nur abcd und badc (cdab/dcba wären gleich).
func modbusRegisterRows(area string, regs []uint16, start uint16) []ModbusRegisterRow {
	rows := make([]ModbusRegisterRow, 0, len(regs))
	for i := range regs {
		addr := uint16(uint32(start) + uint32(i))
		row := ModbusRegisterRow{Address: addr, Hex: strings.ToUpper(fmt.Sprintf("%04x", regs[i]))}
		for _, typ := range modbusCodecTypes {
			n := int(modbusRegisterCount(typ))
			if i+n > len(regs) {
				continue
			}
			for _, o := range modbusOrders {
				if n == 1 && (o.Value == "cdab" || o.Value == "dcba") {
					continue
				}
				v, err := decodeModbusValue(regs[i:i+n], typ, o.Value)
				if err != nil {
					continue
				}
				row.Interps = append(row.Interps, ModbusInterp{Type: typ, Order: o.Value, Value: v, Ref: modbusReading{area, addr, typ, o.Value}.SourceRef()})
			}
		}
		rows = append(rows, row)
	}
	return rows
}
