package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/simonvetter/modbus"
)

// Modbus-TCP-Explorer: Registerbereiche eines Geräts lesen, jedes Register in allen
// Datentypen und Byte-/Wortreihenfolgen deuten, live verfolgen und testweise schreiben.
// Rahmen (Rechte, Sitzungsgrenze, Rückfrage, Verlauf) steht in explorer.go, die Umrechnung
// in modbus_codec.go. Das Gerät wird geschont: höchstens zwei gleichzeitige Zugriffe je
// Verbindung (opcuaConnGate), Leseblöcke höchstens in Modbus-Größe, Mindesttakt 1 s.

const (
	modbusExplorerMaxRegs   = 125 // Register je Leseauftrag (Modbus-Grenze)
	modbusExplorerMaxBits   = 2000
	modbusExplorerMaxWrite  = 100 // Zeichen eines zu schreibenden Werts
	modbusExplorerOpTimeout = 20 * time.Second
	modbusExplorerMinTick   = 1 // Sekunden
	modbusExplorerMaxTick   = 60
	modbusExplorerDefTick   = 2
	modbusExplorerDefCount  = 10
)

// ── Verbindung ───────────────────────────────────────────────

// modbusConfigUnit: Einheit-ID der Verbindung (Vorgabe 1).
func modbusConfigUnit(config map[string]string) byte {
	u, _ := strconv.Atoi(strings.TrimSpace(config["unit_id"]))
	if u <= 0 || u > 255 {
		return 1
	}
	return byte(u)
}

// openModbusClientUnit öffnet eine TCP-Verbindung zum Gerät (Zeitgrenze 5 s) für die Einheit unit.
func openModbusClientUnit(config map[string]string, unit byte) (*modbus.ModbusClient, error) {
	host := strings.Trim(strings.TrimSpace(config["host"]), "[]")
	if host == "" {
		return nil, fmt.Errorf("kein Host konfiguriert")
	}
	port := firstNonEmpty(strings.TrimSpace(config["port"]), "502")
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     "tcp://" + net.JoinHostPort(host, port),
		Timeout: 5 * time.Second,
		Logger:  log.New(io.Discard, "", 0), // sonst schreibt die Bibliothek nach stdout
	})
	if err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	if err := client.SetUnitId(unit); err != nil {
		return nil, err
	}
	if err := client.Open(); err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return client, nil
}

// modbusDeviceError: Meldung des Geräts bzw. der Verbindung in Klartext.
func modbusDeviceError(err error) string {
	switch {
	case errors.Is(err, modbus.ErrIllegalDataAddress):
		return "Das Gerät kennt diese Adresse nicht (Illegal Data Address)"
	case errors.Is(err, modbus.ErrIllegalFunction):
		return "Das Gerät unterstützt diese Funktion nicht (Illegal Function)"
	case errors.Is(err, modbus.ErrIllegalDataValue):
		return "Das Gerät lehnt den Wert ab (Illegal Data Value)"
	case errors.Is(err, modbus.ErrServerDeviceBusy):
		return "Das Gerät ist beschäftigt (Server Device Busy)"
	case errors.Is(err, modbus.ErrServerDeviceFailure):
		return "Das Gerät meldet einen Fehler (Server Device Failure)"
	case errors.Is(err, modbus.ErrRequestTimedOut):
		return "Das Gerät antwortet nicht (Zeitüberschreitung)"
	}
	return opcuaCleanText(err.Error(), 200)
}

