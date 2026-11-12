package web

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/integrations/acme"
	"pdh/internal/integrations/cloudflare"
)

// Let's-Encrypt-Zertifikate (optional): ein Auftrag je Name, die tägliche Prüfung mit
// Erneuerung, Abgleich mit den Dateien des Update-Agenten und die Alarme. Ohne
// eingetragene Namen tut der tägliche Lauf nichts – kein Cloudflare, kein Agent, keine CA.

// certDeps sind die Teile, die ein Auftrag braucht; im Betrieb aus certDepsLive, in den
// Tests im Speicher.
type certDeps struct {
	store   certStore
	ca      func(staging bool) (acme.CA, error)
	dns     func() (acme.DNS, error)
	// install gibt Zertifikat und Schlüssel dem Agenten; note ist sein Hinweis, wenn er nur die
	// Dateien ablegen konnte und an der nginx-Seite etwas von Hand zu tun ist.
	install func(ctx context.Context, name string, chain, key []byte) (fp, note string, err error)
	status  func(ctx context.Context) (map[string]certFile, error) // Name → Datei beim Agenten
	role    func(ctx context.Context, name string) string          // "pdh", "nextcloud" oder ""; nil = ""
	blocked func() bool                                            // Spiegel oder Wartung: nichts starten
	alarm   func(text string)
	now     func() time.Time

	// Merkzettel für die Alarme (app_settings); fehlen sie, merkt sich nichts den Tag.
	getSetting func(ctx context.Context, key string) string
	setSetting func(ctx context.Context, key, value string) error

	// Namen im Haus (UniFi, optional): nur lesen. nil oder errUnifiOff = nicht eingerichtet, keine Anfrage.
	lanCheck func(ctx context.Context) (certLANClient, string, error)
}

// certFile: Stand einer Zertifikatsdatei beim Update-Agenten (GET /v1/certs/status).
type certFile struct {
	Fingerprint string
	NotAfter    time.Time
	Site        bool // eine nginx-Seite verweist auf /etc/ssl/pdh/<name>/
}

var errCertBusy = errors.New("Für diesen Namen läuft schon ein Auftrag")

const (
	// keyCertAlarmPrefix + Name: zuletzt gemeldeter Alarm, damit ein Neustart nicht doppelt meldet.
	keyCertAlarmPrefix = "certs.alarm."
	// keyCertPrevPrefix + Name: Zertifikatszeilen der Nextcloud-Seite vor der ersten Umstellung.
	keyCertPrevPrefix = "certs.prev."
	// keyCertNotePrefix + Name: Hinweis des Agenten (Dateien abgelegt, Seite von Hand), kein Fehler.
	keyCertNotePrefix = "certs.note."
	// keyCertPendingPrefix + Name: zuletzt gemeldet „erneuert, aber noch nicht eingerichtet“ (Fingerabdruck|Tag).
	keyCertPendingPrefix = "certs.pending."

	certNotInstalled = "noch nicht eingerichtet"
	certUpstreamDef  = "http://127.0.0.1:8090"
)

func (d certDeps) withDefaults() certDeps {
	if d.now == nil {
		d.now = time.Now
	}
	if d.blocked == nil {
		d.blocked = func() bool { return false }
	}
	if d.alarm == nil {
		d.alarm = func(string) {}
	}
	if d.getSetting == nil {
		d.getSetting = func(context.Context, string) string { return "" }
	}
	if d.setSetting == nil {
		d.setSetting = func(context.Context, string, string) error { return nil }
	}
	if d.role == nil {
		d.role = func(context.Context, string) string { return "" }
	}
	return d
}

// ── Sperre je Name ───────────────────────────────────────────

var certJobs = struct {
	sync.Mutex
	m map[string]struct{}
}{m: map[string]struct{}{}}

// certTryLock belegt den Namen; false, wenn schon ein Auftrag läuft.
func certTryLock(name string) bool {
	certJobs.Lock()
	defer certJobs.Unlock()
	if _, ok := certJobs.m[name]; ok {
		return false
	}
	certJobs.m[name] = struct{}{}
	return true
}

func certUnlock(name string) {
	certJobs.Lock()
	defer certJobs.Unlock()
	delete(certJobs.m, name)
}

// ── Ein Auftrag ──────────────────────────────────────────────

// certFingerprint: SHA-256 des Blatt-Zertifikats, hex klein – wie acme.Issue und der Agent.
func certFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// certSafeText kürzt eine Fehlermeldung für last_error und Protokoll: nie ein PEM-Block,
// höchstens 500 Zeichen.
func certSafeText(s string) string {
	if i := strings.Index(s, "-----BEGIN"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + " …"
	}
	return s
}

// certRememberDNS merkt sich die Kennung des Prüf-Eintrags in der Datenbank, solange er
// steht. Stürzt das PDH zwischen Anlegen und Löschen ab, räumt der tägliche Lauf ihn weg.
// pending ist die Kennung, solange der Eintrag nicht gelöscht ist; certIssue merkt sie nach
// dem Speichern erneut (certSaveIssued leert challenge_id).
type certRememberDNS struct {
	acme.DNS
	ctx     context.Context
	store   certStore
	name    string
	pending string
}

