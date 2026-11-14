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

// Modbus-TCP-Vorschau: anders als die bisherigen Konnektoren gibt es hier
// nichts zu "browsen" - ein Modbus-Geraet liefert keine Feldnamen, nur
// nummerierte Register ohne Metadaten. Stattdessen ein manueller
// Test-Lese-Dialog (Registertyp + Adresse + Datentyp -> sofort live
// lesen), dessen Ergebnis direkt markiert werden kann. Der Quellwert
// einer Zuordnung wird als "registertyp:adresse:datentyp" gespeichert
// (z.B. "holding:100:float32").

var modbusRegisterTypes = []KindOption{
	{"coil", "Coil (1 Bit, lesen/schreiben)"},
	{"discrete", "Discrete Input (1 Bit, nur lesen)"},
	{"holding", "Holding Register (16 Bit, lesen/schreiben)"},
	{"input", "Input Register (16 Bit, nur lesen)"},
}

var modbusDataTypes = []KindOption{
	{"uint16", "UInt16"},
	{"int16", "Int16"},
	{"uint32", "UInt32 (2 Register, big-endian)"},
	{"int32", "Int32 (2 Register, big-endian)"},
	{"float32", "Float32 (2 Register, big-endian)"},
}

// modbusWritableRegisterTypes: nur Coils und Holding Register lassen
// sich per Modbus beschreiben (Discrete Input/Input Register sind laut
// Spezifikation nur lesbar) - fuer den Export-Markieren-Dialog.
var modbusWritableRegisterTypes = []KindOption{
	{"coil", "Coil (1 Bit)"},
	{"holding", "Holding Register (16 Bit)"},
}

type modbusReading struct {
	RegisterType string
	Address      uint16
	DataType     string
}

func parseModbusReading(sourceRef string) (modbusReading, error) {
	parts := strings.SplitN(sourceRef, ":", 3)
	if len(parts) != 3 {
		return modbusReading{}, fmt.Errorf("ungültiger Quellwert")
	}
	addr, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return modbusReading{}, fmt.Errorf("ungültige Adresse")
	}
	return modbusReading{RegisterType: parts[0], Address: uint16(addr), DataType: parts[2]}, nil
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
		return decodeModbusValue(regs, reading.DataType, "abcd")
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

type ModbusPageData struct {
	BaseData
	ConnectionID     string
	ConnectionName   string
	Applicable       bool
	CanWrite         bool
	Host             string
	RegisterTypes    []KindOption
	DataTypes        []KindOption
	TestRegisterType string
	TestAddress      string
	TestDataType     string
	TestResult       string
	TestError        string
	Mappings         []ImportMappingView
	Notice           string
}

func (h *Handler) ModbusReadPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	var name, kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT name, kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&name, &kind, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := ModbusPageData{
		BaseData:       h.baseData(r, "import", "Modbus-Test", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     kind == "modbus",
		CanWrite:       h.canConnectionWrite(r, "import"),
		Host:           strings.TrimSpace(config["host"]) + ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "502"),
		RegisterTypes:  modbusRegisterTypes,
		DataTypes:      modbusDataTypes,
		Notice:         r.URL.Query().Get("notice"),
	}
	if !data.Applicable {
		h.render(w, "modbus_read", data)
		return
	}

	data.TestRegisterType = r.URL.Query().Get("register_type")
	data.TestAddress = r.URL.Query().Get("address")
	data.TestDataType = r.URL.Query().Get("data_type")
	if data.TestRegisterType != "" && data.TestAddress != "" {
		addr, err := strconv.ParseUint(data.TestAddress, 10, 16)
		if err != nil {
			data.TestError = "Ungültige Adresse"
		} else {
			dataType := data.TestDataType
			if data.TestRegisterType == "coil" || data.TestRegisterType == "discrete" {
				dataType = "bool"
			}
			reading := modbusReading{RegisterType: data.TestRegisterType, Address: uint16(addr), DataType: dataType}
			client, err := openModbusClient(config)
			if err != nil {
				data.TestError = err.Error()
			} else {
				value, readErr := readModbusValue(client, reading)
				client.Close()
				if readErr != nil {
					data.TestError = "Lesen fehlgeschlagen: " + readErr.Error()
				} else {
					data.TestResult = value
				}
			}
		}
	}

	if mappings, err := h.importMappings(r.Context(), id); err == nil {
		data.Mappings = mappings
	}
	h.render(w, "modbus_read", data)
}

// ModbusRefreshValuesWeb liest fuer jede bestehende Zuordnung ihren
// Quellwert (Registertyp/Adresse/Datentyp) erneut live vom Geraet -
// scheitert eine einzelne Zuordnung (z.B. Register nicht mehr
// vorhanden), wird sie uebersprungen statt die ganze Aktualisierung
// abzubrechen.
func (h *Handler) ModbusRefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "/read"

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
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}

	client, err := openModbusClient(config)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer client.Close()

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
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
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