// modbusIsException: das Gerät hat geantwortet (Ausnahmecode); die Verbindung ist in Ordnung.
func modbusIsException(err error) bool {
	for _, e := range []modbus.Error{
		modbus.ErrIllegalFunction, modbus.ErrIllegalDataAddress, modbus.ErrIllegalDataValue,
		modbus.ErrServerDeviceFailure, modbus.ErrAcknowledge, modbus.ErrServerDeviceBusy,
		modbus.ErrMemoryParityError, modbus.ErrGWPathUnavailable, modbus.ErrGWTargetFailedToRespond,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// ── Bereiche lesen ───────────────────────────────────────────

type modbusRange struct {
	Area         string
	Start, Count uint16
	Unit         byte
}

func modbusAreaIsBits(area string) bool { return area == "coil" || area == "discrete" }

// parseModbusRange liest area, start, count und unit aus der Adresse; Fehler sind Eingabefehler.
func parseModbusRange(q url.Values, defaultUnit byte) (modbusRange, error) {
	rg := modbusRange{Area: strings.TrimSpace(q.Get("area")), Unit: defaultUnit}
	if rg.Area == "" {
		rg.Area = "holding"
	}
	switch rg.Area {
	case "coil", "discrete", "holding", "input":
	default:
		return rg, errors.New("Ungültiger Bereich")
	}
	start := 0
	if s := strings.TrimSpace(q.Get("start")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 65535 {
			return rg, errors.New("Ungültige Startadresse (0 bis 65535)")
		}
		start = n
	}
	limit := modbusExplorerMaxRegs
	if modbusAreaIsBits(rg.Area) {
		limit = modbusExplorerMaxBits
	}
	count := modbusExplorerDefCount
	if s := strings.TrimSpace(q.Get("count")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > limit {
			return rg, fmt.Errorf("Ungültige Anzahl (1 bis %d)", limit)
		}
		count = n
	}
	if start+count > 65536 {
		return rg, errors.New("Der Bereich reicht über die Adresse 65535 hinaus")
	}
	if s := strings.TrimSpace(q.Get("unit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 255 {
			return rg, errors.New("Ungültige Einheit-ID (0 bis 255)")
		}
		rg.Unit = byte(n)
	}
	rg.Start, rg.Count = uint16(start), uint16(count)
	return rg, nil
}

// ModbusBit ist ein gelesener Coil bzw. Discrete Input.
type ModbusBit struct {
	Address uint16 `json:"address"`
	On      bool   `json:"on"`
	Ref     string `json:"ref"`
}

// ModbusRangeResult ist das Ergebnis eines Bereichslesens.
type ModbusRangeResult struct {
	Area  string              `json:"area"`
	Start uint16              `json:"start"`
	Rows  []ModbusRegisterRow `json:"rows,omitempty"`
	Bits  []ModbusBit         `json:"bits,omitempty"`
}

// readModbusRange liest den Bereich in Blöcken (Register höchstens 125, Bits höchstens 2000 je Auftrag).
func readModbusRange(c *modbus.ModbusClient, rg modbusRange) (ModbusRangeResult, error) {
	res := ModbusRangeResult{Area: rg.Area, Start: rg.Start}
	fail := func(err error) (ModbusRangeResult, error) {
		last := uint32(rg.Start) + uint32(rg.Count) - 1
		return ModbusRangeResult{}, fmt.Errorf("Lesen von %s %d–%d fehlgeschlagen: %s", rg.Area, rg.Start, last, modbusDeviceError(err))
	}
	bits := modbusAreaIsBits(rg.Area)
	block := uint32(modbusExplorerMaxRegs)
	if bits {
		block = modbusExplorerMaxBits
	}
	var regs []uint16
	for off := uint32(0); off < uint32(rg.Count); off += block {
		n := uint32(rg.Count) - off
		if n > block {
			n = block
		}
		addr := uint16(uint32(rg.Start) + off)
		switch rg.Area {
		case "coil", "discrete":
			var vals []bool
			var err error
			if rg.Area == "coil" {
				vals, err = c.ReadCoils(addr, uint16(n))
			} else {
				vals, err = c.ReadDiscreteInputs(addr, uint16(n))
			}
			if err != nil {
				return fail(err)
			}
			for i, v := range vals {
				a := uint16(uint32(addr) + uint32(i))
				res.Bits = append(res.Bits, ModbusBit{Address: a, On: v, Ref: fmt.Sprintf("%s:%d:bool", rg.Area, a)})
			}
		default:
			rt := modbus.INPUT_REGISTER
			if rg.Area == "holding" {
				rt = modbus.HOLDING_REGISTER
			}
			vals, err := c.ReadRegisters(addr, uint16(n), rt)
			if err != nil {
				return fail(err)
			}
			regs = append(regs, vals...)
		}
	}
	if !bits {
		res.Rows = modbusRegisterRows(rg.Area, regs, rg.Start)
	}
	return res, nil
}

// ── Schreiben ────────────────────────────────────────────────

type modbusWriteInput struct {
	Area               string
	Address            uint16
	Type, Order, Value string
	Unit               byte
}

// parseModbusBool: true/false/1/0 (ohne Rücksicht auf Groß-/Kleinschreibung).
func parseModbusBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, errors.New("Ein Coil nimmt nur true, false, 1 oder 0 an")
}

// writeModbusExplorer schreibt einen Coil oder ein Holding-Register (bzw. mehrere Register).
// Werte, die vor dem Senden abgelehnt werden, kommen als *opcuaRejected zurück.
func writeModbusExplorer(c *modbus.ModbusClient, in modbusWriteInput) error {
	switch in.Area {
	case "coil":
		b, err := parseModbusBool(in.Value)
		if err != nil {
			return &opcuaRejected{err.Error()}
		}
		if err := c.SetUnitId(in.Unit); err != nil {
			return err
		}
		return c.WriteCoil(in.Address, b)
	case "holding":
		regs, err := encodeModbusValue(in.Value, in.Type, in.Order)
		if err != nil {
			return &opcuaRejected{err.Error()}
		}
		if uint32(in.Address)+uint32(len(regs)) > 65536 {
			return &opcuaRejected{"Der Wert reicht über die Adresse 65535 hinaus"}
		}
		if err := c.SetUnitId(in.Unit); err != nil {
			return err
		}
		if len(regs) == 1 {
			return c.WriteRegister(in.Address, regs[0])
		}
		return c.WriteRegisters(in.Address, regs)
	}
	return &opcuaRejected{"Nur Coils und Holding Register sind beschreibbar"}
}

// modbusReadBack liest die eben geschriebene Stelle zurück (best effort, "" bei Fehler).
func modbusReadBack(c *modbus.ModbusClient, in modbusWriteInput) string {
	if in.Area == "coil" {
		v, err := c.ReadCoils(in.Address, 1)
		if err != nil || len(v) != 1 {
			return ""
		}
		return strconv.FormatBool(v[0])
	}
	regs, err := c.ReadRegisters(in.Address, modbusRegisterCount(in.Type), modbus.HOLDING_REGISTER)
	if err != nil {
		return ""
	}
	v, _ := decodeModbusValue(regs, in.Type, in.Order)
	return v
}

// ── Handler ──────────────────────────────────────────────────

// modbusReadGuard: Rechte, Verbindung und Typ (404 JSON, wenn keine Modbus-Verbindung).
func (h *Handler) modbusReadGuard(w http.ResponseWriter, r *http.Request, direction string) (explorerConn, bool) {
	c, ok := h.explorerReadGuard(w, r, direction)
	if !ok {
		return c, false
	}
	if c.Kind != "modbus" {
		writeGlobalBoardError(w, http.StatusNotFound, "Keine Modbus-Verbindung")
		return c, false
	}
	return c, true
}

// ModbusExplorerRange: GET …/explorer/modbus/range?area&start&count&unit – ein Bereich einmal lesen.
func (h *Handler) ModbusExplorerRange(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.modbusReadGuard(w, r, direction)
		if !ok {
			return
		}
		rg, err := parseModbusRange(r.URL.Query(), modbusConfigUnit(c.Config))
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
			return
		}
		release, ok := opcuaAcquire(w, c)
		if !ok {
			return
		}
		defer release()
		cl, err := openModbusClientUnit(c.Config, rg.Unit)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, opcuaCleanText(err.Error(), 300))
			return
		}
		defer cl.Close()
		res, err := readModbusRange(cl, rg)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, opcuaCleanText(err.Error(), 300))
			return
		}
		writeOPCUAJSON(w, res)
	}
}