func (r *certRememberDNS) Present(ctx context.Context, fqdn, value string) (string, error) {
	id, err := r.DNS.Present(ctx, fqdn, value)
	if err == nil && id != "" {
		r.pending = id
		_ = r.store.certSetChallenge(r.ctx, r.name, id)
	}
	return id, err
}

func (r *certRememberDNS) CleanUp(ctx context.Context, id string) error {
	err := r.DNS.CleanUp(ctx, id)
	if err == nil {
		r.pending = ""
		_ = r.store.certSetChallenge(r.ctx, r.name, "")
	} else {
		componentLog("zertifikate").Warn().Str("name", r.name).Str("fehler", certSafeText(err.Error())).
			Msg("prüf-eintrag nicht gelöscht – der tägliche lauf räumt ihn auf")
	}
	return err
}

// certWait wartet die Zeit oder bis ctx endet.
func certWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// certIssue holt (oder erneuert) das Zertifikat für einen eingetragenen Namen, speichert es
// und gibt es dem Agenten. Scheitert etwas, bleibt das bisherige Zertifikat in Betrieb.
func certIssue(ctx context.Context, d certDeps, name string) (err error) {
	d = d.withDefaults()
	if !certTryLock(name) {
		return errCertBusy
	}
	defer certUnlock(name)

	lg := componentLog("zertifikate")
	sctx := context.WithoutCancel(ctx) // Zustand auch nach Abbruch noch festhalten
	step := ""
	started := false
	fail := func(e error) error {
		msg := certSafeText(e.Error())
		var rl *acme.RateLimited
		if errors.As(e, &rl) {
			until := rl.Until
			if until.IsZero() {
				until = d.now().Add(time.Hour)
			}
			_ = d.store.certSetRetry(sctx, name, until)
		}
		_ = d.store.certSetStep(sctx, name, certStateFehler, "", msg)
		lg.Warn().Str("name", name).Str("schritt", step).Str("fehler", msg).Msg("zertifikat holen fehlgeschlagen")
		return e
	}
	defer func() {
		if p := recover(); p != nil {
			lg.Error().Str("name", name).Str("schritt", step).Str("panik", certSafeText(fmt.Sprint(p))).Msg("auftrag abgebrochen")
			e := errors.New("Auftrag mit internem Fehler abgebrochen")
			if started {
				err = fail(e)
			} else {
				err = e
			}
		}
	}()

	if d.blocked() {
		return errors.New("Am Spiegel und während der Wartung werden keine Zertifikate geholt")
	}
	rows, err := d.store.certList(ctx)
	if err != nil {
		return err
	}
	var row *certRow
	for i := range rows {
		if rows[i].Name == name {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return fmt.Errorf("%s ist nicht eingetragen", name)
	}
	if !row.RetryAfter.IsZero() && d.now().Before(row.RetryAfter) {
		return fmt.Errorf("Let's Encrypt nimmt für %s erst ab %s wieder Aufträge an", name,
			row.RetryAfter.In(time.Local).Format("02.01.2006 15:04"))
	}

	started = true
	step = "start"
	if err := d.store.certSetStep(sctx, name, certStateLaeuft, step, ""); err != nil {
		return err
	}
	lg.Info().Str("name", name).Bool("testumgebung", row.Staging).Msg("zertifikat wird geholt")

	dns, err := d.dns()
	if err != nil {
		return fail(err)
	}
	// Ein gemerkter Prüf-Eintrag (abgebrochener Auftrag, gescheitertes Löschen) wird vor
	// dem neuen Auftrag gelöscht – nie ein neuer Eintrag über einem alten.
	if row.ChallengeID != "" {
		step = "aufraeumen"
		if err := certRemoveChallenge(ctx, dns, name, row.ChallengeID); err != nil {
			return fail(fmt.Errorf("Alter Prüf-Eintrag für %s ließ sich nicht löschen, deshalb kein neuer Auftrag: %w", name, err))
		}
		_ = d.store.certSetChallenge(sctx, name, "")
		lg.Info().Str("name", name).Msg("übrig gebliebenen prüf-eintrag gelöscht")
	}
	ca, err := d.ca(row.Staging)
	if err != nil {
		return fail(err)
	}
	onStep := func(s string) {
		step = s
		_ = d.store.certSetStep(sctx, name, certStateLaeuft, s, "")
		lg.Debug().Str("name", name).Str("schritt", s).Msg("zertifikat")
	}
	rd := &certRememberDNS{DNS: dns, ctx: sctx, store: d.store, name: name}
	is, err := acme.Issue(ctx, ca, rd, name, onStep, certWait)
	if err != nil {
		return fail(err) // eine nicht gelöschte Kennung bleibt gemerkt
	}
	step = "speichern"
	err = d.store.certSaveIssued(sctx, name, is)
	if rd.pending != "" {
		// Löschen gescheitert: certSaveIssued hat challenge_id geleert – wieder merken,
		// damit der tägliche Lauf den Eintrag findet.
		_ = d.store.certSetChallenge(sctx, name, rd.pending)
	}
	if err != nil {
		return fail(fmt.Errorf("Zertifikat speichern: %w", err))
	}
	lg.Info().Str("name", name).Str("fingerabdruck", is.Fingerprint).Time("gueltig_bis", is.NotAfter).Msg("zertifikat geholt")

	step = "einrichten"
	return certInstall(sctx, d, name, is.ChainPEM, is.KeyPEM, is.Fingerprint, certStateGueltig, "")
}

// certInstall gibt Zertifikat und Schlüssel dem Agenten und hält das Ergebnis fest; der
// Zustand bleibt state, last_error wird bei Erfolg keepErr. Bei einem Fehler bleibt das
// Zertifikat gespeichert und last_error beginnt mit „noch nicht eingerichtet“. Einen Hinweis
// des Agenten (Dateien abgelegt, nginx-Seite von Hand) merkt sich die Zeile als Hinweis –
// eingerichtet ist das Zertifikat trotzdem; ohne Hinweis verschwindet ein alter.
func certInstall(ctx context.Context, d certDeps, name string, chain, key []byte, wantFP, state, keepErr string) error {
	lg := componentLog("zertifikate")
	fp, note, err := d.install(ctx, name, chain, key)
	if err == nil && !strings.EqualFold(strings.TrimSpace(fp), wantFP) {
		err = errors.New("der Update-Agent meldet einen anderen Fingerabdruck")
	}
	if err != nil {
		msg := certNotInstalled + ": " + certSafeText(err.Error())
		_ = d.store.certSetStep(ctx, name, state, "", msg)
		lg.Warn().Str("name", name).Str("fingerabdruck", wantFP).Str("fehler", msg).Msg("zertifikat nicht eingerichtet")
		return errors.New(msg)
	}
	_ = d.store.certSetInstalled(ctx, name, strings.ToLower(wantFP))
	_ = d.store.certSetStep(ctx, name, state, "", keepErr)
	note = certSafeText(strings.TrimSpace(note))
	if d.getSetting(ctx, keyCertNotePrefix+name) != note {
		_ = d.setSetting(ctx, keyCertNotePrefix+name, note)
	}
	if note != "" {
		lg.Info().Str("name", name).Str("fingerabdruck", wantFP).Str("hinweis", note).Msg("zertifikat abgelegt, nginx-seite von hand")
		return nil
	}
	lg.Info().Str("name", name).Str("fingerabdruck", wantFP).Msg("zertifikat eingerichtet")
	return nil
}

// ── Fälligkeit und Alarme ────────────────────────────────────

func certHasCert(r certRow) bool {
	return !r.NotAfter.IsZero() && (r.ChainPEM != "" || r.Fingerprint != "")
}

// certDue: Namen, die jetzt geholt werden – nie geholt oder ab 30 Tagen vor Ablauf (bei
// kurzer Laufzeit ein Drittel davor); nicht während eines Auftrags und nicht vor Ende einer Sperrzeit.
func certDue(rows []certRow, now time.Time) []string {
	var out []string
	for _, r := range rows {
		if r.State == certStateLaeuft {
			continue
		}
		if !r.RetryAfter.IsZero() && now.Before(r.RetryAfter) {
			continue
		}
		if !certHasCert(r) || !now.Before(acme.RenewAt(r.NotBefore, r.NotAfter)) {
			out = append(out, r.Name)
		}
	}
	return out
}

// certAlarm: Text an die Administratoren, wenn ein Zertifikat in höchstens 14 Tagen abläuft
// oder abgelaufen ist. Wie oft gemeldet wird, entscheidet certDaily.
func certAlarm(row certRow, now time.Time) (text string, ok bool) {
	return certAlarmAt(row, row.NotAfter, false, now)
}

// certAlarmAt: wie certAlarm, aber für den Ablauf at. served: at stammt von der Datei, die nginx
// noch ausliefert, weil das erneuerte Zertifikat (noch) nicht eingerichtet ist.
func certAlarmAt(row certRow, at time.Time, served bool, now time.Time) (text string, ok bool) {
	if !certHasCert(row) || at.IsZero() {
		return "", false
	}
	until := at.In(now.Location()).Format("02.01.2006 15:04")
	where := " Server-Einstellungen → Cloudflare-Tunnel → Zertifikate."
	if row.LastError != "" {
		where = " Letzter Fehler: " + certSafeText(row.LastError) + "." + where
	}
	what, failed := "Das Zertifikat", "Die Erneuerung hat nicht geklappt."
	if served {
		what, failed = "Das eingerichtete Zertifikat", "Das erneuerte Zertifikat ist auf dem Server noch nicht eingerichtet."
	}
	left := at.Sub(now)
	if left <= 0 {
		return fmt.Sprintf("%s für %s ist abgelaufen (seit %s). %s%s", what, row.Name, until, failed, where), true
	}
	days := int(left / (24 * time.Hour)) // volle Tage
	if days > 14 {
		return "", false
	}
	var when string
	switch days {
	case 0:
		when = "läuft in weniger als einem Tag ab"
	case 1:
		when = "läuft in 1 Tag ab"
	default:
		when = fmt.Sprintf("läuft in %d Tagen ab", days)
	}
	if served {
		return fmt.Sprintf("%s für %s %s (gültig bis %s). %s%s", what, row.Name, when, until, failed, where), true
	}
	return fmt.Sprintf("Das Zertifikat für %s %s (gültig bis %s). Die automatische Erneuerung hat bisher nicht geklappt.%s", row.Name, when, until, where), true
}

// certPendingAfter: So lange darf ein erneuertes Zertifikat ohne Rückmeldung des Agenten
// uneingerichtet bleiben, bevor die Administratoren eine Nachricht bekommen. Gemessen ab
// not_before; Let's Encrypt setzt den Beginn eine Stunde vor die Ausstellung.
const certPendingAfter = 25 * time.Hour

// certPendingNote: Text, wenn der Stand beim Agenten nicht abrufbar ist und ein erneuertes
// Zertifikat seit mehr als 24 Stunden nicht eingerichtet ist (das bisher eingerichtete läuft
// sonst still ab). Nur für Namen, die schon einmal eingerichtet waren.
func certPendingNote(row certRow, statusErr string, now time.Time) (string, bool) {
	if !certHasCert(row) || row.Fingerprint == "" || row.InstalledFP == "" || strings.EqualFold(row.InstalledFP, row.Fingerprint) {
		return "", false
	}
	if row.NotBefore.IsZero() || now.Sub(row.NotBefore) <= certPendingAfter {
		return "", false
	}
	why := strings.TrimSpace(strings.TrimPrefix(row.LastError, certNotInstalled+":"))
	if why == "" {
		why = statusErr
	}
	if why == "" {
		why = "der Update-Agent antwortet nicht"
	}
	return fmt.Sprintf("Das Zertifikat für %s ist erneuert, aber noch nicht eingerichtet: %s. Das bisher eingerichtete läuft ab, wenn das so bleibt. Server-Einstellungen → Cloudflare-Tunnel → Zertifikate.",
		row.Name, certSafeText(why)), true
}

// certAlarmDue: soll der Alarm heute (noch) gemeldet werden? Einmal ab 14 Tagen, ab 7 Tagen
// täglich, abgelaufen sofort und danach täglich. Gemerkt: Ablaufdatum|Tag|Stufe.
func certAlarmDue(row certRow, now time.Time, stored string) (mark string, send bool) {
	today := now.Format("2006-01-02")
	left := row.NotAfter.Sub(now)
	stage := "14"
	switch {
	case left <= 0:
		stage = "ab"
	case left < 8*24*time.Hour:
		stage = "7"
	}
	mark = strconv.FormatInt(row.NotAfter.Unix(), 10) + "|" + today + "|" + stage
	p := strings.Split(stored, "|")
	if len(p) != 3 || p[0] != strconv.FormatInt(row.NotAfter.Unix(), 10) {
		return mark, true // für dieses Zertifikat noch nichts gemeldet
	}
	switch stage {
	case "14":
		return mark, false
	case "7":
		return mark, p[1] != today
	default:
		return mark, p[1] != today || p[2] != "ab"
	}
}

// ── Täglicher Lauf ───────────────────────────────────────────

// certChallengeLister: ein DNS-Zugang, der die Einträge der Zone nennen kann (Cloudflare).
type certChallengeLister interface {
	challengeRecords(ctx context.Context) ([]cloudflare.Record, error)
}

// certRemoveChallenge löscht einen gemerkten Prüf-Eintrag eines Namens. Mit Cloudflare-Zugang
// nur, wenn er noch als TXT _acme-challenge.<name> in der Zone steht (wie certCleanLeftovers);
// steht er dort nicht mehr, gilt er als gelöscht.
func certRemoveChallenge(ctx context.Context, dns acme.DNS, name, id string) error {
	if l, ok := dns.(certChallengeLister); ok {
		recs, err := l.challengeRecords(ctx)
		if err != nil {
			return err
		}
		if len(certLeftovers(recs, map[string]string{id: "_acme-challenge." + name})) == 0 {
			return nil
		}
	}
	return dns.CleanUp(ctx, id)
}

func (d *certDNS) challengeRecords(ctx context.Context) ([]cloudflare.Record, error) {
	return d.api.Records(ctx, d.zoneID)
}

// certStep führt einen Teil des täglichen Laufs aus; eine Panik darin landet im Protokoll
// und beendet weder den Lauf noch das PDH.
func certStep(what string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			componentLog("zertifikate").Error().Str("teil", what).Str("panik", certSafeText(fmt.Sprint(p))).
				Msg("tägliche prüfung: teil mit internem fehler abgebrochen")
		}
	}()
	fn()
}

