package web

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/goburrow/modbus"
	"github.com/rs/zerolog/log"
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

func modbusRegisterCount(dataType string) uint16 {
	switch dataType {
	case "uint32", "int32", "float32":
		return 2
	default:
		return 1
	}
}

func openModbusClient(config map[string]string) (*modbus.TCPClientHandler, modbus.Client, error) {
	host := strings.TrimSpace(config["host"])
	if host == "" {
		return nil, nil, fmt.Errorf("kein Host konfiguriert")
	}
	port := firstNonEmpty(strings.TrimSpace(config["port"]), "502")
	unitID, _ := strconv.Atoi(strings.TrimSpace(config["unit_id"]))
	if unitID <= 0 || unitID > 255 {
		unitID = 1
	}
	handler := modbus.NewTCPClientHandler(host + ":" + port)
	handler.Timeout = 5 * time.Second
	handler.SlaveId = byte(unitID)
	if err := handler.Connect(); err != nil {
		return nil, nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return handler, modbus.NewClient(handler), nil
}

func readModbusValue(client modbus.Client, reading modbusReading) (string, error) {
	switch reading.RegisterType {
	case "coil":
		results, err := client.ReadCoils(reading.Address, 1)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(len(results) > 0 && results[0]&0x01 == 1), nil
	case "discrete":
		results, err := client.ReadDiscreteInputs(reading.Address, 1)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(len(results) > 0 && results[0]&0x01 == 1), nil
	case "holding", "input":
		count := modbusRegisterCount(reading.DataType)
		var results []byte
		var err error
		if reading.RegisterType == "holding" {
			results, err = client.ReadHoldingRegisters(reading.Address, count)
		} else {
			results, err = client.ReadInputRegisters(reading.Address, count)
		}
		if err != nil {
			return "", err
		}
		return formatModbusRegisters(results, reading.DataType)
	default:
		return "", fmt.Errorf("unbekannter Registertyp")
	}
}

func formatModbusRegisters(data []byte, dataType string) (string, error) {
	switch dataType {
	case "uint16":
		if len(data) < 2 {
			return "", fmt.Errorf("zu wenig Daten")
		}
		return strconv.FormatUint(uint64(binary.BigEndian.Uint16(data)), 10), nil
	case "int16":
		if len(data) < 2 {
			return "", fmt.Errorf("zu wenig Daten")
		}
		return strconv.FormatInt(int64(int16(binary.BigEndian.Uint16(data))), 10), nil
	case "uint32":
		if len(data) < 4 {
			return "", fmt.Errorf("zu wenig Daten")
		}
		return strconv.FormatUint(uint64(binary.BigEndian.Uint32(data)), 10), nil
	case "int32":
		if len(data) < 4 {
			return "", fmt.Errorf("zu wenig Daten")
		}
		return strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(data))), 10), nil
	case "float32":
		if len(data) < 4 {
			return "", fmt.Errorf("zu wenig Daten")
		}
		return strconv.FormatFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(data))), 'f', -1, 32), nil
	default:
		return "", fmt.Errorf("unbekannter Datentyp")
	}
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
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

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
			handler, client, err := openModbusClient(config)
			if err != nil {
				data.TestError = err.Error()
			} else {
				value, readErr := readModbusValue(client, reading)
				handler.Close()
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
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	handler, client, err := openModbusClient(config)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer handler.Close()

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	updated := 0
	for _, mp := range mappings {
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