// ModbusExplorerWatch: GET …/explorer/modbus/watch?area&start&count&unit&interval= – den Bereich
// im Takt (Sekunden, 1 bis 60, Vorgabe 2) lesen; SSE „range“. Lesefehler kommen als „warn“,
// die Sitzung läuft weiter.
func (h *Handler) ModbusExplorerWatch(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.modbusReadGuard(w, r, direction)
		if !ok {
			return
		}
		rg, err := parseModbusRange(r.URL.Query(), modbusConfigUnit(c.Config))
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
			return
		}
		tick := modbusExplorerDefTick
		if s := strings.TrimSpace(r.URL.Query().Get("interval")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiger Takt")
				return
			}
			tick = min(max(n, modbusExplorerMinTick), modbusExplorerMaxTick)
		}
		release, ok := opcuaAcquire(w, c)
		if !ok {
			return
		}
		defer release()
		h.runExplorerStream(w, r, func(ctx context.Context, send func(string, any)) error {
			var cl *modbus.ModbusClient
			defer func() {
				if cl != nil {
					cl.Close()
				}
			}()
			send("connected", map[string]any{"interval": tick, "area": rg.Area, "start": rg.Start, "count": rg.Count, "unit": rg.Unit})
			t := time.NewTicker(time.Duration(tick) * time.Second)
			defer t.Stop()
			for {
				if cl == nil {
					var err error
					if cl, err = openModbusClientUnit(c.Config, rg.Unit); err != nil {
						cl = nil
						if ctx.Err() != nil {
							return nil
						}
						send("warn", map[string]string{"message": opcuaCleanText(err.Error(), 300)})
					}
				}
				if cl != nil {
					res, err := readModbusRange(cl, rg)
					switch {
					case ctx.Err() != nil:
						return nil
					case err != nil:
						send("warn", map[string]string{"message": opcuaCleanText(err.Error(), 300)})
						if !modbusIsException(err) { // Verbindung gestört: beim nächsten Takt neu aufbauen
							cl.Close()
							cl = nil
						}
					default:
						send("range", res)
					}
				}
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
				}
			}
		})
	}
}

