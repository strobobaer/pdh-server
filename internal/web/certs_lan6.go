package web

import (
	"context"
	"errors"
	"fmt"
	"net"
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
		// nur eingeschaltete Einträge zeigen wirklich auf die Adresse
		if !strings.EqualFold(r.Type, "A") || !r.Enabled || !lan6Same(r.Value, v4) {
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

// lan6Marks: Merker je Name unter einem Präfix – alter Stand aus den Einstellungen, neuer Stand
// aus diesem Lauf. flush schreibt nur, was sich geändert hat; was im neuen Stand fehlt, wird geleert.
type lan6Marks struct {
	prefix   string
	old, cur map[string]string
}

func lan6MarksFrom(prefix string, kv map[string]string) *lan6Marks {
	m := &lan6Marks{prefix: prefix, old: map[string]string{}, cur: map[string]string{}}
	for k, v := range kv {
		m.old[strings.TrimPrefix(k, prefix)] = v
	}
	return m
}

// keepAll: nichts geprüft – alle Merker bleiben, wie sie sind.
func (m *lan6Marks) keepAll() {
	m.cur = map[string]string{}
	for k, v := range m.old {
		m.cur[k] = v
	}
}

// keepUnless: Merker der Namen, die nicht in done stehen, bleiben, wie sie vorher waren
// (Lauf abgebrochen: was nicht fertig geprüft ist, ändert sich nicht).
func (m *lan6Marks) keepUnless(done map[string]bool) {
	for n := range m.cur {
		if _, was := m.old[n]; !was && !done[n] {
			delete(m.cur, n)
		}
	}
	for n, v := range m.old {
		if !done[n] {
			m.cur[n] = v
		}
	}
}

func (m *lan6Marks) flush(put func(key, value string) error) {
	all := map[string]string{}
	for n := range m.old {
		all[n] = ""
	}
	for n := range m.cur {
		all[n] = ""
	}
	for _, n := range lan6SortedKeys(all) {
		if m.cur[n] != m.old[n] {
			_ = put(m.prefix+n, m.cur[n])
		}
	}
}

// certLAN6Sync gleicht die AAAA-Einträge an der UDM Pro ab (siehe oben) und meldet, wie viele
// Einträge angelegt, geändert oder entfernt wurden. Fehler stehen als Hinweis beim Namen und im
// Protokoll; der Lauf macht mit dem nächsten Namen weiter.
//
// Verloren geht nichts nach einer einzelnen Beobachtung: Eine gemerkte Kennung wird erst
// vergessen, wenn sie in zwei Läufen nacheinander in der Antwort fehlt, und ein eigener Eintrag
// erst gelöscht, wenn der Name in zwei Läufen nacheinander nicht mehr auf die mitwandernde
// Adresse zeigt oder IPv6 zweimal nacheinander sauber als „aus“ gemeldet wird (Zähler
// unifi.wait6.<name>). Läuft die Frist mitten im Lauf ab, behalten die übrigen Namen ihre
// Hinweise und Merker.
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
	otherKeys := d.settingsPrefix(ctx, keyUnifiOther6Prefix)
	waitKeys := d.settingsPrefix(ctx, keyUnifiWait6Prefix)
	if ownKeys == nil || hintKeys == nil || otherKeys == nil || waitKeys == nil {
		return 0 // Einstellungen nicht lesbar – nichts entscheiden
	}
	own := map[string]string{}
	for k, v := range ownKeys {
		own[strings.TrimPrefix(k, keyUnifiID6Prefix)] = v
	}
	hints := lan6MarksFrom(keyUnifiHint6Prefix, hintKeys)
	other := lan6MarksFrom(keyUnifiOther6Prefix, otherKeys)
	wait := lan6MarksFrom(keyUnifiWait6Prefix, waitKeys)
	defer func() {
		hints.flush(persist)
		other.flush(persist)
		wait.flush(persist)
	}()
	keepAll := func() {
		hints.keepAll()
		other.keepAll()
		wait.keepAll()
	}
	if !t.Wanted && len(own) == 0 {
		return 0 // nichts gewünscht, nichts eigenes: keine Anfrage an die UDM Pro
	}

	lg := componentLog("zertifikate")
	c, _, err := d.lanCheck(ctx)
	if err != nil || c == nil {
		if err != nil && !errors.Is(err, errUnifiOff) {
			lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("ipv6-einträge im haus nicht abgeglichen")
		}
		keepAll() // nichts geprüft – Merker bleiben, wie sie sind
		return 0
	}
	recs, err := c.Records(ctx)
	if err != nil {
		lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("ipv6-einträge im haus nicht abgeglichen")
		keepAll()
		return 0
	}
	if len(recs) == 0 {
		// Eine leere Antwort ist kein Beweis, dass alles fehlt (falscher Standort, Gerät startet):
		// unbekannt – nichts anlegen, nichts vergessen, nichts löschen.
		lg.Info().Msg("ipv6-einträge im haus: udm pro meldet keine einträge – nichts geändert")
		keepAll()
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
	// observe: true bei der zweiten gleichen Beobachtung in Folge (dann wird gehandelt und der
	// Zähler geleert); sonst Zähler setzen. Was nicht beobachtet wird, setzt den Zähler zurück.
	observe := func(n, what string) bool {
		if wait.old[n] == what+":1" {
			return true
		}
		wait.cur[n] = what + ":1"
		return false
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

	// Frist mitten im Lauf abgelaufen: abbrechen. Namen, die nicht fertig geprüft sind (nicht in
	// checked), behalten Hinweise und Merker; ein Fehler der Frist wird nie zum Hinweis.
	checked := map[string]bool{}
	aborted := false
	expired := func() bool {
		if ctx.Err() != nil {
			aborted = true
		}
		return aborted
	}
	defer func() { // läuft vor dem Schreiben der Merker (oben)
		if aborted {
			hints.keepUnless(checked)
			other.keepUnless(checked)
			wait.keepUnless(checked)
			lg.Warn().Msg("ipv6-einträge im haus: frist abgelaufen – übrige namen im nächsten lauf")
		}
	}()

	for _, n := range names {
		if expired() {
			break
		}
		checked[n] = true
		var aaaa []unifi.Record
		off := false
		for _, r := range recs {
			if strings.EqualFold(r.Type, "AAAA") && certLANNorm(r.Name) == n {
				aaaa = append(aaaa, r)
				off = off || !r.Enabled
			}
		}
		if off {
			// abgeschaltet (eigen oder fremd): weder anlegen noch ändern, kein Knopf
			hints.cur[n] = "Der IPv6-Eintrag von " + n + " ist an der UDM Pro abgeschaltet. Nichts geändert."
			continue
		}
		var mine *unifi.Record
		if id := own[n]; id != "" {
			r, st := ownState(n, id)
			switch st {
			case "ok":
				mine = &r
			case "weg":
				// Der Name zeigt mit Typ A auf V4, die Kennung fehlt. Erst beim zweiten Mal in
				// Folge vergessen (und dann wie üblich weiter); vorher nichts anlegen.
				if !observe(n, "fehlt") {
					lg.Info().Str("name", n).Str("kennung", id).Msg("ipv6-eintrag im haus fehlt in der antwort – erst beim nächsten lauf vergessen")
					continue
				}
				forget(n, id, "ipv6-eintrag im haus fehlt an der udm pro – kennung vergessen")
			default:
				forget(n, id, "ipv6-eintrag im haus an der udm pro verändert – kennung vergessen")
				hints.cur[n] = lan6ChangedHint(n)
				continue // erst im nächsten Lauf wie jeder andere Name behandeln
			}
		}
		update := func(r unifi.Record, ownRec bool) {
			if err := c.Update(ctx, r.ID, n, t.V6); err != nil {
				if ctx.Err() != nil {
					aborted = true
					delete(checked, n)
					return
				}
				hints.cur[n] = "IPv6-Eintrag nicht geändert: " + certSafeText(err.Error())
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
			addr := strings.TrimSpace(aaaa[0].Value)
			hints.cur[n] = "An der UDM Pro steht für " + n + " schon ein IPv6-Eintrag auf " + addr + ". Nichts geändert."
			other.cur[n] = addr // Knopf „IPv6 überschreiben“
			continue
		}
		nid, err := c.Create(ctx, n, t.V6)
		if err != nil {
			if ctx.Err() != nil {
				aborted = true
				delete(checked, n)
				break
			}
			hints.cur[n] = "IPv6-Eintrag nicht angelegt: " + certSafeText(err.Error())
			lg.Warn().Str("name", n).Str("adresse", t.V6).Str("fehler", certSafeText(err.Error())).Msg("ipv6-eintrag im haus nicht angelegt")
			continue
		}
		changed++
		lg.Info().Str("name", n).Str("adresse", t.V6).Str("kennung", nid).Msg("ipv6-eintrag im haus angelegt")
		if err := persist(keyUnifiID6Prefix+n, nid); err != nil {
			hints.cur[n] = fmt.Sprintf("IPv6-Eintrag angelegt (Kennung %s), aber nicht gemerkt: %s", nid, certSafeText(err.Error()))
			lg.Warn().Str("name", n).Str("kennung", nid).Str("fehler", certSafeText(err.Error())).Msg("kennung des ipv6-eintrags im haus nicht gemerkt")
		}
	}

	// Aufräumen: eigene Einträge von Namen, die nicht (mehr) auf die mitwandernde Adresse
	// zeigen, oder alle eigenen, wenn IPv6 sauber aus ist – beides erst nach zwei gleichen
	// Beobachtungen in Folge (ein kurz verlorenes Ende kostet keine Einträge).
	what := "weg"
	if !t.Wanted {
		what = "aus"
	}
	for _, n := range lan6SortedKeys(own) {
		if pointing[n] {
			continue
		}
		if expired() {
			break
		}
		checked[n] = true
		id := own[n]
		switch _, st := ownState(n, id); st {
		case "weg":
			// nicht in der (nicht leeren) Antwort: nichts zu löschen; erst beim zweiten Mal vergessen
			if observe(n, "fehlt") {
				forget(n, id, "ipv6-eintrag im haus fehlt an der udm pro – kennung vergessen")
			}
			continue
		case "veraendert":
			forget(n, id, "ipv6-eintrag im haus an der udm pro verändert – kennung vergessen")
			hints.cur[n] = lan6ChangedHint(n)
			continue
		}
		if !observe(n, what) {
			if t.Wanted {
				lg.Info().Str("name", n).Str("kennung", id).Msg("name zeigt nicht mehr auf die mitwandernde adresse – ipv6-eintrag erst beim nächsten lauf entfernt")
			} else {
				lg.Info().Str("name", n).Str("kennung", id).Msg("ipv6-ende ist aus – ipv6-eintrag erst beim nächsten lauf entfernt")
			}
			continue
		}
		if err := c.Delete(ctx, id); err != nil {
			if ctx.Err() != nil {
				aborted = true
				delete(checked, n)
				break
			}
			hints.cur[n] = "IPv6-Eintrag (Kennung " + id + ") nicht entfernt: " + certSafeText(err.Error())
			wait.cur[n] = what + ":1" // im nächsten Lauf gleich wieder versuchen
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

var errLAN6Unknown = errors.New("Die mitwandernde IPv6-Adresse ist gerade nicht bekannt. Bitte später erneut versuchen.")

// certDoLAN6: „IPv6 überschreiben“ (eigener Knopf, getrennt von IPv4). Ändert genau einen
// fremden AAAA-Eintrag eines Namens, der mit Typ A auf die mitwandernde Adresse zeigt, auf die
// mitwandernde IPv6-Adresse – ohne ihn zu übernehmen. Der Name muss kein Zertifikat haben.
// Legt nie etwas an, löscht nie, ändert nie einen Eintrag Typ A oder einen anderen Namen.
// from ist die Adresse, die die Rückfrage gezeigt hat: geändert wird nur, wenn der fremde
// Eintrag noch genau diese Adresse hat (fehlt from, wird nichts geändert).
func certDoLAN6(ctx context.Context, d certActDeps, raw, from string) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	if d.lan == nil {
		return "", errUnifiOff
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "*") {
		return "", errors.New("Sammelnamen (*.…) werden nicht unterstützt")
	}
	// dieselbe Regel wie die UniFi-Anbindung: auch Kurznamen wie „nas“
	n := certLANNorm(raw)
	if n == "" {
		return "", errors.New("Name fehlt")
	}
	if !unifi.ValidName(n) {
		return "", fmt.Errorf("%q ist kein gültiger Name", raw)
	}
	if net.ParseIP(n) != nil {
		return "", fmt.Errorf("%q ist ein Name, keine IP-Adresse", raw)
	}
	var t lan6Target
	if d.lan6 != nil {
		t = d.lan6(ctx)
	}
	if !t.Wanted || t.V4 == "" || t.V6 == "" {
		return "", errLAN6Unknown
	}
	c, err := d.lan(ctx)
	if err != nil {
		return "", err
	}
	recs, err := c.Records(ctx)
	if err != nil {
		return "", err
	}
	pointing := false
	for _, x := range certLAN6Names(recs, t.V4) {
		pointing = pointing || x == n
	}
	if !pointing {
		return "", errors.New(n + " zeigt an der UDM Pro nicht auf die mitwandernde Adresse. Nichts geändert.")
	}
	ownID := ""
	if d.setting != nil {
		ownID = d.setting(ctx, keyUnifiID6Prefix+n)
	}
	clear := func() {
		_ = d.putSetting(ctx, keyUnifiHint6Prefix+n, "")
		_ = d.putSetting(ctx, keyUnifiOther6Prefix+n, "")
	}
	var foreign []unifi.Record
	for _, r := range recs {
		if !strings.EqualFold(r.Type, "AAAA") || certLANNorm(r.Name) != n {
			continue
		}
		if !r.Enabled {
			return "", errors.New("Der IPv6-Eintrag von " + n + " ist an der UDM Pro abgeschaltet. Nichts geändert.")
		}
		if lan6Same(r.Value, t.V6) {
			clear()
			return n + " zeigt für IPv6 schon auf " + t.V6 + ".", nil
		}
		if r.ID != ownID {
			foreign = append(foreign, r)
		}
	}
	switch len(foreign) {
	case 0:
		clear()
		return "Am IPv6-Eintrag von " + n + " war nichts zu ändern.", nil
	case 1:
	default:
		return "", errors.New("Für " + n + " gibt es mehrere IPv6-Einträge an der UDM Pro. Bitte dort von Hand aufräumen.")
	}
	rec := foreign[0]
	if strings.TrimSpace(from) == "" || !lan6Same(rec.Value, from) {
		return "", errors.New("Der IPv6-Eintrag von " + n + " hat sich inzwischen geändert. Bitte die Seite neu laden.")
	}
	if err := c.Update(ctx, rec.ID, n, t.V6); err != nil {
		return "", err
	}
	componentLog("zertifikate").Info().Str("name", n).Str("adresse", t.V6).Str("alt", rec.Value).Str("kennung", rec.ID).
		Msg("fremden ipv6-eintrag im haus überschrieben (nicht übernommen)")
	clear()
	return n + " zeigt für IPv6 jetzt auf " + t.V6 + " (vorhandener Eintrag geändert, vorher " + strings.TrimSpace(rec.Value) +
		"). Der Eintrag bleibt an der UDM Pro stehen, wenn das PDH aufräumt, weil das PDH ihn nicht angelegt hat.", nil
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
// Other: Adresse eines fremden AAAA-Eintrags (unifi.other6.<name>) – dann Knopf „IPv6 überschreiben“.
type lan6Extra struct{ Name, Hint, Other string }

func certLAN6Extra(hints, others map[string]string, rows []certRow) []lan6Extra {
	have := map[string]bool{}
	for _, r := range rows {
		have[certLANNorm(r.Name)] = true
	}
	byName := map[string]*lan6Extra{}
	add := func(kv map[string]string, prefix string, set func(e *lan6Extra, v string)) {
		for k, v := range kv {
			n := strings.TrimPrefix(k, prefix)
			if v == "" || n == "" || have[n] {
				continue
			}
			e := byName[n]
			if e == nil {
				e = &lan6Extra{Name: n}
				byName[n] = e
			}
			set(e, v)
		}
	}
	add(hints, keyUnifiHint6Prefix, func(e *lan6Extra, v string) { e.Hint = v })
	add(others, keyUnifiOther6Prefix, func(e *lan6Extra, v string) { e.Other = v })
	names := map[string]string{}
	for n := range byName {
		names[n] = ""
	}
	var out []lan6Extra
	for _, n := range lan6SortedKeys(names) {
		out = append(out, *byName[n])
	}
	return out
}