// certDaily: hängende Aufträge lösen, übrige Prüf-Einträge löschen, fällige Namen holen,
// Datenbank und Dateien abgleichen, Alarme melden. Am Spiegel und in der Wartung nichts.
// Eine Panik in einem Teil wird protokolliert; die übrigen Teile laufen weiter.
func certDaily(ctx context.Context, d certDeps) {
	d = d.withDefaults()
	lg := componentLog("zertifikate")
	defer func() {
		if p := recover(); p != nil {
			lg.Error().Str("panik", certSafeText(fmt.Sprint(p))).Msg("tägliche prüfung mit internem fehler abgebrochen")
		}
	}()
	if d.blocked() {
		lg.Debug().Msg("tägliche prüfung ausgelassen (spiegel oder wartung)")
		return
	}
	rows, err := d.store.certList(ctx)
	if err != nil {
		lg.Warn().Err(err).Msg("zertifikate lesen")
		return
	}
	if len(rows) == 0 {
		lg.Debug().Msg("keine zertifikate eingetragen")
		return
	}

	// 1. Ein Eintrag in „laeuft“, den kein Auftrag hält, stammt aus einem Absturz oder Neustart.
	for _, r := range rows {
		if r.State != certStateLaeuft || !certTryLock(r.Name) {
			continue
		}
		_ = d.store.certSetStep(ctx, r.Name, certStateFehler, "", "Auftrag wurde unterbrochen (Neustart oder Rollentausch) – wird erneut versucht")
		certUnlock(r.Name)
		lg.Warn().Str("name", r.Name).Str("schritt", r.Step).Msg("unterbrochener auftrag zurückgesetzt")
	}

	// 2. Gemerkte Prüf-Einträge löschen.
	if rows, err = d.store.certList(ctx); err != nil {
		lg.Warn().Err(err).Msg("zertifikate lesen")
		return
	}
	certStep("prüf-einträge", func() { certCleanLeftovers(ctx, d, rows) })

	// 3. Fällige Namen holen.
	for _, name := range certDue(rows, d.now()) {
		if d.blocked() {
			return
		}
		certStep("holen", func() { _ = certIssue(ctx, d, name) }) // Fehler stehen in last_error und im Protokoll
	}

	// 4. Datenbank und Dateien abgleichen.
	if rows, err = d.store.certList(ctx); err != nil {
		lg.Warn().Err(err).Msg("zertifikate lesen")
		return
	}
	// Bricht der Abgleich ab, ist der Stand beim Agenten unbekannt, nicht „nicht abrufbar“:
	// dann keine Meldung „erneuert, aber noch nicht eingerichtet“.
	rec := certReconciled{statusOK: true}
	certStep("abgleich", func() { rec = certReconcile(ctx, d, rows) })
	if again, err := d.store.certList(ctx); err == nil {
		rows = again // last_error und installed_fp nach dem Abgleich
	}

	// 5. Alarme.
	certStep("alarme", func() { certDailyAlarms(ctx, d, rows, rec) })

	// 6. Namen im Haus (UniFi): nur lesen, Abweichungen als Hinweis in die Zeile, kein Alarm.
	certStep("namen im haus", func() { certLANDaily(ctx, d, rows) })
}