// ModbusExplorerWrite: POST …/explorer/modbus/write (area, address, type, order, value, unit, confirm)
// – testweise schreiben. Jeder Versuch steht im Verlauf der Verbindung, auch ein Fehlschlag.
func (h *Handler) ModbusExplorerWrite(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		c, ok := h.explorerWriteGuard(w, r, direction)
		if !ok {
			return
		}
		if c.Kind != "modbus" {
			writeGlobalBoardError(w, http.StatusNotFound, "Keine Modbus-Verbindung")
			return
		}
		in := modbusWriteInput{
			Area:  strings.TrimSpace(r.FormValue("area")),
			Type:  strings.TrimSpace(r.FormValue("type")),
			Order: strings.TrimSpace(r.FormValue("order")),
			Value: r.FormValue("value"),
			Unit:  modbusConfigUnit(c.Config),
		}
		if in.Area != "coil" && in.Area != "holding" {
			writeGlobalBoardError(w, http.StatusBadRequest, "Nur Coils und Holding Register sind beschreibbar")
			return
		}
		addr, err := strconv.Atoi(strings.TrimSpace(r.FormValue("address")))
		if err != nil || addr < 0 || addr > 65535 {
			writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Adresse (0 bis 65535)")
			return
		}
		in.Address = uint16(addr)
		if s := strings.TrimSpace(r.FormValue("unit")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > 255 {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Einheit-ID (0 bis 255)")
				return
			}
			in.Unit = byte(n)
		}
		if in.Area == "coil" {
			in.Type, in.Order = "bool", ""
		} else {
			if in.Order == "" {
				in.Order = "abcd"
			}
			if !modbusKnownType(in.Type) {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiger Datentyp")
				return
			}
			if !modbusKnownOrder(in.Order) {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Reihenfolge")
				return
			}
		}
		if utf8.RuneCountInString(in.Value) > modbusExplorerMaxWrite {
			writeGlobalBoardError(w, http.StatusBadRequest, "Der Wert ist zu lang")
			return
		}
		release, ok := opcuaAcquire(w, c)
		if !ok {
			return
		}
		defer release()
		readBack := ""
		writeErr := func() error {
			// Vor dem Verbinden prüfen: Wert und Bereich sind ohne Gerät beurteilbar.
			if in.Area == "holding" {
				if _, err := encodeModbusValue(in.Value, in.Type, in.Order); err != nil {
					return &opcuaRejected{err.Error()}
				}
			} else if _, err := parseModbusBool(in.Value); err != nil {
				return &opcuaRejected{err.Error()}
			}
			cl, err := openModbusClientUnit(c.Config, in.Unit)
			if err != nil {
				return err
			}
			defer cl.Close()
			if err := writeModbusExplorer(cl, in); err != nil {
				return err
			}
			readBack = modbusReadBack(cl, in)
			return nil
		}()
		target := fmt.Sprintf("%s (Einheit %d)", modbusSourceRef(in.Area, in.Address, in.Type, in.Order), in.Unit)
		logged := opcuaCleanText(in.Value, opcuaExplorerMaxValue)
		if in.Area == "holding" {
			logged += " (" + in.Type + ", " + strings.ToUpper(in.Order) + ")"
		}
		var rejected *opcuaRejected
		isRejected := errors.As(writeErr, &rejected)
		if writeErr != nil {
			reason := opcuaCleanText(modbusDeviceError(writeErr), 80)
			if isRejected {
				logged = "Abgelehnt (" + reason + ") – " + logged
			} else {
				logged = "Fehlgeschlagen (" + reason + ") – " + logged
			}
		}
		h.explorerLogWrite(r, c, target, logged)
		if writeErr != nil {
			status := http.StatusBadGateway
			if isRejected {
				status = http.StatusBadRequest
			}
			writeGlobalBoardError(w, status, opcuaCleanText(modbusDeviceError(writeErr), 300))
			return
		}
		writeOPCUAJSON(w, map[string]any{"ok": true, "value": readBack, "target": target})
	}
}
