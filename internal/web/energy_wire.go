package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"

	"pdh/internal/modules/energie"
)

// Energiedaten – Anbindung an den Rest des PDH. Die Seite steht in
// energy_meters.go, gespeichert wird über energie.Store
// (internal/modules/energie).

// meterRef: Datensatz-Typ einer Messstelle (Rechte auf Abteilungsebene,
// Bereichsadministrator, Abteilungsfilter).
const meterRef = "meter"

// Rechte des Bereichs (Migration 161).
const (
	permEnergyView  = "energy.view"
	permEnergyEdit  = "energy.edit"
	permEnergyAdmin = "energy.admin"
)

// energyStore: Speicher der Messstellen; ohne Datenbank (Tests) nil.
func (h *Handler) energyStore() *energie.Store {
	if h.db == nil {
		return nil
	}
	return energie.NewStore(h.db)
}

// StartEnergySampler startet den Minuten-Takt der Messstellen (nur am aktiven
// Server; ruht in der Wartungssperre).
func (h *Handler) StartEnergySampler(ctx context.Context) {
	st := h.energyStore()
	if st == nil {
		log.Warn().Msg("energiedaten: minutenwerte nicht gestartet (keine datenbank)")
		return
	}
	sm := &energie.Sampler{Store: st, Skip: h.MaintLocked}
	go sm.Start(ctx)
}

// meterFilterFromQuery liest Filter und Sortierung aus der Adresse
// (infra, virtual, export, active, unit, kind, conn, q, stale, sort, dir).
// Ja/Nein-Felder: "1" = ja, "0" = nein, sonst nicht filtern. Ungültige IDs und
// unbekannte Arten werden ignoriert, eine unbekannte Sortierung gilt als "key".
func meterFilterFromQuery(q url.Values) energie.Filter {
	tri := func(k string) *bool {
		switch q.Get(k) {
		case "1":
			v := true
			return &v
		case "0":
			v := false
			return &v
		}
		return nil
	}
	f := energie.Filter{
		Virtual: tri("virtual"), Export: tri("export"), Active: tri("active"),
		Unit:  strings.TrimSpace(q.Get("unit")),
		Query: strings.TrimSpace(q.Get("q")),
		Stale: q.Get("stale") == "1",
		Sort:  q.Get("sort"),
		Desc:  q.Get("dir") == "desc",
	}
	if id := q.Get("infra"); meterUUIDRe.MatchString(id) {
		f.InfraID = id
	}
	if id := q.Get("conn"); meterUUIDRe.MatchString(id) {
		f.ConnectionID = id
	}
	switch k := q.Get("kind"); k {
	case energie.KindCounter, energie.KindConsumption, energie.KindInstant:
		f.Kind = k
	}
	switch f.Sort {
	case "key", "name", "infra", "unit", "kind", "last":
	default:
		f.Sort = "key"
	}
	return f
}

// meterKindLabel: Beschriftung der Art einer Messstelle.
func meterKindLabel(k string) string {
	switch k {
	case energie.KindCounter:
		return "Zählerstand"
	case energie.KindConsumption:
		return "Verbrauch"
	case energie.KindInstant:
		return "Momentanwert"
	}
	return k
}

// meterSaveMessage: Fehlermeldung in Anwendersprache für Speichern und Löschen.
// Unbekannte Fehler bleiben im Protokoll, die Person bekommt einen allgemeinen Satz.
func meterSaveMessage(err error) string {
	var inUse *energie.InUseError
	var pe *pgconn.PgError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &inUse):
		return "Nicht möglich: Die Messstelle " + inUse.Error() + ". Bitte zuerst dort die Formel ändern."
	case errors.As(err, &pe) && pe.Code == "23503":
		return "Die gewählte Quelle oder Anlage gibt es nicht mehr. Bitte die Seite neu laden."
	case meterUserError(err):
		return upperFirst(err.Error())
	}
	componentLog("energiedaten").Error().Err(err).Msg("messstelle speichern")
	return "Die Messstelle konnte nicht gespeichert werden."
}

// meterUserError: Fehler, deren Text für die Person gedacht ist.
func meterUserError(err error) bool {
	var fe *energie.FormulaError
	var ue *energie.UnknownRefError
	var ce *energie.CycleError
	return errors.As(err, &fe) || errors.As(err, &ue) || errors.As(err, &ce) ||
		errors.Is(err, energie.ErrBadKey) || errors.Is(err, energie.ErrKeyTaken) || errors.Is(err, energie.ErrBadKind) ||
		errors.Is(err, energie.ErrKindMix) || errors.Is(err, energie.ErrNoInfra) || errors.Is(err, energie.ErrNotFound)
}

// meterSaveStatus: HTTP-Status zum Fehler von meterSaveMessage.
func meterSaveStatus(err error) int {
	var inUse *energie.InUseError
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, energie.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, energie.ErrKeyTaken), errors.As(err, &inUse):
		return http.StatusConflict
	case errors.As(err, &pe) && pe.Code == "23503", meterUserError(err):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || unicode.IsUpper(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
