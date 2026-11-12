package web

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"pdh/internal/integrations/unifi"
)

// Namen im Haus über IPv6 (Typ AAAA an der UniFi UDM Pro).
//
// Mit eingestelltem IPv6-Ende der mitwandernden Adresse (Ausfallschutz) bekommt JEDER Name,
// der an der UDM Pro mit Typ A auf die mitwandernde IPv4-Adresse zeigt, einen Eintrag Typ AAAA
// auf die mitwandernde IPv6-Adresse – auch von Hand angelegte Namen, die das PDH sonst nicht
// führt. Es zählt, wohin ein Eintrag zeigt, nicht wer ihn angelegt hat:
//
//   - eigener AAAA-Eintrag (gemerkte Kennung unifi.id6.<name>) wird nachgezogen;
//   - ein fremder AAAA-Eintrag, dessen untere 64 Bit dem Ende entsprechen, wird ebenfalls
//     nachgezogen (Präfixwechsel des Anbieters), aber nicht übernommen;
//   - ein AAAA-Eintrag mit anderem Ziel bleibt, wie er ist (Hinweis unifi.hint6.<name>);
//     nur „Überschreiben“ in der Zertifikatskarte ändert ihn;
//   - gelöscht werden nur Einträge, die das PDH selbst angelegt hat.
//
// Der Lauf kommt alle 5 Minuten (StartCertRenewal); am Spiegel und in der Wartung nichts.
// Ohne UniFi-Einrichtung, ohne IPv6-Ende und ohne gemerkte Kennung geht keine Anfrage an die
// UDM Pro.

// lan6MaxNames: höchstens so viele Namen je Lauf (kein unbegrenzter Schwall an Änderungen).
const lan6MaxNames = 200

// lan6Target: mitwandernde IPv4- und IPv6-Adresse aus dem Status des Verbunds.
// V6 == "" = unbekannt (dann ändert der Lauf nichts); Wanted = IPv6-Ende eingestellt.
// Ist der Verbund nicht erreichbar, gilt Wanted mit unbekannter Adresse – so löscht ein
// kurzer Ausfall des Status nie Einträge.
type lan6Target struct {
	V4, V6 string
	Suffix netip.Addr
	Wanted bool
}

// lan6Suffix: eingestelltes Ende wie im Dienst pdh-ha – nur die unteren 64 Bit, nicht null.
func lan6Suffix(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, ".%/") {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr("::" + s)
	if err != nil || !a.Is6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	upper, lower := false, false
	for i := 0; i < 8; i++ {
		upper = upper || b[i] != 0
		lower = lower || b[8+i] != 0
	}
	if upper || !lower {
		return netip.Addr{}, false
	}
	return a, true
}

