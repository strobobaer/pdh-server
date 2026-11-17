package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/integrations/snmp"
)

// SNMP v3 – Oberfläche (0.80.22). Speicher in snmp_store.go, Abgleich in it_net_snmp.go,
// Vorlagen in web/templates/widgets/snmp.gohtml.
//
// Reiter „SNMP“ der Server-Einstellungen (Zugangsprofile):
//
//	POST /admin/server-config/snmp/profiles              Profil anlegen (system.snmp)
//	POST /admin/server-config/snmp/profiles/{id}         Profil ändern (system.snmp; leeres Kennwort = unverändert)
//	POST /admin/server-config/snmp/profiles/{id}/delete  Profil löschen (nur Bereichsadministrator: system.snmp_admin)
//
// Karte „SNMP-Geräte“ der Seite /it/net (Geräteliste):
//
//	POST /it/net/snmp/targets               Gerät hinzufügen (it.netsync)
//	POST /it/net/snmp/targets/{id}          Adresse, Port, Profil ändern (it.netsync)
//	POST /it/net/snmp/targets/{id}/enabled  Abfrage ein-/ausschalten (it.netsync; on=1|0)
//	POST /it/net/snmp/targets/{id}/check    „Prüfen“: Gerät sofort fragen, nichts speichern (it.netsync)
//	POST /it/net/snmp/targets/{id}/remove   aus der Liste entfernen (nur Bereichsadministrator IT: it.admin)
//
// Kennwörter stehen nie in einer Antwort, einer Meldung oder im Protokoll.

// permSNMPAdmin: Bereichsadministrator der SNMP-Zugangsprofile – als Einziger darf er ein
// Profil endgültig löschen (areaAdminPerm, gateRules). Das Recht allein genügt für den Reiter.
const permSNMPAdmin = "system.snmp_admin"

const (
	snmpCheckTimeout = 25 * time.Second
	// snmpNeutralErr: Meldung bei einem Programm- oder Datenbankfehler (Näheres nur im Protokoll).
	snmpNeutralErr = "Das hat nicht geklappt. Näheres steht im Server-Protokoll."
)

var errSNMPBlocked = snmpInputErr("Am Spiegelserver und während der Wartung lassen sich die SNMP-Zugangsprofile nicht ändern")

// snmpMayDelete: Bereichsadministrator der SNMP-Zugangsprofile?
func (h *Handler) snmpMayDelete(r *http.Request) bool {
	if h.hasPermHook != nil { // nur in Tests
		return h.hasPermHook(r, permSNMPAdmin)
	}
	if h.rbac == nil {
		return false
	}
	return h.isAreaAdmin(r, "snmp", "")
}

// snmpCanManage: Reiter sehen, Profile anlegen und ändern – mit system.snmp oder als
// Bereichsadministrator (system.snmp_admin allein genügt).
func (h *Handler) snmpCanManage(r *http.Request) bool {
	if h.rbac == nil && h.hasPermHook == nil {
		return false
	}
	return h.hasPerm(r, permSNMP) || h.snmpMayDelete(r)
}

// snmpErrText: Klartext für die Oberfläche. Unbekannte Fehler werden nur protokolliert.
func snmpErrText(err error, what string) string {
	if text, ok := snmpUserText(err); ok {
		return text
	}
	componentLog("snmp").Error().Err(err).Str("aktion", what).Msg("snmp")
	return snmpNeutralErr
}

// ── Reiter „SNMP“ ────────────────────────────────────────────

// snmpProtoOpt: ein Verfahren in der Auswahl.
type snmpProtoOpt struct {
	Key, Name string
	Old       bool // veraltet (SHA-1)
}

// snmpProfileForm: Formularwerte eines Profils – ohne Kennwörter.
type snmpProfileForm struct {
	Name, User, Auth, Priv string
}

