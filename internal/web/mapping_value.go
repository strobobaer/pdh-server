package web

import "context"

// storeMappingValue hält den zuletzt empfangenen Wert einer Zuordnung fest und
// stößt das Rechenwerk der berechneten Werte an. Alle Konnektoren (MQTT, OPC
// UA, Modbus, SQL, Web, Excel/CSV, gespeicherte Abfragen) schreiben nur
// hierüber – sonst rechnen berechnete Werte nicht nach
// (TestMappingValueSingleWriter).
func (h *Handler) storeMappingValue(ctx context.Context, mappingID, value string) error {
	if _, err := h.db.Exec(ctx, `UPDATE import_mappings SET last_value = $1, last_received_at = NOW() WHERE id = $2::uuid`, value, mappingID); err != nil {
		return err
	}
	h.calcNudge()
	return nil
}