// lan6HasSuffix: untere 64 Bit der Adresse gleich dem Ende.
func lan6HasSuffix(value string, suffix netip.Addr) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" || !suffix.IsValid() {
		return false
	}
	x, y := a.As16(), suffix.As16()
	for i := 8; i < 16; i++ {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// lan6Same: gleiche Adresse (Schreibweise egal).
func lan6Same(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	x, ex := netip.ParseAddr(a)
	y, ey := netip.ParseAddr(b)
	if ex == nil && ey == nil {
		return x == y
	}
	return a == b
}

// certLAN6Names: normalisierte Namen mit einem Eintrag Typ A auf v4, sortiert, ohne Dopplungen.
func certLAN6Names(recs []unifi.Record, v4 string) []string {
	if strings.TrimSpace(v4) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range recs {
		if !strings.EqualFold(r.Type, "A") || !lan6Same(r.Value, v4) {
			continue
		}
		n := certLANNorm(r.Name)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func lan6SortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// certLAN6Sync gleicht die AAAA-Einträge an der UDM Pro ab (siehe oben) und meldet, wie viele
// Einträge angelegt, geändert oder entfernt wurden. Fehler stehen als Hinweis beim Namen und im
// Protokoll; der Lauf macht mit dem nächsten Namen weiter.
func certLAN6Sync(ctx context.Context, d certDeps, t lan6Target) (changed int) {
	d = d.withDefaults()
	if d.blocked() || d.lanCheck == nil || d.settingsPrefix == nil {
		return 0
	}
	if t.Wanted && (t.V4 == "" || t.V6 == "" || !t.Suffix.IsValid()) {
		return 0 // Ziel unbekannt: nichts anlegen, nichts ändern, nichts löschen
	}
	// Merker nach einer gelungenen Änderung auch dann schreiben, wenn die Frist des Laufs
	// gerade abläuft – sonst ginge eine Kennung verloren.
	persist := func(k, v string) error {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return d.setSetting(pctx, k, v)
	}
	ownKeys := d.settingsPrefix(ctx, keyUnifiID6Prefix)
	hintKeys := d.settingsPrefix(ctx, keyUnifiHint6Prefix)
	if ownKeys == nil || hintKeys == nil {
		return 0 // Einstellungen nicht lesbar – nichts entscheiden
	}
	own := map[string]string{}
	for k, v := range ownKeys {
		own[strings.TrimPrefix(k, keyUnifiID6Prefix)] = v
	}
	oldHints := map[string]string{}
	for k, v := range hintKeys {
		oldHints[strings.TrimPrefix(k, keyUnifiHint6Prefix)] = v
	}
	hints := map[string]string{} // neuer Stand; was fehlt, wird geleert
	defer func() {
		for n, old := range oldHints {
			if hints[n] != old {
				_ = persist(keyUnifiHint6Prefix+n, hints[n])
			}
		}
		for _, n := range lan6SortedKeys(hints) {
			if _, had := oldHints[n]; !had && hints[n] != "" {
				_ = persist(keyUnifiHint6Prefix+n, hints[n])
			}
		}
	}()
	if !t.Wanted && len(own) == 0 {
		return 0 // nichts gewünscht, nichts eigenes: keine Anfrage an die UDM Pro
	}

	lg := componentLog("zertifikate")
	c, _, err := d.lanCheck(ctx)
	if err != nil || c == nil {
		if err != nil && !errors.Is(err, errUnifiOff) {
			lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("ipv6-einträge im haus nicht abgeglichen")
		}
		hints = oldHints // nichts geprüft – Hinweise bleiben, wie sie sind
		return 0
	}
	recs, err := c.Records(ctx)
	if err != nil {
		lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("ipv6-einträge im haus nicht abgeglichen")
		hints = oldHints
		return 0
	}
	if len(recs) == 0 {
		// Eine leere Antwort ist kein Beweis, dass alles fehlt (falscher Standort, Gerät startet):
		// unbekannt – nichts anlegen, nichts vergessen, nichts löschen.
		lg.Info().Msg("ipv6-einträge im haus: udm pro meldet keine einträge – nichts geändert")
		hints = oldHints
		return 0
	}
	byID := map[string]unifi.Record{}
	for _, r := range recs {
		if _, dup := byID[r.ID]; !dup {
			byID[r.ID] = r
		}
	}
	// ownState: was aus einer gemerkten Kennung an der UDM Pro geworden ist –
	// "ok" (AAAA gleichen Namens), "weg" (nicht in der Antwort) oder "veraendert"
	// (jetzt anderer Typ oder anderer Name: das PDH führt ihn nicht mehr, nie löschen).
	ownState := func(n, id string) (unifi.Record, string) {
		r, ok := byID[id]
		switch {
		case !ok:
			return r, "weg"
		case !strings.EqualFold(r.Type, "AAAA") || certLANNorm(r.Name) != n:
			return r, "veraendert"
		}
		return r, "ok"
	}
	forget := func(n, id, why string) {
		_ = persist(keyUnifiID6Prefix+n, "")
		lg.Info().Str("name", n).Str("kennung", id).Msg(why)
	}

	var names []string
	if t.Wanted {
		names = certLAN6Names(recs, t.V4)
	}
	pointing := map[string]bool{}
	for _, n := range names {
		pointing[n] = true
	}
	if len(names) > lan6MaxNames {
		if lan6CapWarnDue(d.now()) {
			lg.Warn().Int("namen", len(names)).Int("bearbeitet", lan6MaxNames).
				Msg("mehr namen auf der mitwandernden adresse als erlaubt – ipv6 nur für die ersten")
		}
		names = names[:lan6MaxNames]
	}

	for _, n := range names {
		var aaaa []unifi.Record
		for _, r := range recs {
			if strings.EqualFold(r.Type, "AAAA") && certLANNorm(r.Name) == n {
				aaaa = append(aaaa, r)
			}
		}
		var mine *unifi.Record
		if id := own[n]; id != "" {
			r, st := ownState(n, id)
			switch st {
			case "ok":
				mine = &r
			case "weg":
				// Dieselbe Antwort zeigt den Namen mit Typ A auf V4, aber nicht die Kennung:
				// von Hand gelöscht – vergessen und wie üblich weiter.
				forget(n, id, "ipv6-eintrag im haus fehlt an der udm pro – kennung vergessen")
			default:
				forget(n, id, "ipv6-eintrag im haus an der udm pro verändert – kennung vergessen")
				hints[n] = lan6ChangedHint(n)
				continue // erst im nächsten Lauf wie jeder andere Name behandeln
			}
		}
		update := func(r unifi.Record, ownRec bool) {
			if err := c.Update(ctx, r.ID, n, t.V6); err != nil {
				hints[n] = "IPv6-Eintrag nicht geändert: " + certSafeText(err.Error())
				lg.Warn().Str("name", n).Str("kennung", r.ID).Str("fehler", certSafeText(err.Error())).Msg("ipv6-eintrag im haus nicht geändert")
				return
			}
			changed++
			lg.Info().Str("name", n).Str("adresse", t.V6).Str("alt", r.Value).Str("kennung", r.ID).Bool("eigen", ownRec).
				Msg("ipv6-eintrag im haus geändert")
		}
		if mine != nil {
			if !lan6Same(mine.Value, t.V6) {
				update(*mine, true)
			}
			continue
		}
		done := false
		for _, r := range aaaa {
			if lan6Same(r.Value, t.V6) {
				done = true // zeigt schon richtig (wer ihn auch angelegt hat)
				break
			}
		}
		if done {
			continue
		}
		for _, r := range aaaa {
			if lan6HasSuffix(r.Value, t.Suffix) {
				update(r, false) // nach Präfixwechsel nachziehen, nicht übernehmen
				done = true
				break
			}
		}
		if done {
			continue
		}
		if len(aaaa) > 0 {
			hints[n] = "An der UDM Pro steht für " + n + " schon ein IPv6-Eintrag auf " + strings.TrimSpace(aaaa[0].Value) + ". Nichts geändert."
			continue
		}
		nid, err := c.Create(ctx, n, t.V6)
		if err != nil {
			hints[n] = "IPv6-Eintrag nicht angelegt: " + certSafeText(err.Error())
			lg.Warn().Str("name", n).Str("adresse", t.V6).Str("fehler", certSafeText(err.Error())).Msg("ipv6-eintrag im haus nicht angelegt")
			continue
		}
		changed++
		lg.Info().Str("name", n).Str("adresse", t.V6).Str("kennung", nid).Msg("ipv6-eintrag im haus angelegt")
		if err := persist(keyUnifiID6Prefix+n, nid); err != nil {
			hints[n] = fmt.Sprintf("IPv6-Eintrag angelegt (Kennung %s), aber nicht gemerkt: %s", nid, certSafeText(err.Error()))
			lg.Warn().Str("name", n).Str("kennung", nid).Str("fehler", certSafeText(err.Error())).Msg("kennung des ipv6-eintrags im haus nicht gemerkt")
		}
	}

	// Aufräumen: eigene Einträge von Namen, die nicht (mehr) auf die mitwandernde Adresse
	// zeigen, oder alle eigenen, wenn IPv6 aus ist.
	for _, n := range lan6SortedKeys(own) {
		if pointing[n] {
			continue
		}
		id := own[n]
		switch _, st := ownState(n, id); st {
		case "weg":
			// nicht in der (nicht leeren) Antwort: nichts zu löschen, Kennung vergessen
			forget(n, id, "ipv6-eintrag im haus fehlt an der udm pro – kennung vergessen")
			continue
		case "veraendert":
			forget(n, id, "ipv6-eintrag im haus an der udm pro verändert – kennung vergessen")
			hints[n] = lan6ChangedHint(n)
			continue
		}
		if err := c.Delete(ctx, id); err != nil {
			hints[n] = "IPv6-Eintrag (Kennung " + id + ") nicht entfernt: " + certSafeText(err.Error())
			lg.Warn().Str("name", n).Str("kennung", id).Str("fehler", certSafeText(err.Error())).Msg("ipv6-eintrag im haus nicht entfernt")
			continue
		}
		changed++
		_ = persist(keyUnifiID6Prefix+n, "")
		lg.Info().Str("name", n).Str("kennung", id).Msg("ipv6-eintrag im haus entfernt")
	}
	return changed
}

// lan6ChangedHint: Hinweis, wenn eine gemerkte Kennung an der UDM Pro jetzt etwas anderes ist.
func lan6ChangedHint(name string) string {
	return "Der IPv6-Eintrag von " + name + " wurde an der UDM Pro verändert. Das PDH führt ihn nicht mehr."
}

// lan6CapWarn: Warnung „mehr als 200 Namen“ höchstens einmal am Tag.
var lan6CapWarn struct {
	sync.Mutex
	day string
}

func lan6CapWarnDue(now time.Time) bool {
	lan6CapWarn.Lock()
	defer lan6CapWarn.Unlock()
	day := now.Format("2006-01-02")
	if lan6CapWarn.day == day {
		return false
	}
	lan6CapWarn.day = day
	return true
}

// certLAN6Overwrite: „Überschreiben“ für einen geführten Namen – einen fremden AAAA-Eintrag
// mit anderem Ziel auf die mitwandernde IPv6-Adresse ändern, ohne ihn zu übernehmen.
func certLAN6Overwrite(ctx context.Context, d certActDeps, c certLANClient, recs []unifi.Record, name string) string {
	if d.lan6 == nil || d.setting == nil {
		return ""
	}
	t := d.lan6(ctx)
	if !t.Wanted || t.V6 == "" {
		return ""
	}
	n := certLANNorm(name)
	action, rec := certLANMatchType(recs, "AAAA", n, t.V6, d.setting(ctx, keyUnifiID6Prefix+n))
	if action != "fremd" {
		return ""
	}
	lg := componentLog("zertifikate")
	if err := c.Update(ctx, rec.ID, n, t.V6); err != nil {
		lg.Warn().Str("name", n).Str("kennung", rec.ID).Str("fehler", certSafeText(err.Error())).Msg("fremden ipv6-eintrag im haus nicht überschrieben")
		return " Der IPv6-Eintrag ist nicht geändert: " + certSafeText(err.Error()) + "."
	}
	lg.Info().Str("name", n).Str("adresse", t.V6).Str("alt", rec.Value).Str("kennung", rec.ID).Msg("fremden ipv6-eintrag im haus überschrieben (nicht übernommen)")
	_ = d.putSetting(ctx, keyUnifiHint6Prefix+n, "")
	return " Der IPv6-Eintrag zeigt jetzt auf " + t.V6 + " (vorher " + strings.TrimSpace(rec.Value) + ")."
}

// lan6Unknown: Ziel unbekannt – der Lauf legt nichts an, ändert nichts und löscht nichts.
var lan6Unknown = lan6Target{Wanted: true}

// lan6TargetFrom: Ziel aus dem Status des Verbunds. Nur ein sauberer Status (OK, ohne Fehler)
// kann „aus“ bedeuten; ein Status mit Fehler (z. B. Einstellungen nicht ladbar) ist unbekannt.
func lan6TargetFrom(st haStatus) lan6Target {
	if !st.OK || strings.TrimSpace(st.Error) != "" {
		return lan6Unknown
	}
	a := st.Access
	t := lan6Target{Wanted: st.Configured && a.VIPOn && strings.TrimSpace(a.VIP6Suffix) != ""}
	if !t.Wanted {
		return t
	}
	t.Suffix, _ = lan6Suffix(a.VIP6Suffix)
	if v4, err := netip.ParseAddr(vipAddr(strings.TrimSpace(a.VIP))); err == nil && v4.Is4() {
		t.V4 = v4.String()
	}
	if v6, err := netip.ParseAddr(strings.TrimSpace(a.VIP6)); err == nil && v6.Is6() && !v6.Is4In6() && v6.Zone() == "" {
		t.V6 = v6.String()
	}
	return t
}

// certLAN6Target: Ziel aus dem Verbund (2 s Frist). Nicht erreichbar = unbekannt (kein Löschen).
func (h *Handler) certLAN6Target(ctx context.Context) lan6Target {
	return lan6TargetFetched(h.certHAStatus(ctx))
}

// lan6TargetFetched: wie lan6TargetFrom; ein Abruffehler heißt unbekannt.
func lan6TargetFetched(st haStatus, err error) lan6Target {
	if err != nil {
		return lan6Unknown
	}
	return lan6TargetFrom(st)
}

// certLAN6Run: ein Lauf des 5-Minuten-Takts; eine Panik landet im Protokoll.
func (h *Handler) certLAN6Run(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			componentLog("zertifikate").Error().Str("panik", certSafeText(fmt.Sprint(p))).
				Msg("ipv6-einträge im haus: lauf mit internem fehler abgebrochen")
		}
	}()
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	certLAN6Sync(runCtx, h.certDepsLive(), h.certLAN6Target(runCtx))
}

// certLAN6Extra: Hinweise zu Namen an der UDM Pro, die das PDH nicht als Zertifikat führt –
// nur aus den Einstellungen, nie eine Anfrage an die UDM Pro.
type lan6Extra struct{ Name, Hint string }

func certLAN6Extra(hints map[string]string, rows []certRow) []lan6Extra {
	have := map[string]bool{}
	for _, r := range rows {
		have[certLANNorm(r.Name)] = true
	}
	var out []lan6Extra
	for _, k := range lan6SortedKeys(hints) {
		n := strings.TrimPrefix(k, keyUnifiHint6Prefix)
		if hints[k] == "" || n == "" || have[n] {
			continue
		}
		out = append(out, lan6Extra{Name: n, Hint: hints[k]})
	}
	return out
}