// snmpProfileRow: eine Zeile der Profilliste.
type snmpProfileRow struct {
	SNMPProfile
	AuthName, PrivName string
	AuthOld            bool            // Anmelde-Verfahren gilt als veraltet
	Form               snmpProfileForm // Werte des Formulars „Ändern“
	Open               bool            // Formular „Ändern“ aufgeklappt (nach einem Fehler)
}

// snmpTab: Reiter „SNMP“ (widgets/snmp.gohtml). Kennwörter sind nie darin.
type snmpTab struct {
	Can        bool // system.snmp oder Bereichsadministrator
	CanDelete  bool // Bereichsadministrator (system.snmp_admin)
	Msg, Err   string
	Profiles   []snmpProfileRow
	New        snmpProfileForm // Formular „Neues Profil“
	AuthProtos []snmpProtoOpt
	PrivProtos []snmpProtoOpt
}

func snmpProtoOpts(list []snmp.Proto) []snmpProtoOpt {
	out := make([]snmpProtoOpt, 0, len(list))
	for _, p := range list {
		out = append(out, snmpProtoOpt{Key: p.Key, Name: p.Name, Old: snmp.Deprecated(p.Key)})
	}
	return out
}

// snmpTabFor liest nur die Datenbank – es geht keine Anfrage an ein Gerät.
func (h *Handler) snmpTabFor(ctx context.Context, r *http.Request) snmpTab {
	t := snmpTab{Can: h.snmpCanManage(r)}
	if !t.Can {
		return t
	}
	t.CanDelete = h.snmpMayDelete(r)
	t.AuthProtos, t.PrivProtos = snmpProtoOpts(snmp.AuthProtos()), snmpProtoOpts(snmp.PrivProtos())
	t.New = snmpProfileForm{Auth: snmp.DefaultAuthProto, Priv: snmp.DefaultPrivProto}
	if h.db == nil {
		return t
	}
	list, err := h.snmpProfiles(ctx)
	if err != nil {
		t.Err = snmpErrText(err, "profile lesen")
		return t
	}
	for _, p := range list {
		t.Profiles = append(t.Profiles, snmpProfileRow{SNMPProfile: p,
			AuthName: snmp.ProtoName(p.AuthProto), PrivName: snmp.ProtoName(p.PrivProto), AuthOld: snmp.Deprecated(p.AuthProto),
			Form: snmpProfileForm{Name: p.Name, User: p.User, Auth: p.AuthProto, Priv: p.PrivProto}})
	}
	return t
}

func (h *Handler) snmpRender(w http.ResponseWriter, t snmpTab) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tm, err := h.tmpl.Clone()
	if err == nil {
		err = tm.ExecuteTemplate(w, "snmp-panel", t)
	}
	if err != nil {
		fmt.Fprintf(w, `<div id="snmp-panel" class="sc-sub" style="color:var(--red)">SNMP: %s</div>`, esc(err.Error()))
	}
}

func snmpProfileInput(r *http.Request) SNMPProfileInput {
	return SNMPProfileInput{Name: r.FormValue("name"), User: r.FormValue("username"),
		AuthProto: r.FormValue("auth_proto"), AuthPass: r.FormValue("auth_pass"),
		PrivProto: r.FormValue("priv_proto"), PrivPass: r.FormValue("priv_pass")}
}