// certDailyAlarms: Ablauf-Alarme nach dem früheren Ablauf aus Datenbank und eingerichteter
// Datei; ist der Stand beim Agenten nicht abrufbar, einmal am Tag „erneuert, aber noch nicht
// eingerichtet“, wenn das länger als 24 Stunden so ist.
func certDailyAlarms(ctx context.Context, d certDeps, rows []certRow, rec certReconciled) {
	lg := componentLog("zertifikate")
	now := d.now()
	for _, r := range rows {
		at, served := r.NotAfter, false
		if f, ok := rec.served[r.Name]; ok && !f.IsZero() && f.Before(r.NotAfter) {
			at, served = f, true
		}
		if text, ok := certAlarmAt(r, at, served, now); ok {
			key := keyCertAlarmPrefix + r.Name
			eff := r
			eff.NotAfter = at
			if mark, send := certAlarmDue(eff, now, d.getSetting(ctx, key)); send {
				d.alarm(text)
				_ = d.setSetting(ctx, key, mark)
				lg.Warn().Str("name", r.Name).Time("gueltig_bis", at).Bool("datei", served).Msg("alarm: zertifikat läuft ab")
			}
		}
		if rec.statusOK {
			continue
		}
		text, ok := certPendingNote(r, rec.statusErr, now)
		if !ok {
			continue
		}
		key := keyCertPendingPrefix + r.Name
		mark := strings.ToLower(r.Fingerprint) + "|" + now.Format("2006-01-02")
		if d.getSetting(ctx, key) == mark {
			continue
		}
		d.alarm(text)
		_ = d.setSetting(ctx, key, mark)
		lg.Warn().Str("name", r.Name).Str("fingerabdruck", r.Fingerprint).Msg("alarm: erneuertes zertifikat nicht eingerichtet")
	}
}

