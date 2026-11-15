package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/simonvetter/modbus"
)

// Modbus-TCP: ein Modbus-Geraet liefert keine Feldnamen, nur nummerierte
// Register ohne Metadaten. Erkundet wird im Explorer (explorer_modbus.go);
// hier steht der Quellwert einer Zuordnung und das Lesen/Schreiben davon.
// Der Quellwert wird als "registertyp:adresse:datentyp" gespeichert
// (z.B. "holding:100:float32"), bei Reihenfolge ungleich abcd mit
// ":reihenfolge" (z.B. "holding:100:float32:cdab").

var modbusRegisterTypes = []KindOption{
	{"coil", "Coil (1 Bit, lesen/schreiben)"},
	{"discrete", "Discrete Input (1 Bit, nur lesen)"},
	{"holding", "Holding Register (16 Bit, lesen/schreiben)"},
	{"input", "Input Register (16 Bit, nur lesen)"},
}

var modbusDataTypes = []KindOption{
	{"uint16", "UInt16"},
	{"int16", "Int16"},
	{"uint32", "UInt32 (2 Register)"},
	{"int32", "Int32 (2 Register)"},
	{"float32", "Float32 (2 Register)"},
	{"uint64", "UInt64 (4 Register)"},
	{"int64", "Int64 (4 Register)"},
	{"float64", "Float64 (4 Register)"},
}

// modbusWritableRegisterTypes: nur Coils und Holding Register lassen
// sich per Modbus beschreiben (Discrete Input/Input Register sind laut
// Spezifikation nur lesbar) - fuer den Export-Markieren-Dialog.
var modbusWritableRegisterTypes = []KindOption{
	{"coil", "Coil (1 Bit)"},
	{"holding", "Holding Register (16 Bit)"},
}

// modbusReading ist ein Quellwert „Registertyp:Adresse:Datentyp[:Reihenfolge]“.
// Eine leere Reihenfolge gilt als abcd.
type modbusReading struct {
	RegisterType string
	Address      uint16
	DataType     string
	Order        string
}

// parseModbusReading liest einen Quellwert mit drei Teilen (bisherige Schreibweise,
// Reihenfolge abcd) oder vier Teilen (mit Reihenfolge, Groß-/Kleinschreibung egal).
func parseModbusReading(sourceRef string) (modbusReading, error) {
	parts := strings.Split(sourceRef, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return modbusReading{}, fmt.Errorf("ungültiger Quellwert")
	}
	addr, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return modbusReading{}, fmt.Errorf("ungültige Adresse")
	}
	order := "abcd"
	if len(parts) == 4 {
		order = strings.ToLower(parts[3])
		if !modbusKnownOrder(order) {
			return modbusReading{}, fmt.Errorf("unbekannte Reihenfolge")
		}
	}
	return modbusReading{RegisterType: parts[0], Address: uint16(addr), DataType: parts[2], Order: order}, nil
}

// SourceRef ist die Umkehrung. abcd wird nicht ausgeschrieben, damit bestehende
// Zuordnungen unverändert bleiben.
func (m modbusReading) SourceRef() string {
	s := fmt.Sprintf("%s:%d:%s", m.RegisterType, m.Address, m.DataType)
	if o := m.order(); o != "abcd" {
		s += ":" + o
	}
	return s
}

// order: die Reihenfolge, leer gilt als abcd.
func (m modbusReading) order() string {
	if m.Order == "" {
		return "abcd"
	}
	return m.Order
}

func openModbusClient(config map[string]string) (*modbus.ModbusClient, error) {
	return openModbusClientUnit(config, modbusConfigUnit(config))
}

func readModbusValue(client *modbus.ModbusClient, reading modbusReading) (string, error) {
	switch reading.RegisterType {
	case "coil":
		results, err := client.ReadCoils(reading.Address, 1)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(len(results) > 0 && results[0]), nil
	case "discrete":
		results, err := client.ReadDiscreteInputs(reading.Address, 1)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(len(results) > 0 && results[0]), nil
	case "holding", "input":
		regType := modbus.INPUT_REGISTER
		if reading.RegisterType == "holding" {
			regType = modbus.HOLDING_REGISTER
		}
		regs, err := client.ReadRegisters(reading.Address, modbusRegisterCount(reading.DataType), regType)
		if err != nil {
			return "", err
		}
		return decodeModbusValue(regs, reading.DataType, reading.order())
	default:
		return "", fmt.Errorf("unbekannter Registertyp")
	}
}

// formatModbusRegisters deutet Registerbytes (big-endian) in Standardreihenfolge (ABCD).
func formatModbusRegisters(data []byte, dataType string) (string, error) {
	if !modbusKnownType(dataType) {
		return "", fmt.Errorf("unbekannter Datentyp")
	}
	regs := make([]uint16, len(data)/2)
	for i := range regs {
		regs[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
	}
	if len(regs) < int(modbusRegisterCount(dataType)) {
		return "", fmt.Errorf("zu wenig Daten")
	}
	return decodeModbusValue(regs, dataType, "abcd")
}

// ModbusRefreshValuesWeb liest fuer jede bestehende Zuordnung ihren
// Quellwert (Registertyp/Adresse/Datentyp/Reihenfolge) erneut live vom Geraet -
// scheitert eine einzelne Zuordnung (z.B. Register nicht mehr
// vorhanden), wird sie uebersprungen statt die ganze Aktualisierung
// abzubrechen.
func (h *Handler) ModbusRefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "?tab=mappings&notice="

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || kind != "modbus" {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}

	client, err := openModbusClient(config)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer client.Close()

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	updated := 0
	for _, mp := range mappings {
		if isQueryRef(mp.SourceRef) {
			continue
		}
		reading, err := parseModbusReading(mp.SourceRef)
		if err != nil {
			continue
		}
		value, err := readModbusValue(client, reading)
		if err != nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("modbus-wert konnte nicht gelesen werden")
			continue
		}
		if _, err := h.db.Exec(r.Context(), `
			UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, value, mp.ID); err != nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("modbus-wert konnte nicht gespeichert werden")
			continue
		}
		updated++
	}
	http.Redirect(w, r, base+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