// snmpProfileAct: Recht prüfen, Aktion ausführen, protokollieren, melden, Reiter-Karte neu
// zeichnen. Nach einem Fehler behält das betroffene Formular die Eingaben – ohne Kennwörter.
func (h *Handler) snmpProfileAct(w http.ResponseWriter, r *http.Request, allowed bool, what, id string,
	do func(ctx context.Context) (string, error)) {
	if !allowed {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var msg string
	var err error
	if h.Mirror().On || h.MaintLocked() {
		err = errSNMPBlocked
	} else {
		msg, err = do(ctx)
	}
	uid := getUser(r).ID
	lg := componentLog("snmp")
	errText := ""
	if err != nil {
		errText = snmpErrText(err, what)
		lg.Warn().Str("von", uid).Str("aktion", what).Str("fehler", errText).Msg("snmp")
	} else {
		lg.Info().Str("von", uid).Str("aktion", what).Str("meldung", msg).Msg("snmp")
		if h.itNet.snmpNotify != nil {
			h.itNet.snmpNotify(ctx, msg)
		} else if h.db != nil {
			h.notifyAdmins(ctx, "📡 **SNMP-Zugangsprofile geändert** von "+h.chatUserName(ctx, uid)+": "+msg)
		}
	}
	t := h.snmpTabFor(ctx, r)
	t.Msg = msg
	if err != nil {
		t.Err = errText
		in := snmpProfileInput(r)
		form := snmpProfileForm{Name: strings.TrimSpace(in.Name), User: strings.TrimSpace(in.User), Auth: in.AuthProto, Priv: in.PrivProto}
		if id == "" && what == "profil-anlegen" {
			t.New = form
		}
		for i := range t.Profiles {
			if t.Profiles[i].ID == id && what == "profil-aendern" {
				t.Profiles[i].Form, t.Profiles[i].Open = form, true
			}
		}
	}
	h.snmpRender(w, t)
}

// SNMPProfileCreateWeb: POST /admin/server-config/snmp/profiles
// (name, username, auth_proto, auth_pass, priv_proto, priv_pass)
func (h *Handler) SNMPProfileCreateWeb(w http.ResponseWriter, r *http.Request) {
	h.snmpProfileAct(w, r, h.snmpCanManage(r), "profil-anlegen", "", func(ctx context.Context) (string, error) {
		in := snmpProfileInput(r)
		if _, err := h.snmpProfileCreate(ctx, in); err != nil {
			return "", err
		}
		return "Zugangsprofil „" + strings.TrimSpace(in.Name) + "“ angelegt. Die Kennwörter sind verschlüsselt gespeichert.", nil
	})
}

// SNMPProfileUpdateWeb: POST /admin/server-config/snmp/profiles/{id} – leere Kennwörter bleiben unverändert.
func (h *Handler) SNMPProfileUpdateWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.snmpProfileAct(w, r, h.snmpCanManage(r), "profil-aendern", id, func(ctx context.Context) (string, error) {
		in := snmpProfileInput(r)
		if err := h.snmpProfileUpdate(ctx, id, in); err != nil {
			return "", err
		}
		msg := "Zugangsprofil „" + strings.TrimSpace(in.Name) + "“ gespeichert."
		switch {
		case in.AuthPass != "" && in.PrivPass != "":
			msg += " Beide Kennwörter neu hinterlegt."
		case in.AuthPass != "":
			msg += " Kennwort der Anmeldung neu hinterlegt."
		case in.PrivPass != "":
			msg += " Kennwort der Verschlüsselung neu hinterlegt."
		default:
			msg += " Die Kennwörter bleiben unverändert."
		}
		return msg, nil
	})
}

// SNMPProfileDeleteWeb: POST /admin/server-config/snmp/profiles/{id}/delete – endgültig
// löschen darf nur der Bereichsadministrator; nur, solange kein Gerät das Profil benutzt.
func (h *Handler) SNMPProfileDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.snmpProfileAct(w, r, h.snmpMayDelete(r), "profil-loeschen", id, func(ctx context.Context) (string, error) {
		name := id
		used := 0
		if list, err := h.snmpProfiles(ctx); err == nil {
			for _, p := range list {
				if p.ID == id {
					name, used = p.Name, p.Targets
				}
			}
		}
		err := h.snmpProfileDelete(ctx, id)
		if errors.Is(err, errSNMPProfileInUse) {
			return "", snmpProfileInUseErr(name, used)
		}
		if err != nil {
			return "", err
		}
		return "Zugangsprofil „" + name + "“ gelöscht.", nil
	})
}