// certCleanLeftovers löscht Prüf-Einträge, die das PDH angelegt und sich gemerkt hat, deren
// Auftrag aber nicht mehr läuft. Fremde Einträge bleiben unberührt.
func certCleanLeftovers(ctx context.Context, d certDeps, rows []certRow) {
	lg := componentLog("zertifikate")
	remembered := map[string]string{} // Kennung → _acme-challenge.<name>
	owner := map[string]string{}      // Kennung → Name
	for _, r := range rows {
		if r.ChallengeID == "" || !certTryLock(r.Name) {
			continue
		}
		defer certUnlock(r.Name)
		remembered[r.ChallengeID] = "_acme-challenge." + r.Name
		owner[r.ChallengeID] = r.Name
	}
	if len(owner) == 0 {
		return
	}
	dns, err := d.dns()
	if err != nil {
		lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("prüf-einträge nicht aufgeräumt")
		return
	}
	ids := make([]string, 0, len(owner))
	gone := map[string]bool{}
	if l, ok := dns.(certChallengeLister); ok {
		recs, err := l.challengeRecords(ctx)
		if err != nil {
			lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("prüf-einträge nicht aufgeräumt")
			return
		}
		ids = certLeftovers(recs, remembered)
		present := map[string]bool{}
		for _, id := range ids {
			present[id] = true
		}
		for id := range owner {
			if !present[id] {
				gone[id] = true // steht nicht mehr in der Zone
			}
		}
	} else {
		for id := range owner {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := dns.CleanUp(ctx, id); err != nil {
			lg.Warn().Str("name", owner[id]).Str("fehler", certSafeText(err.Error())).Msg("prüf-eintrag nicht gelöscht")
			continue
		}
		gone[id] = true
		lg.Info().Str("name", owner[id]).Msg("übrig gebliebenen prüf-eintrag gelöscht")
	}
	for id := range gone {
		_ = d.store.certSetChallenge(ctx, owner[id], "")
	}
}

// certReconciled: Ergebnis des Abgleichs für die Alarme.
type certReconciled struct {
	statusOK  bool
	statusErr string
	// served: Name → Ablauf der Datei, die beim Agenten liegt, solange sie nicht die aus der
	// Datenbank ist (Einrichten gescheitert). nginx liefert dann diese aus.
	served map[string]time.Time
}

// certReconcile vergleicht den Fingerabdruck in der Datenbank mit der Datei beim Agenten
// und installiert bei Abweichung neu aus der Datenbank. Für den Namen des PDH und der
// Nextcloud gilt auch „Datei stimmt, aber keine nginx-Seite verweist darauf“ (nach einem
// Rollentausch oder Rückspielen) als Abweichung – der Agent richtet die Seite dann ein.
func certReconcile(ctx context.Context, d certDeps, rows []certRow) certReconciled {
	lg := componentLog("zertifikate")
	out := certReconciled{served: map[string]time.Time{}}
	var have []certRow
	for _, r := range rows {
		if certHasCert(r) && r.Fingerprint != "" && r.State != certStateLaeuft {
			have = append(have, r)
		}
	}
	if len(have) == 0 {
		out.statusOK = true
		return out
	}
	files, err := d.status(ctx)
	out.statusOK = err == nil
	if err != nil {
		out.statusErr = certSafeText(err.Error())
		lg.Warn().Str("fehler", out.statusErr).Msg("stand der zertifikatsdateien nicht abrufbar")
	}
	for _, r := range have {
		f, present := files[r.Name]
		var match, noSite bool
		if out.statusOK {
			match = present && strings.EqualFold(f.Fingerprint, r.Fingerprint)
			if match && !f.Site {
				role := d.role(ctx, r.Name)
				noSite = role == "pdh" || role == "nextcloud"
			}
		} else {
			match = strings.EqualFold(r.InstalledFP, r.Fingerprint)
		}
		if match && !noSite {
			if out.statusOK && !strings.EqualFold(r.InstalledFP, r.Fingerprint) {
				_ = d.store.certSetInstalled(ctx, r.Name, strings.ToLower(r.Fingerprint))
			}
			continue
		}
		if !match && present && !f.NotAfter.IsZero() {
			out.served[r.Name] = f.NotAfter
		}
		if !certTryLock(r.Name) {
			continue
		}
		func() {
			defer certUnlock(r.Name)
			chain, key, err := d.store.certKey(ctx, r.Name)
			if err != nil {
				lg.Warn().Str("name", r.Name).Str("fehler", certSafeText(err.Error())).Msg("zertifikat nicht lesbar")
				return
			}
			if noSite {
				lg.Info().Str("name", r.Name).Msg("keine nginx-seite verweist auf das zertifikat – neu einrichten")
			} else {
				lg.Info().Str("name", r.Name).Str("fingerabdruck", r.Fingerprint).Str("datei", f.Fingerprint).Msg("datei weicht ab – neu einrichten")
			}
			keep := r.LastError // z. B. eine gescheiterte Erneuerung bleibt sichtbar
			if strings.HasPrefix(keep, certNotInstalled) {
				keep = ""
			}
			if certInstall(ctx, d, r.Name, chain, key, r.Fingerprint, r.State, keep) == nil {
				delete(out.served, r.Name)
			}
		}()
	}
	return out
}

// ── Betrieb ──────────────────────────────────────────────────

// certRole: welche Seite der Agent für den Namen schreibt – "pdh" (eigene Seite für den
// PDH-Namen), "nextcloud" (Zertifikatszeilen der Nextcloud-Seite) oder "" (nur Dateien).
func certRole(name, pdhHost, nextcloudURL string) string {
	norm := func(s string) string {
		s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
		if h, _, err := net.SplitHostPort(s); err == nil {
			s = h
		}
		return s
	}
	n := norm(name)
	if n == "" {
		return ""
	}
	if n == norm(pdhHost) {
		return "pdh"
	}
	if u, err := url.Parse(strings.TrimSpace(nextcloudURL)); err == nil && u.Hostname() != "" && n == norm(u.Hostname()) {
		return "nextcloud"
	}
	return ""
}

var certUpstreamRe = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost):([0-9]{1,5})$`)

// certUpstreamPort: Port für die nginx-Seite des PDH – nie 80 oder 443 (die gehören nginx
// selbst, die Seite zeigte sonst auf sich selbst).
func certUpstreamPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && n != 80 && n != 443
}

// certUpstream: Adresse, an die die nginx-Seite des PDH weiterreicht (nur lokal). Zeigt das
// Tunnel-Ziel auf nginx (Port 80/443) oder woandershin, gilt der Port des PDH (PDH_SERVER_PORT).
func certUpstream(origin, serverPort string) string {
	o := strings.TrimRight(strings.TrimSpace(origin), "/")
	if m := certUpstreamRe.FindStringSubmatch(o); m != nil && certUpstreamPort(m[2]) {
		return o
	}
	if p := strings.TrimSpace(serverPort); certUpstreamPort(p) {
		return "http://127.0.0.1:" + p
	}
	return certUpstreamDef
}

// certRedirect: Soll die nginx-Seite des PDH Port 80 auf https umleiten? Nicht, wenn der Tunnel
// das PDH über nginx Port 80 dieses Servers erreicht – das gäbe eine Weiterleitungsschleife.
func certRedirect(origin string) bool {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return true
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return true
	}
	return !(u.Port() == "" || u.Port() == "80")
}

// certInstallBody: Anfrage an POST /v1/certs/install – Rolle aus PDH-Name bzw.
// Nextcloud-Adresse, Weiterleitung und Ziel nur für die Seite des PDH.
func certInstallBody(name string, chain, key []byte, pdhHost, origin, serverPort, nextcloudURL string) map[string]any {
	role := certRole(name, pdhHost, nextcloudURL)
	body := map[string]any{"name": name, "chain_pem": string(chain), "key_pem": string(key), "role": role}
	if role == "pdh" {
		body["upstream"] = certUpstream(origin, serverPort)
		body["redirect"] = certRedirect(origin)
	}
	return body
}

// certRemoveBody: Anfrage an POST /v1/certs/remove. Für einen Nextcloud-Namen ohne gemerkte
// Zertifikatszeilen (die Seite wurde nie umgestellt) geht Rolle "" – der Agent entfernt dann
// die Dateien, wenn keine Seite mehr darauf verweist.
func certRemoveBody(name, pdhHost, nextcloudURL, prevCert, prevKey string, havePrev bool) map[string]string {
	role := certRole(name, pdhHost, nextcloudURL)
	body := map[string]string{"name": name, "role": role}
	if role == "nextcloud" && !havePrev {
		body["role"] = ""
	}
	if havePrev {
		body["restore_cert"], body["restore_key"] = prevCert, prevKey
	}
	return body
}

func (h *Handler) certSetting(ctx context.Context, key string) string {
	if h.db == nil {
		return ""
	}
	return h.getUpdateSetting(ctx, key, "")
}

func (h *Handler) certPutSetting(ctx context.Context, key, value string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	return h.setUpdateSetting(ctx, key, value)
}

// certPrevious: Pfade der Zertifikatszeilen, die in der Nextcloud-Seite vor der ersten
// Umstellung standen (für „Entfernen“). ok=false, wenn nichts gemerkt ist.
func (h *Handler) certPrevious(ctx context.Context, name string) (cert, key string, ok bool) {
	return certPrevDecode(h.certSetting(ctx, keyCertPrevPrefix+name))
}

type certPrev struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

func certPrevDecode(raw string) (cert, key string, ok bool) {
	var p certPrev
	if raw == "" || json.Unmarshal([]byte(raw), &p) != nil || p.Cert == "" {
		return "", "", false
	}
	return p.Cert, p.Key, true
}

// certKeepPrevious merkt sich die vorigen Zertifikatszeilen, wenn der Agent sie meldet (nur
// beim ersten Umstellen). Eine leere Meldung (Erneuerung) überschreibt nie den gemerkten Stand.
func certKeepPrevious(ctx context.Context, put func(ctx context.Context, key, value string) error, name, cert, key string) error {
	if strings.TrimSpace(cert) == "" {
		return nil
	}
	raw, err := json.Marshal(certPrev{Cert: cert, Key: key})
	if err != nil {
		return err
	}
	return put(ctx, keyCertPrevPrefix+name, string(raw))
}

// certDepsLive: die echten Teile – Let's Encrypt, Cloudflare, Update-Agent, Chat.
func (h *Handler) certDepsLive() certDeps {
	return certDeps{
		store: h,
		ca: func(staging bool) (acme.CA, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			key, err := h.certAccountKey(ctx, staging)
			if err != nil {
				return nil, err
			}
			return acme.NewLetsEncrypt(key, h.certSetting(ctx, keyCertEmail), staging)
		},
		dns: func() (acme.DNS, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a, err := h.cfAccess(ctx)
			if err != nil {
				return nil, err
			}
			zone, err := a.client.ZoneByName(ctx, a.zone)
			if err != nil {
				return nil, err
			}
			return &certDNS{api: a.client, zoneID: zone.ID, lookupTXT: authoritativeTXT(a.zone)}, nil
		},
		install: func(ctx context.Context, name string, chain, key []byte) (string, string, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			host, _ := h.nextStartValue(ctx, cfHostKey)
			origin, _ := h.nextStartValue(ctx, cfOriginKey)
			// der Port, auf dem das PDH gerade läuft – ein geänderter gilt erst nach dem Neustart
			port := os.Getenv("PDH_SERVER_PORT")
			if port == "" {
				port, _ = h.nextStartValue(ctx, "PDH_SERVER_PORT")
			}
			body := certInstallBody(name, chain, key, host, origin, port, os.Getenv("PDH_NEXTCLOUD_BASEURL"))
			var out struct {
				Fingerprint  string `json:"fingerprint"`
				Note         string `json:"note"`
				PreviousCert string `json:"previous_cert"`
				PreviousKey  string `json:"previous_key"`
			}
			if err := h.agentJSON(ctx, "POST", "/v1/certs/install", body, &out); err != nil {
				return "", "", err
			}
			if err := certKeepPrevious(context.WithoutCancel(ctx), h.certPutSetting, name, out.PreviousCert, out.PreviousKey); err != nil {
				componentLog("zertifikate").Warn().Str("name", name).Err(err).Msg("vorige zertifikatszeilen nicht gemerkt")
			}
			return out.Fingerprint, out.Note, nil
		},
		status: func(ctx context.Context) (map[string]certFile, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var out struct {
				Certs []struct {
					Name        string `json:"name"`
					Fingerprint string `json:"fingerprint"`
					NotAfter    string `json:"not_after"`
					Site        bool   `json:"site"`
				} `json:"certs"`
			}
			if err := h.agentJSON(ctx, "GET", "/v1/certs/status", nil, &out); err != nil {
				return nil, err
			}
			m := make(map[string]certFile, len(out.Certs))
			for _, c := range out.Certs {
				f := certFile{Fingerprint: strings.ToLower(c.Fingerprint), Site: c.Site}
				if t, err := time.Parse(time.RFC3339, c.NotAfter); err == nil {
					f.NotAfter = t
				}
				m[c.Name] = f
			}
			return m, nil
		},
		role: func(ctx context.Context, name string) string {
			host, _ := h.nextStartValue(ctx, cfHostKey)
			return certRole(name, host, os.Getenv("PDH_NEXTCLOUD_BASEURL"))
		},
		blocked: func() bool { return h.Mirror().On || h.MaintLocked() },
		alarm: func(text string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			h.notifyAdmins(ctx, text)
		},
		now:        time.Now,
		getSetting: h.certSetting,
		setSetting: h.certPutSetting,
		lanCheck:   h.certLANCheckLive,
	}
}

// certTimes: Zeitplan des Hintergrundlaufs.
type certTimes struct {
	first     time.Duration // erster Lauf nach dem Start
	every     time.Duration // danach
	afterRole time.Duration // nach einem Wechsel vom Spiegel zu aktiv
	poll      time.Duration // wie oft die Rolle angesehen wird
	retry     time.Duration // nach einem ausgelassenen Lauf (Wartung)
}

// certSchedule ruft run im Takt auf; run meldet false, wenn der Lauf ausgelassen wurde
// (dann bald erneut). Am Spiegel läuft nichts; nach einem Wechsel zu aktiv bald ein Lauf.
func certSchedule(ctx context.Context, t certTimes, mirror func() bool, run func() bool) {
	if t.retry <= 0 {
		t.retry = 10 * time.Minute
	}
	timer := time.NewTimer(t.first)
	defer timer.Stop()
	poll := time.NewTicker(t.poll)
	defer poll.Stop()
	wasMirror := mirror()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			m := mirror()
			if wasMirror && !m {
				timer.Reset(t.afterRole)
			}
			wasMirror = m
		case <-timer.C:
			if mirror() || !run() {
				timer.Reset(t.retry)
				continue
			}
			timer.Reset(t.every)
		}
	}
}

// StartCertRenewal: tägliche Prüfung der Zertifikate – erster Lauf 2 Minuten nach dem
// Start, dann alle 24 Stunden, nach einem Wechsel vom Spiegel zu aktiv nach 1 Minute.
// Ohne Datenbank startet nichts; ohne eingetragene Namen tut der Lauf nichts.
func (h *Handler) StartCertRenewal(ctx context.Context) {
	if h.db == nil {
		return
	}
	go certSchedule(ctx, certTimes{first: 2 * time.Minute, every: 24 * time.Hour, afterRole: time.Minute, poll: 30 * time.Second},
		func() bool { return h.Mirror().On },
		func() bool {
			if h.maintSkip("zertifikate") {
				return false
			}
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			certDaily(runCtx, h.certDepsLive())
			return true
		})
}
