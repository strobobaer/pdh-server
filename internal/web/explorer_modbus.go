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
	"syscall"
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
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed), errors.Is(err, syscall.ECONNRESET):
		return "Das Gerät hat die Verbindung beendet"
	case isTimeout(err):
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
	Ref     string `json:"ref,omitempty"`
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
				res.Bits = append(res.Bits, ModbusBit{Address: a, On: v, Ref: modbusReading{RegisterType: rg.Area, Address: a, DataType: "bool"}.SourceRef()})
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

// writeModbusExplorer schreibt (auf der Einheit, mit der c geöffnet wurde) einen Coil oder ein Holding-Register (bzw. mehrere Register).
// Werte, die vor dem Senden abgelehnt werden, kommen als *opcuaRejected zurück.
func writeModbusExplorer(c *modbus.ModbusClient, in modbusWriteInput) error {
	switch in.Area {
	case "coil":
		b, err := parseModbusBool(in.Value)
		if err != nil {
			return &opcuaRejected{err.Error()}
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

// modbusEffOrder: bei einem Register sind CDAB und DCBA gleich ABCD bzw. BADC.
func modbusEffOrder(typ, order string) string {
	if modbusRegisterCount(typ) == 1 {
		switch order {
		case "cdab":
			return "abcd"
		case "dcba":
			return "badc"
		}
	}
	return order
}

// modbusOrderParam liest „order“: leer = abcd, „all“ = alle Reihenfolgen (nur für Einzelzeilen).
func modbusOrderParam(q url.Values) (string, error) {
	o := strings.TrimSpace(q.Get("order"))
	switch {
	case o == "":
		return "abcd", nil
	case o == "all" || modbusKnownOrder(o):
		return o, nil
	}
	return "", errors.New("Ungültige Reihenfolge")
}

// modbusCompact verkleinert das Ergebnis für die Übertragung: je Zeile nur die Deutungen
// der Reihenfolge (order „all“: alle) und ohne Quellwert – den baut der Browser selbst
// (Bereich:Adresse:Typ[:Reihenfolge], wie modbusReading.SourceRef).
func modbusCompact(res ModbusRangeResult, order string) ModbusRangeResult {
	if res.Rows != nil {
		rows := make([]ModbusRegisterRow, len(res.Rows))
		for i, r := range res.Rows {
			nr := ModbusRegisterRow{Address: r.Address, Hex: r.Hex}
			for _, in := range r.Interps {
				if order == "all" || in.Order == modbusEffOrder(in.Type, order) {
					in.Ref = ""
					nr.Interps = append(nr.Interps, in)
				}
			}
			rows[i] = nr
		}
		res.Rows = rows
	}
	for i := range res.Bits {
		res.Bits[i].Ref = ""
	}
	return res
}

// modbusRangeKey: Fingerabdruck der gelesenen Rohwerte (zum Erkennen von Änderungen).
func modbusRangeKey(res ModbusRangeResult) string {
	var b strings.Builder
	for _, r := range res.Rows {
		b.WriteString(r.Hex)
		b.WriteByte(' ')
	}
	for _, bit := range res.Bits {
		if bit.On {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// modbusAliveEvery: so oft kommt beim Verfolgen ohne Änderung ein Lebenszeichen (Variable für Tests).
var modbusAliveEvery = 10 * time.Second

// ModbusExplorerRange: GET …/explorer/modbus/range?area&start&count&unit&order – ein Bereich einmal lesen.
// order: Reihenfolge der Deutungen (Vorgabe abcd); „all“ liefert alle (für die Matrix einer Zeile).
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
		order, err := modbusOrderParam(r.URL.Query())
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
		writeOPCUAJSON(w, modbusCompact(res, order))
	}
}

// ModbusExplorerWatch: GET …/explorer/modbus/watch?area&start&count&unit&order&interval= – den Bereich
// im Takt (Sekunden, 1 bis 60, Vorgabe 2) lesen; SSE „range“ nur bei geänderten Werten, sonst
// alle 10 s „alive“. Lesefehler kommen als „warn“, die Sitzung läuft weiter.
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
		order, err := modbusOrderParam(r.URL.Query())
		if err != nil || order == "all" {
			writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Reihenfolge")
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
			send("connected", map[string]any{"interval": tick, "area": rg.Area, "start": rg.Start, "count": rg.Count, "unit": rg.Unit, "order": order})
			t := time.NewTicker(time.Duration(tick) * time.Second)
			defer t.Stop()
			lastKey, lastSent, havePrev := "", time.Now(), false
			for {
				if cl == nil {
					var err error
					if cl, err = openModbusClientUnit(c.Config, rg.Unit); err != nil {
						cl = nil
						if ctx.Err() != nil {
							return nil
						}
						havePrev = false
						send("warn", map[string]string{"message": opcuaCleanText(err.Error(), 300)})
					}
				}
				if cl != nil {
					res, err := readModbusRange(cl, rg)
					switch {
					case ctx.Err() != nil:
						return nil
					case err != nil:
						havePrev = false
						send("warn", map[string]string{"message": opcuaCleanText(err.Error(), 300)})
						if !modbusIsException(err) { // Verbindung gestört: beim nächsten Takt neu aufbauen
							cl.Close()
							cl = nil
						}
					default:
						if key := modbusRangeKey(res); !havePrev || key != lastKey {
							send("range", modbusCompact(res, order))
							lastKey, lastSent, havePrev = key, time.Now(), true
						} else if time.Since(lastSent) >= modbusAliveEvery {
							send("alive", map[string]any{})
							lastSent = time.Now()
						}
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

// parseModbusWrite prüft die Felder eines Schreibwunsches; Fehler sind Ablehnungen.
func parseModbusWrite(r *http.Request, defaultUnit byte) (modbusWriteInput, error) {
	in := modbusWriteInput{
		Area:  strings.TrimSpace(r.FormValue("area")),
		Type:  strings.TrimSpace(r.FormValue("type")),
		Order: strings.TrimSpace(r.FormValue("order")),
		Value: r.FormValue("value"),
		Unit:  defaultUnit,
	}
	if in.Area != "coil" && in.Area != "holding" {
		return in, errors.New("Nur Coils und Holding Register sind beschreibbar")
	}
	addr, err := strconv.Atoi(strings.TrimSpace(r.FormValue("address")))
	if err != nil || addr < 0 || addr > 65535 {
		return in, errors.New("Ungültige Adresse (0 bis 65535)")
	}
	in.Address = uint16(addr)
	if s := strings.TrimSpace(r.FormValue("unit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 255 {
			return in, errors.New("Ungültige Einheit-ID (0 bis 255)")
		}
		in.Unit = byte(n)
	}
	// Geschrieben wird nur mit der Einheit der Verbindung (wie beim Übernehmen); Rundruf nie.
	if in.Unit == 0 {
		return in, errors.New("Einheit 0 (Rundruf) ist beim Schreiben gesperrt")
	}
	if in.Unit != defaultUnit {
		return in, fmt.Errorf("Einheit %d statt der Einheit %d der Verbindung", in.Unit, defaultUnit)
	}
	if utf8.RuneCountInString(in.Value) > modbusExplorerMaxWrite {
		return in, errors.New("Der Wert ist zu lang")
	}
	if in.Area == "coil" {
		in.Type, in.Order = "bool", ""
		if _, err := parseModbusBool(in.Value); err != nil {
			return in, err
		}
		return in, nil
	}
	if in.Order == "" {
		in.Order = "abcd"
	}
	if !modbusKnownType(in.Type) {
		return in, errors.New("Ungültiger Datentyp")
	}
	if !modbusKnownOrder(in.Order) {
		return in, errors.New("Ungültige Reihenfolge")
	}
	if regs, err := encodeModbusValue(in.Value, in.Type, in.Order); err != nil {
		return in, err
	} else if uint32(in.Address)+uint32(len(regs)) > 65536 {
		return in, errors.New("Der Wert reicht über die Adresse 65535 hinaus")
	}
	return in, nil
}

// ModbusExplorerWrite: POST …/explorer/modbus/write (area, address, type, order, value, unit, confirm)
// – testweise schreiben. Hat der Guard die Rechte bejaht, steht jeder Versuch im Verlauf der
// Verbindung: Ablehnung („Abgelehnt (…)“), Fehlschlag und Erfolg. Nur „Gerät beschäftigt“ (429)
// nicht, denn dann wurde nichts versucht.
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
		in, perr := parseModbusWrite(r, modbusConfigUnit(c.Config))
		// Ziel und Wert für den Verlauf, auch wenn die Eingabe unbrauchbar ist.
		target := fmt.Sprintf("%s (Einheit %d)", modbusReading{in.Area, in.Address, in.Type, in.Order}.SourceRef(), in.Unit)
		logged := opcuaCleanText(in.Value, opcuaExplorerMaxValue)
		if perr != nil { // unbrauchbare Eingabe: so festhalten, wie sie ankam
			target = strings.TrimRight(strings.Join([]string{r.FormValue("area"), r.FormValue("address"), r.FormValue("type"), r.FormValue("order")}, ":"), ":")
			target = opcuaCleanText(target, 120)
			if u := strings.TrimSpace(r.FormValue("unit")); u != "" {
				target += " (Einheit " + opcuaCleanText(u, 10) + ")"
			}
			if t := opcuaCleanText(strings.TrimSpace(r.FormValue("type")), 20); t != "" {
				logged += " (" + t + ")"
			}
		} else if in.Area == "holding" {
			logged += " (" + in.Type + ", " + strings.ToUpper(in.Order) + ")"
		}
		reject := func(err error) {
			h.explorerLogWrite(r, c, target, "Abgelehnt ("+opcuaCleanText(err.Error(), 80)+") – "+logged)
			writeGlobalBoardError(w, http.StatusBadRequest, opcuaCleanText(err.Error(), 300))
		}
		if perr != nil {
			reject(perr)
			return
		}
		release, ok := opcuaAcquire(w, c)
		if !ok {
			return
		}
		defer release()
		readBack := ""
		writeErr := func() error {
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
		var rejected *opcuaRejected
		if errors.As(writeErr, &rejected) {
			reject(writeErr)
			return
		}
		if writeErr != nil {
			h.explorerLogWrite(r, c, target, "Fehlgeschlagen ("+opcuaCleanText(modbusDeviceError(writeErr), 80)+") – "+logged)
			writeGlobalBoardError(w, http.StatusBadGateway, opcuaCleanText(modbusDeviceError(writeErr), 300))
			return
		}
		h.explorerLogWrite(r, c, target, logged)
		writeOPCUAJSON(w, map[string]any{"ok": true, "value": readBack, "target": target})
	}
}