// snmpProfileInUseErr: verständliche Meldung, warum sich ein Profil nicht löschen lässt.
func snmpProfileInUseErr(name string, used int) error {
	n := "noch von Geräten"
	switch {
	case used == 1:
		n = "noch von 1 Gerät"
	case used > 1:
		n = fmt.Sprintf("noch von %d Geräten", used)
	}
	return snmpInputErr("Das Profil „" + name + "“ wird " + n + " genutzt und bleibt deshalb bestehen. Gib den Geräten unter IT-Assets → Netz-Abgleich erst ein anderes Profil oder entferne sie aus der Liste.")
}

// ── Karte „SNMP-Geräte“ auf /it/net ──────────────────────────

// ITNetSNMPRow: eine Zeile der Geräteliste.
type ITNetSNMPRow struct {
	SNMPTarget
	StateLabel, StateClass string
	LastOKText             string
	Open                   bool // Formular „Bearbeiten“ aufgeklappt (nach einem Fehler)
	// CanRemove: it.admin – und hat der Eintrag ein Asset, it.admin für genau dieses Asset
	// (Abteilung), so wie „Quelle trennen“ am Asset je Datensatz prüft.
	CanRemove bool
}

// ITNetSNMPCard: Karte „SNMP-Geräte“ (Vorlage itn-snmp in widgets/snmp.gohtml).
type ITNetSNMPCard struct {
	CanSetup    bool // it.netsync
	CanRemove   bool // it.admin – aus der Liste entfernen (je Zeile: ITNetSNMPRow.CanRemove)
	CanProfiles bool // darf den Reiter „SNMP“ öffnen
	Profiles    []itNetOption
	Rows        []ITNetSNMPRow
	Max         int
	Msg, Err    string
	// Eingaben des Formulars „Gerät hinzufügen“ nach einem Fehler
	NewHost, NewPort, NewProfile string
}

// itNetSNMPState: Abzeichen einer Zeile der Geräteliste.
func itNetSNMPState(t SNMPTarget) (string, string) {
	switch {
	case !t.Enabled:
		return "aus", "b-gray"
	case t.DetachedAssetID != "":
		return "Asset getrennt", "b-gray"
	case t.AssetID == "":
		return "noch kein Asset", "b-gray"
	}
	return itNetStateBadge(t.NetState)
}

// itNetSNMPCard baut die Karte aus der Datenbank – es geht keine Anfrage an ein Gerät.
func (h *Handler) itNetSNMPCard(r *http.Request) ITNetSNMPCard {
	ctx := r.Context()
	c := ITNetSNMPCard{CanSetup: h.itNetCan(r, permITNetsync, ""), CanRemove: h.itNetCan(r, itNetPermAdmin, ""),
		CanProfiles: h.itNetCan(r, permSNMP, "") || h.itNetCan(r, permSNMPAdmin, ""), Max: snmpMaxTargets, NewPort: strconv.Itoa(snmp.DefaultPort)}
	if h.db == nil {
		return c
	}
	profiles, err := h.snmpProfiles(ctx)
	if err != nil {
		c.Err = snmpErrText(err, "profile lesen")
		return c
	}
	for _, p := range profiles {
		c.Profiles = append(c.Profiles, itNetOption{p.ID, p.Name})
	}
	targets, err := h.snmpTargets(ctx)
	if err != nil {
		c.Err = snmpErrText(err, "geräteliste lesen")
		return c
	}
	for _, t := range targets {
		row := ITNetSNMPRow{SNMPTarget: t, CanRemove: c.CanRemove && h.snmpMayRemove(r, t.anyAssetID())}
		row.StateLabel, row.StateClass = itNetSNMPState(t)
		if t.LastOK != nil {
			row.LastOKText = alarmTime(*t.LastOK)
		}
		c.Rows = append(c.Rows, row)
	}
	return c
}

func (h *Handler) itNetSNMPRender(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tm, err := h.tmpl.Clone()
	if err == nil {
		err = tm.ExecuteTemplate(w, name, data)
	}
	if err != nil {
		itNetLog().Error().Err(err).Str("vorlage", name).Msg("snmp-karte zeichnen")
		fmt.Fprint(w, `<div class="itn-note" role="alert" style="color:var(--red)">Die Anzeige ist fehlgeschlagen. Bitte die Seite neu laden.</div>`)
	}
}

// snmpFormPort: Port aus dem Formular (leer → 0 = Standard 161).
func snmpFormPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, snmpInputErr("Port: bitte eine Zahl von 1 bis 65535 eingeben (üblich: 161)")
	}
	return n, nil
}

// itNetSNMPAct: Recht prüfen, Aktion ausführen, protokollieren, Karte neu zeichnen.
// fail richtet die Karte nach einem Fehler her (Eingaben behalten).
func (h *Handler) itNetSNMPAct(w http.ResponseWriter, r *http.Request, perm, what string,
	do func(ctx context.Context) (string, error), fail func(c *ITNetSNMPCard)) {
	if !h.itNetCan(r, perm, "") {
		itNetForbidden(w)
		return
	}
	msg, err := do(r.Context())
	uid := getUser(r).ID
	errText := ""
	if err != nil {
		errText = snmpErrText(err, what)
		itNetLog().Warn().Str("von", uid).Str("aktion", what).Str("fehler", errText).Msg("snmp-geräteliste")
	} else {
		itNetLog().Info().Str("von", uid).Str("aktion", what).Str("meldung", msg).Msg("snmp-geräteliste")
	}
	c := h.itNetSNMPCard(r)
	c.Msg = msg
	if err != nil {
		c.Err = errText
		if fail != nil {
			fail(&c)
		}
	}
	h.itNetSNMPRender(w, "itn-snmp", c)
}

// itNetSNMPID: Geräte-ID aus dem Pfad; ok = false → 404 ist schon geschrieben.
func itNetSNMPID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !itNetUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return "", false
	}
	return id, true
}

// ITNetSNMPTargetCreateWeb: POST /it/net/snmp/targets (host, port, profile).
func (h *Handler) ITNetSNMPTargetCreateWeb(w http.ResponseWriter, r *http.Request) {
	host, rawPort, profile := strings.TrimSpace(r.FormValue("host")), r.FormValue("port"), r.FormValue("profile")
	h.itNetSNMPAct(w, r, permITNetsync, "gerät-hinzufügen", func(ctx context.Context) (string, error) {
		port, err := snmpFormPort(rawPort)
		if err != nil {
			return "", err
		}
		if _, err := h.snmpTargetCreate(ctx, host, port, profile, getUser(r).ID); err != nil {
			return "", err
		}
		return "Gerät " + host + " hinzugefügt. Mit „Prüfen“ siehst du sofort, ob es antwortet; ein IT-Asset entsteht beim nächsten Abgleich.", nil
	}, func(c *ITNetSNMPCard) {
		c.NewHost, c.NewPort, c.NewProfile = host, strings.TrimSpace(rawPort), profile
	})
}

// ITNetSNMPTargetUpdateWeb: POST /it/net/snmp/targets/{id} (host, port, profile).
func (h *Handler) ITNetSNMPTargetUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") { // vor der ID-Prüfung: ohne Recht keine Auskunft
		itNetForbidden(w)
		return
	}
	id, ok := itNetSNMPID(w, r)
	if !ok {
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	h.itNetSNMPAct(w, r, permITNetsync, "gerät-ändern", func(ctx context.Context) (string, error) {
		port, err := snmpFormPort(r.FormValue("port"))
		if err != nil {
			return "", err
		}
		if err := h.snmpTargetUpdate(ctx, id, host, port, r.FormValue("profile")); err != nil {
			return "", err
		}
		return "Gerät " + host + " gespeichert. Das IT-Asset bleibt dasselbe.", nil
	}, func(c *ITNetSNMPCard) {
		for i := range c.Rows {
			c.Rows[i].Open = c.Rows[i].ID == id
		}
	})
}

// ITNetSNMPTargetEnabledWeb: POST /it/net/snmp/targets/{id}/enabled (on=1|0).
func (h *Handler) ITNetSNMPTargetEnabledWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	id, ok := itNetSNMPID(w, r)
	if !ok {
		return
	}
	on := r.FormValue("on") == "1"
	h.itNetSNMPAct(w, r, permITNetsync, "gerät-ein-aus", func(ctx context.Context) (string, error) {
		if err := h.snmpTargetSetEnabled(ctx, id, on); err != nil {
			return "", err
		}
		if on {
			return "Abfrage eingeschaltet. Das Gerät wird beim nächsten Abgleich wieder gefragt.", nil
		}
		return "Abfrage ausgeschaltet. Das IT-Asset bleibt und steht ab dem nächsten Abgleich auf „unbekannt“ – es entsteht kein Alarm.", nil
	}, nil)
}

// snmpMayRemove: Eintrag aus der Liste entfernen – it.admin; hat der Eintrag ein Asset
// (assetID), dann it.admin für genau dieses Asset in seiner Abteilung. Entfernen trennt das
// Asset vom Abgleich, deshalb gilt dieselbe Prüfung je Datensatz wie bei /it/{id}/net-detach.
func (h *Handler) snmpMayRemove(r *http.Request, assetID string) bool {
	if !h.itNetCan(r, itNetPermAdmin, "") {
		return false
	}
	return assetID == "" || h.itNetCan(r, itNetPermAdmin, assetID)
}

// ITNetSNMPTargetRemoveWeb: POST /it/net/snmp/targets/{id}/remove – nur der
// Bereichsadministrator IT (it.admin), bei einem Eintrag mit Asset für dieses Asset.
// Das IT-Asset bleibt und wird nicht mehr abgeglichen.
func (h *Handler) ITNetSNMPTargetRemoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, itNetPermAdmin, "") {
		itNetForbidden(w)
		return
	}
	id, ok := itNetSNMPID(w, r)
	if !ok {
		return
	}
	// Unbekannter Eintrag oder Datenbankfehler: weiter unten meldet snmpTargetRemove den Grund.
	if asset, err := h.snmpTargetAssetID(r.Context(), id); err == nil && !h.snmpMayRemove(r, asset) {
		itNetForbidden(w)
		return
	}
	h.itNetSNMPAct(w, r, itNetPermAdmin, "gerät-entfernen", func(ctx context.Context) (string, error) {
		if err := h.snmpTargetRemove(ctx, id); err != nil {
			return "", err
		}
		return "Gerät aus der Liste entfernt. Das IT-Asset bleibt bestehen und wird nicht mehr abgeglichen.", nil
	}, nil)
}

// ITNetSNMPCheckView: Ergebnis von „Prüfen“ (Vorlage itn-snmp-check).
type ITNetSNMPCheckView struct {
	Err    string
	Detail snmp.Detail
	Ports  int
	Up     int
	Switch bool
}

// ITNetSNMPTargetCheckWeb: POST /it/net/snmp/targets/{id}/check – fragt das Gerät sofort
// und zeigt, was es antwortet, oder den Fehler in Klartext. Gespeichert wird nichts.
func (h *Handler) ITNetSNMPTargetCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	id, ok := itNetSNMPID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), snmpCheckTimeout)
	defer cancel()
	var v ITNetSNMPCheckView
	d, err := h.snmpTargetCheck(ctx, id)
	if err != nil {
		v.Err = snmpErrText(err, "gerät-prüfen")
	} else {
		v.Detail, v.Ports, v.Switch = d, len(d.Ports), d.Kind == snmp.DeviceSwitch
		for _, p := range d.Ports {
			if p.Up {
				v.Up++
			}
		}
	}
	h.itNetSNMPRender(w, "itn-snmp-check", v)
}
