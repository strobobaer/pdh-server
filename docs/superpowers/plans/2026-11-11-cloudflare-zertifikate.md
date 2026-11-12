# Cloudflare Stufe 2 (Zertifikate von Let's Encrypt) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Das PDH holt und erneuert Let's-Encrypt-Zertifikate per DNS-01 über Cloudflare, nginx bedient damit PDH und Nextcloud auf Port 443, und der Tunnel lässt sich auf „Full (strict)“ umstellen.

**Architecture:** Der Ablauf (Auftrag, Prüf-Eintrag, Abholen) liegt als reines Paket `internal/integrations/acme` hinter zwei Schnittstellen (Zertifizierungsstelle, DNS). `internal/web` speichert verschlüsselt, plant die Erneuerung und zeigt die Karte; der Update-Agent (root) legt Dateien ab, schreibt nginx-Seiten und lädt nginx neu. Spiegelung und Sicherung nehmen `/etc/ssl/pdh` als weitere Quelle mit.

**Tech Stack:** Go, `golang.org/x/crypto/acme` (im Modul vorhanden), vorhandene Pakete `internal/integrations/cloudflare`, `internal/ha`, Update-Agent, PostgreSQL-Migration, Go-Templates.

**Spec:** `docs/superpowers/specs/2026-11-11-cloudflare-zertifikate-design.md`

## Global Constraints

- Sprache: Oberfläche, Handbuch, Änderungsprotokoll, Commit-Nachrichten auf Deutsch, für Anwender im Betrieb.
- Version `0.77.10`, eigener Eintrag in `CHANGELOG.md`; Migration `157`. Vor dem Merge gegen `origin/main` prüfen (AGENTS.md „Prüfung vor jedem Merge“) und bei Bedarf umnummerieren.
- Je Name ein Zertifikat, Schlüssel ECDSA P-256, bei jeder Erneuerung neu. Keine Sammelzertifikate.
- Erneuerung 30 Tage vor Ablauf; Alarm ab 14 Tage, täglich ab 7 Tagen, sofort bei Ablauf.
- Prüf-Eintrag `_acme-challenge.<name>`, Warten auf Sichtbarkeit höchstens 2 Minuten, Eintrag wird immer gelöscht.
- Dateien: `/etc/ssl/pdh/<name>/fullchain.pem` und `privkey.pem` (root, Gruppe des nginx-Benutzers, Schlüssel 0640).
- Am Spiegel (`h.Mirror().On`) und in der Wartung (`h.MaintLocked()`) startet nichts.
- Produktion ist systemd; im Docker-Betrieb lehnt der Agent ab.
- Neues Recht `system.certs`; „Entfernen“ nur Bereichsadministrator; nie Schlüssel oder Token im Protokoll.
- Farben, Ecken, Schatten nur über CSS-Variablen; Fiori und Fluent, hell und dunkel prüfen.
- Tests auf dem Windows-PC über Git Bash starten (`go test ./...`).

## Review Focus

1. **Name in ungewohnter Schreibweise** (Großbuchstaben, Punkt am Ende, `*.firma.de`, Name außerhalb der Domain): wird klein und ohne Punkt gespeichert; Sammelnamen und fremde Domains lehnt die Karte ab. → Test in Task 4.
2. **Zertifikat mit kurzer Laufzeit** (unter 90 Tagen): Erneuerung beginnt bei einem Drittel Restlaufzeit, höchstens 30 Tage vorher – sonst erneuert ein 6-Tage-Zertifikat täglich oder nie. → Test in Task 1.
3. **Nextcloud-Seite anders als erwartet** (keine oder mehrere `ssl_certificate`-Zeilen, mehrere `server`-Blöcke): Der Agent ändert nichts und meldet es, statt die Seite zu zerstören. → Test in Task 5.
4. **Zwei Aufträge für denselben Namen gleichzeitig** (Doppelklick, Knopf während der täglichen Prüfung): Der zweite meldet „läuft schon“. → Test in Task 6.
5. **Vorhandener DNS-Eintrag an der UDM Pro** unter einem geführten Namen, den jemand von Hand angelegt hat: Das PDH überschreibt ihn nur nach Rückfrage und löscht nie einen Eintrag, den es nicht selbst angelegt hat. → Test in Task 8a.
6. **Fremder `_acme-challenge`-Eintrag** eines anderen Programms unter einem geführten Namen: Das Aufräumen löscht nur Einträge, deren Kennung das PDH selbst angelegt und sich gemerkt hat. → Test in Task 2.

---

### Task 1: Paket `acme` – Ablauf hinter Schnittstellen

**Files:**
- Create: `internal/integrations/acme/acme.go`, `internal/integrations/acme/le.go`
- Test: `internal/integrations/acme/acme_test.go`

**Interfaces:**
- Produces:
  - `type DNS interface { Present(ctx context.Context, fqdn, value string) (id string, err error); CleanUp(ctx context.Context, id string) error; Visible(ctx context.Context, fqdn, value string) (bool, error) }`
  - `type Order interface { Challenge() (fqdn, value string); Accept(ctx context.Context) error; Finalize(ctx context.Context, csrDER []byte) (chainPEM []byte, err error) }`
  - `type CA interface { NewOrder(ctx context.Context, name string) (Order, error); Revoke(ctx context.Context, certPEM []byte) error }`
  - `type Issued struct { ChainPEM, KeyPEM []byte; NotBefore, NotAfter time.Time; Fingerprint string }`
  - `type RateLimited struct { Until time.Time; Msg string }` (erfüllt `error`)
  - `func Issue(ctx context.Context, ca CA, dns DNS, name string, step func(string), wait func(context.Context, time.Duration) error) (*Issued, error)`
  - `func RenewAt(notBefore, notAfter time.Time) time.Time`
  - `func NormalizeName(s string) (string, error)`
  - `func NewLetsEncrypt(accountKeyPEM []byte, email string, staging bool) (CA, error)` und `func NewAccountKey() ([]byte, error)` in `le.go`
  - Schritt-Namen für `step`: `"auftrag"`, `"dns"`, `"warten"`, `"pruefung"`, `"abholen"`.

- [ ] **Step 1: Tests schreiben** (`acme_test.go`, mit `fakeCA`, `fakeDNS` im Speicher und einem `wait`, das sofort zurückkehrt)

```go
func TestIssueHappyPath(t *testing.T)        // Reihenfolge der Aufrufe: NewOrder, Present, Visible(true), Accept, Finalize, CleanUp; Issued.KeyPEM ist ECDSA P-256; step sah auftrag,dns,warten,pruefung,abholen
func TestIssueCleansUpOnEveryFailure(t *testing.T) // je ein Lauf mit Fehler in Present, Visible, Accept, Finalize: CleanUp genau einmal, sobald Present gelang; Fehler wird durchgereicht
func TestIssueGivesUpAfterTwoMinutes(t *testing.T) // Visible bleibt false: Fehler enthält "nicht sichtbar", Summe der wait-Dauern == 2*time.Minute, CleanUp gerufen
func TestIssuePassesRateLimit(t *testing.T)   // NewOrder liefert RateLimited{Until: t0}: errors.As findet es, kein Present
func TestRenewAt(t *testing.T) {
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 90 Tage Laufzeit → 30 Tage vor Ablauf
	if got := RenewAt(nb, nb.AddDate(0, 0, 90)); !got.Equal(nb.AddDate(0, 0, 60)) { t.Errorf("90 Tage: %v", got) }
	// 6 Tage Laufzeit → ein Drittel vor Ablauf (2 Tage)
	if got := RenewAt(nb, nb.AddDate(0, 0, 6)); !got.Equal(nb.AddDate(0, 0, 4)) { t.Errorf("6 Tage: %v", got) }
}
func TestNormalizeName(t *testing.T) // "NC.Firma.de." → "nc.firma.de"; "*.firma.de", "", "a b.de", "firma", "-x.firma.de" → Fehler
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./internal/integrations/acme/` → FAIL (Paket fehlt).
- [ ] **Step 3: `acme.go` schreiben.** `Issue` erzeugt den Schlüssel (`ecdsa.GenerateKey(elliptic.P256(), rand.Reader)`), die CSR mit genau `name` als DNS-Namen, fragt `Visible` alle 5 Sekunden über `wait` und löscht den Eintrag per `defer`, sobald `Present` eine Kennung geliefert hat. `RenewAt` = `notAfter − min(30 Tage, (notAfter−notBefore)/3)`. `Fingerprint` = SHA-256 des Blatt-Zertifikats, hex.
- [ ] **Step 4: `le.go` schreiben.** Dünner Adapter über `golang.org/x/crypto/acme`: Verzeichnis `https://acme-v02.api.letsencrypt.org/directory` bzw. bei `staging` `https://acme-staging-v02.api.letsencrypt.org/directory`; Konto beim ersten Auftrag anlegen (`Register` mit `AcceptTOS`, vorhandenes Konto ist kein Fehler); `Challenge()` liefert `"_acme-challenge."+name` und `DNS01ChallengeRecord`; HTTP 429 und `urn:ietf:params:acme:error:rateLimited` werden zu `RateLimited` mit `Until` aus `Retry-After` (fehlt er: 1 Stunde). Kein eigener Test – der Adapter läuft in der Abnahme gegen die Testumgebung.
- [ ] **Step 5: Tests grün** – `go test ./internal/integrations/acme/` → `ok`.
- [ ] **Step 6: Commit** – `git commit -m "Zertifikate: Ablauf für Let's Encrypt per DNS-01 (Paket acme)"`

---

### Task 2: DNS über Cloudflare

**Files:**
- Create: `internal/web/certs_dns.go`
- Test: `internal/web/certs_dns_test.go`

**Interfaces:**
- Consumes: `acme.DNS` (Task 1); `cloudflare.Client.CreateRecord/DeleteRecord/Records`, `cloudflare.Record` (vorhanden).
- Produces:
  - `type cfRecordAPI interface { CreateRecord(ctx context.Context, zoneID string, r cloudflare.Record) (*cloudflare.Record, error); DeleteRecord(ctx context.Context, zoneID, id string) error; Records(ctx context.Context, zoneID string) ([]cloudflare.Record, error) }`
  - `type certDNS struct { api cfRecordAPI; zoneID string; lookupTXT func(ctx context.Context, fqdn string) ([]string, error) }` – erfüllt `acme.DNS`
  - `func certLeftovers(records []cloudflare.Record, remembered map[string]string) []string` – Kennungen der Einträge, die gelöscht werden dürfen (`remembered`: Eintrags-Kennung → Name)
  - `func authoritativeTXT(zone string) func(ctx context.Context, fqdn string) ([]string, error)` – fragt die Nameserver der Zone direkt

- [ ] **Step 1: Tests schreiben**

```go
func TestCertDNSPresentCleanUp(t *testing.T) // Present legt TXT "_acme-challenge.nc.firma.de" mit TTL 60, nicht proxied, an und gibt die Kennung zurück; CleanUp löscht genau diese Kennung
func TestCertDNSVisible(t *testing.T)        // lookupTXT liefert ["alt","wert"] → true; ["alt"] → false; Fehler "no such host" → false ohne Fehler
func TestCertLeftovers(t *testing.T)         // zwei _acme-challenge-Einträge unter nc.firma.de, nur einer steht in remembered → nur dessen Kennung; fremder Eintrag bleibt
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./internal/web/ -run TestCert` → FAIL.
- [ ] **Step 3: `certs_dns.go` schreiben.** `authoritativeTXT` holt die NS der Zone mit `net.DefaultResolver.LookupNS` und fragt jeden über einen `net.Resolver` mit eigenem `Dial` auf `<ns>:53`; sichtbar heißt: bei allen Nameservern vorhanden.
- [ ] **Step 4: Tests grün**, **Step 5: Commit** – `git commit -m "Zertifikate: Prüf-Einträge über Cloudflare anlegen, prüfen und aufräumen"`

---

### Task 3: Migration 157 und Speicher

**Files:**
- Create: `migrations/157_le_certs.up.sql`, `internal/web/certs_store.go`
- Test: `internal/web/certs_store_test.go`

**Interfaces:**
- Consumes: `encryptSecret(key []byte, plain string) (string, error)`, `decryptSecret` (`partner_links.go`), `h.pkiKey()`, `h.getUpdateSetting/setUpdateSetting`.
- Produces:
  - Tabelle `le_certs(name text PRIMARY KEY, staging boolean NOT NULL, chain_pem text, key_enc text, not_before timestamptz, not_after timestamptz, fingerprint text, prev_chain_pem text, prev_key_enc text, state text NOT NULL DEFAULT 'neu', step text NOT NULL DEFAULT '', last_error text NOT NULL DEFAULT '', retry_after timestamptz, challenge_id text NOT NULL DEFAULT '', installed_fp text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now())`
  - `state`-Werte: `"neu"`, `"laeuft"`, `"gueltig"`, `"fehler"`.
  - Recht `system.certs` „Zertifikate verwalten (Let's Encrypt)“, Kategorie „System & Sicherheit“, an alle Rollen mit `system.cloudflare` ohne Abteilung (Muster: `156_nextcloud_sync.up.sql`).
  - `const permCerts = "system.certs"`; Einstellungen `keyCertEmail = "certs.email"`, `keyCertStaging = "certs.staging"`, `keyCertAccount = "certs.account"` (verschlüsselt, je Umgebung: Anhang `.staging`), `keyCertStrictBefore = "certs.strict_before"`.
  - Recht `system.unifi` „Namen im Haus eintragen (UniFi)“, gleiche Kategorie und Vergabe; `const permUnifi = "system.unifi"`. Einstellungen `keyUnifiURL = "unifi.url"`, `keyUnifiKey = "unifi.key"` (verschlüsselt), `keyUnifiSite = "unifi.site"`, `keyUnifiPin = "unifi.pin"`.
  - `type certRow struct { Name string; Staging bool; ChainPEM string; NotBefore, NotAfter time.Time; Fingerprint, State, Step, LastError, ChallengeID, InstalledFP, LANRecordID string; RetryAfter time.Time }`
  - `certSetLANRecord(ctx context.Context, name, id string) error` (Teil von `certStore`)
  - `type certStore interface` mit den folgenden Methoden (Datenbank-Variante am `Handler`, Speicher-Variante in den Tests von Task 6):
  - `certList(ctx context.Context) ([]certRow, error)`
  - `certAdd(ctx context.Context, name string, staging bool) error`
  - `certSetStep(ctx context.Context, name, state, step, lastErr string) error`
  - `certSetChallenge(ctx context.Context, name, id string) error`
  - `certSetInstalled(ctx context.Context, name, fp string) error`
  - `certSetRetry(ctx context.Context, name string, until time.Time) error`
  - `certSaveIssued(ctx context.Context, name string, is *acme.Issued) error` – schiebt den bisherigen Stand nach `prev_*`
  - `certKey(ctx context.Context, name string) (chainPEM, keyPEM []byte, err error)`
  - `certDelete(ctx context.Context, name string) error`
  - `func (h *Handler) certAccountKey(ctx context.Context, staging bool) ([]byte, error)` – legt beim ersten Aufruf an

- [ ] **Step 1: Tests schreiben** (ohne Datenbank: nur die reinen Teile)

```go
func TestCertKeyRoundTrip(t *testing.T)   // certSealKey(key, pem) → certOpenKey ergibt dasselbe PEM; mit anderem Schlüssel Fehler
func TestMigration157Grants(t *testing.T) // Datei enthält 'system.certs', 'system.unifi', 'System & Sicherheit' und die Vergabe über o.key = 'system.cloudflare' mit rp.department_id IS NULL
```

- [ ] **Step 2: Fehlschlag prüfen**, **Step 3: Migration und `certs_store.go` schreiben** (`certSealKey/certOpenKey` sind dünne Hüllen um `encryptSecret/decryptSecret` mit `h.pkiKey()`).
- [ ] **Step 4: Tests grün** – `go test ./internal/web/ -run 'TestMigration|TestCertKey'`.
- [ ] **Step 5: Commit** – `git commit -m "Zertifikate: Tabelle, Recht system.certs und verschlüsselter Speicher (Migration 157)"`

---

### Task 4: Namen prüfen und Stand im Haus

**Files:**
- Create: `internal/web/certs_names.go`
- Test: `internal/web/certs_names_test.go`

**Interfaces:**
- Consumes: `acme.NormalizeName`, `cfZoneGuess`, `h.cfAccess` (vorhanden).
- Produces:
  - `func certNameInZone(name, zone string) (string, error)` – normalisiert; Fehlertexte: „Sammelnamen (*.…) werden nicht unterstützt“, „Der Name liegt nicht in der Domain %s“
  - `func certSuggested(pdhHost, nextcloudURL string) []string`
  - `func certLANState(addrs []string, own []string) string` – `"direkt"`, wenn eine Antwort eine eigene Adresse ist, sonst `"cloudflare"`; ohne Antwort `"unbekannt"`
  - `func (h *Handler) certOwnAddrs(ctx context.Context) []string` – Adressen des Servers und die mitwandernde Adresse aus dem Verbund

- [ ] **Step 1: Tests schreiben**

```go
func TestCertNameInZone(t *testing.T) // "NC.Strobl-Home.net." in "strobl-home.net" → "nc.strobl-home.net"; "strobl-home.net" selbst erlaubt; "*.strobl-home.net", "nc.andere.de", "evilstrobl-home.net" → Fehler
func TestCertSuggested(t *testing.T)  // ("pdh.firma.de","https://nc.firma.de/") → [pdh.firma.de nc.firma.de]; leere und doppelte entfallen; Adresse als IP entfällt
func TestCertLANState(t *testing.T)   // ["10.10.0.15"] bei own ["10.10.0.17","10.10.0.15"] → "direkt"; ["188.114.96.3"] → "cloudflare"; nil → "unbekannt"
```

- [ ] **Step 2–4: Fehlschlag, Umsetzung, grün.**
- [ ] **Step 5: Commit** – `git commit -m "Zertifikate: Namen prüfen und Stand im Haus ermitteln"`

---

### Task 5: Update-Agent – Dateien, nginx, Rückweg

**Files:**
- Create: `cmd/update-agent/certs.go`
- Modify: `cmd/update-agent/main.go` (Routen neben `/v1/nextcloud/apps`)
- Test: `cmd/update-agent/certs_test.go`

**Interfaces:**
- Produces (HTTP, mit Token wie die übrigen Routen):
  - `POST /v1/certs/install` Rumpf `{"name":"nc.firma.de","chain_pem":"…","key_pem":"…","role":"pdh"|"nextcloud"|"","upstream":"http://127.0.0.1:8090"}` → `{"ok":true,"fingerprint":"…"}` oder `{"ok":false,"error":"…"}`
  - `POST /v1/certs/remove` Rumpf `{"name":"…","role":"…"}`
  - `GET /v1/certs/status` → `{"ok":true,"nginx":true,"certs":[{"name":"…","not_after":"RFC3339","fingerprint":"…"}]}`
  - `func certSiteConf(name, upstream string) string` – nginx-Seite für den PDH-Namen
  - `func certSwapLines(conf, certPath, keyPath string) (string, error)` – ersetzt die Zertifikatszeilen einer Nextcloud-Seite
  - `func certValidName(s string) bool`
  - `func nginxLayout(readFile func(string) ([]byte, error), exists func(string) bool) (sitesDir, group string)`

- [ ] **Step 1: Tests schreiben**

```go
func TestCertSiteConf(t *testing.T) // enthält: "listen 443 ssl;", "server_name pdh.firma.de;", ssl_certificate /etc/ssl/pdh/pdh.firma.de/fullchain.pem, proxy_pass http://127.0.0.1:8090;, "proxy_buffering off;", Upgrade/Connection-Kopfzeilen, X-Forwarded-Proto https, und einen Port-80-Block mit "return 301 https://$host$request_uri;"
func TestCertSwapLines(t *testing.T) {
	// genau ein Paar → beide Zeilen ersetzt, Rest byte-gleich, Einrückung bleibt
	// kein Paar, zwei Paare oder zwei server-Blöcke → Fehler "Nextcloud-Seite hat nicht genau ein Zertifikat – nicht geändert", Rückgabe unverändert
}
func TestCertValidName(t *testing.T)  // "nc.firma.de" ja; "../x", "a/b", "", "nc..de", Name länger als 253 Zeichen nein
func TestNginxLayout(t *testing.T)    // nginx.conf mit "user nginx;" und nur conf.d → ("/etc/nginx/conf.d","nginx"); Ubuntu-Stand → ("/etc/nginx/sites-enabled","www-data")
func TestCertInstallRestoresOnFailedCheck(t *testing.T) // mit temp. Wurzel und eingeschobenem "nginx -t", das fehlschlägt: alte fullchain.pem, privkey.pem und Seiten sind danach byte-gleich, Antwort ok=false mit der Ausgabe von nginx
func TestCertInstallRefusedInDocker(t *testing.T)       // mode docker → ok=false, "nur im systemd-Betrieb"
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/update-agent/ -run TestCert` → FAIL.
- [ ] **Step 3: `certs.go` schreiben.** Dateiwurzel und die Befehle `nginx -t` / `systemctl reload nginx` als Felder am Agenten, damit der Test sie ersetzt. Ablauf: Namen prüfen → Sicherungskopien im Speicher → Dateien erst als `*.neu`, dann umbenennen (Ordner 0750, Schlüssel 0640, Gruppe aus `nginxLayout`) → Seite schreiben bzw. Zeilen tauschen → `nginx -t` → bei Fehler alles zurück, sonst neu laden. Die Nextcloud-Seite ist die Datei im Seiten-Ordner, deren `server_name` den Namen enthält. Fehlt nginx: `ok=false`, „nginx ist nicht eingerichtet“. `remove` merkt nichts selbst: Für die Nextcloud-Seite schickt das PDH die vorigen Zertifikatspfade mit (`"restore_cert"`, `"restore_key"`), die der Agent beim ersten `install` in der Antwort als `"previous_cert"`, `"previous_key"` gemeldet hat.
- [ ] **Step 4: Routen in `main.go`**, **Step 5: Tests grün** – `go test ./cmd/update-agent/`.
- [ ] **Step 6: Commit** – `git commit -m "Update-Agent: Zertifikate ablegen, nginx-Seiten schreiben und bei Fehlern zurücknehmen"`

---

### Task 6: Auftrag, Erneuerung und Alarme im PDH

**Files:**
- Create: `internal/web/certs.go`
- Modify: `cmd/server/main.go` (Start des Hintergrundlaufs neben `StartUpdateChecker`)
- Test: `internal/web/certs_test.go`

**Interfaces:**
- Consumes: Task 1–5; `h.agentJSON(ctx, method, path, body, out)`; `h.notifyAdmins`; `h.Mirror().On`; `h.MaintLocked()`; `componentLog("zertifikate")`.
- Produces:
  - `type certDeps struct { store certStore; ca func(staging bool) (acme.CA, error); dns func() (acme.DNS, error); install func(ctx context.Context, name string, chain, key []byte) (fp string, err error); status func(ctx context.Context) (map[string]string, error); blocked func() bool; alarm func(text string); now func() time.Time }`
  - `var errCertBusy = errors.New("Für diesen Namen läuft schon ein Auftrag")`
  - `func certIssue(ctx context.Context, d certDeps, name string) error` – ein Auftrag
  - `func certDue(rows []certRow, now time.Time) []string` – Namen, die jetzt erneuert werden
  - `func certAlarm(row certRow, now time.Time) (text string, ok bool)`
  - `func certDaily(ctx context.Context, d certDeps)` – Aufräumen, fällige Namen, Abgleich Datei/Datenbank, Alarme
  - `func (h *Handler) certDepsLive() certDeps`
  - `func (h *Handler) StartCertRenewal(ctx context.Context)` – erster Lauf 2 Minuten nach dem Start, dann alle 24 Stunden; zusätzlich 1 Minute nach einem Rollenwechsel zu „aktiv“
  - `func certRole(name, pdhHost, nextcloudURL string) string` – `"pdh"`, `"nextcloud"` oder `""`

- [ ] **Step 1: Tests schreiben** (mit `certStore` im Speicher)

```go
func TestCertIssueStoresAndInstalls(t *testing.T) // Erfolg: state "gueltig", installed_fp == Fingerprint, step leer
func TestCertIssueBusy(t *testing.T)              // zweiter Aufruf während der erste in Finalize wartet → errCertBusy, CA nur einmal gefragt
func TestCertIssueKeepsOldOnFailure(t *testing.T) // Fehler in Accept: state "fehler", last_error gesetzt, bisheriges Zertifikat unverändert
func TestCertIssueRateLimit(t *testing.T)         // RateLimited{Until}: retry_after == Until; certDue liefert den Namen vor Until nicht
func TestCertIssueAgentDown(t *testing.T)         // install schlägt fehl: Zertifikat gespeichert, installed_fp leer, last_error beginnt mit "noch nicht eingerichtet"
func TestCertDue(t *testing.T)                    // gültig bis in 29 Tagen → fällig; 31 Tage → nicht; state "laeuft" → nicht; nie geholt → fällig
func TestCertAlarm(t *testing.T)                  // 15 Tage → kein Alarm; 14 Tage → "läuft in 14 Tagen ab"; abgelaufen → "ist abgelaufen"
func TestCertDailySkipsWhenBlocked(t *testing.T)  // blocked()==true (Spiegel oder Wartung): keine CA-Anfrage, kein Agent-Aufruf
func TestCertDailyReinstallsOnMismatch(t *testing.T) // status meldet anderen Fingerabdruck → install mit dem Stand aus dem Speicher
func TestCertRole(t *testing.T)
```

- [ ] **Step 2: Fehlschlag prüfen**, **Step 3: `certs.go` schreiben.** Sperre je Name über eine `map[string]struct{}` hinter einem Mutex. Alarme: einmal bei 14 Tagen, ab 7 Tagen täglich, bei Ablauf sofort; der zuletzt gemeldete Tag steht in der Einstellung `certs.alarm.<name>`, damit ein Neustart nicht doppelt meldet. Protokoll: Name, Schritt, Fingerabdruck – nie PEM.
- [ ] **Step 4: Start in `cmd/server/main.go`**, **Step 5: Tests grün** – `go test ./internal/web/ -run TestCert`.
- [ ] **Step 6: Commit** – `git commit -m "Zertifikate: holen, täglich prüfen, erneuern und rechtzeitig warnen"`

---

### Task 7: Karte, Routen, Rechte

**Files:**
- Create: `internal/web/certs_web.go`
- Modify: `web/templates/widgets/cloudflare.gohtml` (neue `{{define "cert-panel"}}`, eingebunden unter `cf-panel`), `internal/web/handler.go:606` (Routen), `internal/web/perm_gate.go` (`gateRules`), `internal/web/cloudflare_api.go` (`cfPanel` bekommt `Certs certPanel`)
- Test: `internal/web/certs_web_test.go`

**Interfaces:**
- Consumes: Task 3, 4, 6; `h.hasPerm`, `h.isAreaAdmin`, `loadTestTemplates`, `renderPage` (Testhelfer).
- Produces:
  - `type certLine struct { Name, State, Step, Error, ValidUntil, LAN string; Staging, CanRemove bool }`
  - `type certPanel struct { CanManage, AgentOK, NginxOK, Staging, StrictReady, StrictOn bool; Email, Msg, Err string; Lines []certLine; Suggested []string }`
  - Routen: `GET /admin/server-config/certs/panel`, `POST /admin/server-config/certs/settings` (E-Mail, Testumgebung), `POST /admin/server-config/certs/add`, `POST /admin/server-config/certs/issue`, `POST /admin/server-config/certs/remove`, `POST /admin/server-config/certs/strict`
  - `gateRules`: `certs/remove` braucht den Bereichsadministrator, `certs/strict` zusätzlich `system.cloudflare`, alle anderen `system.certs`.

- [ ] **Step 1: Tests schreiben**

```go
func TestCertPanelRender(t *testing.T) // mit CanManage: Überschrift "Zertifikate (Let's Encrypt)", je Zeile Name, "gültig bis 09.02.2027", "im Haus direkt" bzw. "über Cloudflare", Knöpfe value="issue" und (nur CanRemove) value="remove"; Fehlerzeile zeigt den Grund
func TestCertPanelWithoutRight(t *testing.T) // ohne CanManage: keine Formulare, Satz mit dem Namen des Rechts
func TestCertPanelStrictButton(t *testing.T) // StrictReady=false → Knopf disabled; Staging=true → Hinweis "Testumgebung: Zertifikate sind nicht vertrauenswürdig"
func TestCertGateRules(t *testing.T)         // remove ohne Bereichsadministrator → 403; strict ohne system.cloudflare → 403; issue ohne system.certs → 403
func TestCertAddRejectsForeignName(t *testing.T) // "nc.andere.de" → Meldung "liegt nicht in der Domain", nichts gespeichert
```

- [ ] **Step 2: Fehlschlag prüfen**, **Step 3: Handler und Vorlage schreiben.** Die Karte lädt sich wie `cf-panel` per HTMX nach und fragt alle 5 Sekunden neu, solange eine Zeile „läuft“. `issue` startet `certIssue` in einer Goroutine mit eigenem Kontext (nicht dem der Anfrage). `remove` ruft erst den Agenten, dann `CA.Revoke`, dann `certDelete`. Nur Symbol-Klassen und CSS-Variablen, keine festen Farben.
- [ ] **Step 4: Tests grün** – `go test ./internal/web/ -run 'TestCert|TestServerConfig|TestPermGate'`.
- [ ] **Step 5: Im Browser prüfen** (lokaler Server): Karte in Fiori und Fluent, hell und dunkel, bei 390 und 1366 px Breite.
- [ ] **Step 6: Commit** – `git commit -m "Zertifikate: Karte unter Cloudflare-Tunnel mit Recht system.certs"`

---

### Task 8: „Full (strict)“ umstellen und zurückstellen

**Files:**
- Create: `internal/web/certs_strict.go`
- Modify: `internal/integrations/cloudflare/cloudflare.go` (Angaben `originServerName` und `httpHostHeader` am Ziel, falls sie fehlen)
- Test: `internal/web/certs_strict_test.go`

**Interfaces:**
- Consumes: `cloudflare.Client.TunnelConfig/PutTunnelConfig/Setting/SetSetting`, `cfSetIngress`, `cloudflare.TunnelIDs` (vorhanden); `certList`.
- Produces:
  - `type strictBefore struct { SSL string; Ingress []cloudflare.Ingress }` (als JSON in `keyCertStrictBefore`)
  - `func certStrictIngress(list []cloudflare.Ingress, names []string) []cloudflare.Ingress` – je Name Ziel `https://localhost:443`, „Origin Server Name“ = Name, Host-Kopfzeile = Name, Prüfung an; andere Ziele unverändert
  - `func certStrictReady(rows []certRow, now time.Time) bool` – mindestens ein Name, alle `gueltig`, keiner aus der Testumgebung, keiner abgelaufen, alle eingerichtet
  - `func certLocalCheck(ctx context.Context, name string) error` – TLS-Verbindung zu `127.0.0.1:443` mit Servername `name` und Prüfung gegen die Wurzelzertifikate des Systems
  - `func (h *Handler) certStrictOn(ctx context.Context) error`, `func (h *Handler) certStrictOff(ctx context.Context) error`

- [ ] **Step 1: Tests schreiben**

```go
func TestCertStrictIngress(t *testing.T) // zwei geführte Namen und ein fremdes Ziel: nur die zwei geändert, Reihenfolge bleibt, Auffangregel am Ende bleibt
func TestCertStrictReady(t *testing.T)   // Testumgebung, Zustand fehler, installed_fp leer, leere Liste → false
func TestCertStrictOnRemembersBefore(t *testing.T) // mit Cloudflare im Speicher: vorheriger Stand gespeichert, SSL "strict"; Off stellt Ziel und SSL genau wieder her und löscht die Einstellung
func TestCertStrictOnStopsWhenLocalCheckFails(t *testing.T) // nichts bei Cloudflare geändert
```

- [ ] **Step 2–4: Fehlschlag, Umsetzung, grün.** Jede Änderung geht über `h.notifyAdmins` und ins Protokoll (Muster aus `cfDone`).
- [ ] **Step 5: Commit** – `git commit -m "Zertifikate: Tunnel und Domain auf Full (strict) umstellen und zurückstellen"`

---

### Task 8a: Namen im Haus über die UniFi UDM Pro

**Files:**
- Create: `internal/integrations/unifi/unifi.go`, `internal/web/certs_lan.go`
- Modify: `web/templates/widgets/cloudflare.gohtml` (`cert-panel`: Abschnitt „Namen im Haus (UniFi)“, Knopf je Zeile), `internal/web/certs_web.go` (`certPanel` bekommt `Unifi unifiPanel`, `certLine` bekommt `LANRecord, LANOther string`), `internal/web/handler.go`, `internal/web/perm_gate.go`
- Test: `internal/integrations/unifi/unifi_test.go`, `internal/web/certs_lan_test.go`

**Interfaces:**
- Consumes: Task 3 (`permUnifi`, `keyUnifi*`, `certSetLANRecord`), Task 4 (`certOwnAddrs`), Task 7 (Karte), `encryptSecret/decryptSecret`, `h.pkiKey()`.
- Produces:
  - `type Client struct { Base, Key, Site, Pin string; HTTP *http.Client }`; `func New(base, key, site, pin string) *Client` – TLS prüft allein den SHA-256-Fingerabdruck des Gerätezertifikats gegen `Pin`; leerer `Pin` → jede Anfrage scheitert mit `ErrNoPin`
  - `func Fingerprint(ctx context.Context, base string) (string, error)` – liest nur das Zertifikat, sendet keinen Schlüssel
  - `type Record struct { ID, Name, Value, Type string }`
  - `func (c *Client) Records(ctx context.Context) ([]Record, error)`, `func (c *Client) Create(ctx context.Context, name, ip string) (id string, err error)`, `func (c *Client) Update(ctx context.Context, id, name, ip string) error`, `func (c *Client) Delete(ctx context.Context, id string) error`
  - `var ErrNoPin`, `var ErrPinChanged`, `var ErrForbidden` (401/403)
  - `type unifiPanel struct { URL, Site, Pin, SeenPin, Err string; HasKey, Confirmed bool }`
  - `func certLANPlan(recs []unifi.Record, name, ip, ownID string) (action string, other string)` – `"anlegen"`, `"stimmt"`, `"eigenen-aendern"` (Kennung == ownID), `"fremd"` (anderer Eintrag gleichen Namens mit anderer Adresse; `other` = dessen Adresse)
  - Routen: `POST /admin/server-config/certs/unifi` (Einstellungen; antwortet mit dem gesehenen Fingerabdruck), `POST /admin/server-config/certs/unifi/confirm`, `POST /admin/server-config/certs/lan` (Felder `name`, `overwrite`) – alle `system.unifi`; nur am aktiven Server, nicht in der Wartung.

- [ ] **Step 0: Am Gerät des Anwenders ablesen (mit dem Anwender, vor dem Code).** Version von UniFi Network; mit dem Schlüssel `GET /proxy/network/integration/v1/sites` und `GET …/sites/<id>/dns/policies`; antwortet das zweite mit 404, `GET /proxy/network/v2/api/site/<name>/static-dns`. Eine Antwort je Variante (ohne Schlüssel) als `testdata/*.json` ablegen und die Feldnamen für Anlegen/Ändern aus der Beschreibung der Schnittstelle am Gerät übernehmen. Die Pfade hier stammen aus Quellen Dritter – bei Abweichung gilt das Gerät; den Plan dann an dieser Stelle berichtigen.
- [ ] **Step 1: Tests schreiben**

```go
// unifi_test.go, mit httptest.NewTLSServer und den Antworten aus testdata
func TestPinAccepted(t *testing.T)        // Pin == Fingerabdruck des Testservers → Records liefert die Einträge; Kopfzeile X-API-KEY gesetzt
func TestPinChanged(t *testing.T)         // anderer Pin → ErrPinChanged, der Server sah keine Anfrage (Schlüssel nicht gesendet)
func TestNoPin(t *testing.T)              // leerer Pin → ErrNoPin
func TestFallbackToStaticDNS(t *testing.T) // dns/policies antwortet 404 → dieselben Aufrufe gehen an static-dns
func TestForbidden(t *testing.T)          // 403 → ErrForbidden
func TestCreateSendsARecord(t *testing.T) // Rumpf enthält Name, Adresse, Typ A, eingeschaltet
// certs_lan_test.go
func TestCertLANPlan(t *testing.T) // kein Eintrag → "anlegen"; gleicher Name+Adresse → "stimmt"; eigener mit alter Adresse → "eigenen-aendern"; fremder mit anderer Adresse → "fremd", other gesetzt; Vergleich ohne Groß/Klein und ohne Punkt am Ende
func TestCertLANNeedsOverwrite(t *testing.T) // "fremd" ohne overwrite=1 → nichts geändert, Meldung nennt die vorhandene Adresse
func TestCertLANRemoveOnlyOwn(t *testing.T)  // Entfernen eines Namens: Delete nur mit gemerkter LANRecordID; ohne Kennung kein Aufruf
func TestUnifiPanelHidesKey(t *testing.T)    // gerenderte Karte enthält den Schlüssel nicht, nur "hinterlegt"
```

- [ ] **Step 2: Fehlschlag prüfen**, **Step 3: Umsetzen.** Zieladresse ist die mitwandernde Adresse, ohne Verbund die erste eigene Adresse. Die tägliche Prüfung (`certDaily`) ruft `Records` nur lesend und schreibt Abweichungen als Hinweis in die Zeile. Protokoll: Name, Adresse, Kennung – nie der Schlüssel.
- [ ] **Step 4: Tests grün** – `go test ./internal/integrations/unifi/ ./internal/web/ -run 'TestPin|TestNoPin|TestFallback|TestForbidden|TestCreate|TestCertLAN|TestUnifi'`.
- [ ] **Step 5: Commit** – `git commit -m "Zertifikate: Namen im Haus über die UniFi UDM Pro eintragen (Recht system.unifi)"`

---

### Task 9: Verbund, Sicherung, nginx-Einrichtung, Labor

**Files:**
- Modify: `internal/ha/mirror.go:133` (Quelle `ssl-pdh`), `cmd/pdh-ha/mirror_recv.go:693` und `cmd/pdh-ha/mirror_worker.go:1225` (Rang und Sonderfall wie `ssl-pdh-nextcloud`), `cmd/update-agent/dr_apps.go:62` (Sicherungsquelle), `install_update.sh` (nginx einrichten, wenn `/etc/ssl/pdh` Namen enthält und nginx fehlt), `deploy/ha-lab/ha-lab.sh` (Szenario)
- Test: `internal/ha/mirror_test.go`, `cmd/update-agent/dr_apps_test.go` (vorhandene Tabellen-Tests erweitern)

**Interfaces:**
- Produces: `MirrorSource{Label: "ssl-pdh", Path: "/etc/ssl/pdh"}`; `drSource{Label: "ssl-pdh", Path: "/etc/ssl/pdh"}` – beide unabhängig davon, ob Nextcloud eingerichtet ist; Labor-Szenario `sc_spiegel_zertifikat`.

- [ ] **Step 1: Tests erweitern**

```go
// mirror_test.go: MirrorSources enthält "ssl-pdh" mit und ohne „Nextcloud mitnehmen“; Rang hinter "ssl-pdh-nextcloud"
// dr_apps_test.go: Quelle "ssl-pdh" erscheint, sobald /etc/ssl/pdh existiert – auch ohne Nextcloud
```

- [ ] **Step 2: Fehlschlag prüfen**, **Step 3: Quellen ergänzen**, **Step 4: `install_update.sh`** – nur im systemd-Betrieb, `apt-get install -y nginx`, danach `systemctl enable --now nginx` nur am aktiven Server (im Verbund steuert `pdh-ha` den Dienst wie in Teil 6).
- [ ] **Step 5: Labor-Szenario** `sc_spiegel_zertifikat`: auf dem aktiven Knoten `/etc/ssl/pdh/lab.test/fullchain.pem` und `privkey.pem` anlegen, Abgleich abwarten, Tausch; danach sind beide Dateien auf dem neuen aktiven Knoten byte-gleich (Prüfsumme) und der Schlüssel hat Rechte 0640.
- [ ] **Step 6: Tests und Labor** – `go test ./internal/ha/ ./cmd/update-agent/ ./cmd/pdh-ha/` grün; im Labor `spiegel_zertifikat` und einmal `alle` (Windows-Docker, Skripte wie im Ledger der Spiegelung beschrieben).
- [ ] **Step 7: Commit** – `git commit -m "Verbund: Zertifikate unter /etc/ssl/pdh spiegeln und sichern, nginx bei Bedarf einrichten"`

---

### Task 10: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Abschnitt nach `id="hb-cloudflare-api"`, Kapitel „Administration“), `web/templates/widgets/help_figures.gohtml` (Skizze der Cloudflare-Seite um die Karte ergänzen), `CHANGELOG.md`, `VERSION`

- [ ] **Step 1: Handbuch.** Drei `hb-howto`-Blöcke: „So holst du ein Zertifikat“ (E-Mail, erst Testumgebung, Name, „Jetzt holen“), „So zeigen die Namen im Haus auf den Server“ (UniFi UDM Pro: API-Schlüssel anlegen – Menüpfad aus Task 8a Step 0, nicht raten –, Adresse und Schlüssel im PDH eintragen, Fingerabdruck bestätigen, je Name „Im Haus eintragen“; dazu ein allgemeiner Satz für andere DNS-Server: Eintrag Typ A von Hand auf die mitwandernde Adresse; Kontrolle über die Spalte „im Haus direkt“), „Full (strict) einschalten und zurückstellen“. Fehlerbilder aus Spec Abschnitt 8 als Liste. Den Satz „Zertifikate über Cloudflare stellt das PDH noch nicht aus.“ streichen. Im Kapitel „Administration“ das Recht nennen.
- [ ] **Step 2: `VERSION` = `0.77.10`, `CHANGELOG.md`** – neuer Eintrag `## [0.77.10] – <Datum>` mit `### Neu` (Zertifikate, Namen im Haus, Full (strict)) und `### Sicherheit` (Rechte `system.certs` und `system.unifi`, Schlüssel verschlüsselt, UDM Pro nur mit bestätigtem Fingerabdruck, Entfernen nur Bereichsadministrator).
- [ ] **Step 3: Prüfen** – `go test ./internal/web/ -run 'TestHelp|TestChangelog'` → `ok`; danach `go vet ./...` und `go test ./...` vollständig.
- [ ] **Step 4: Commit** – `git commit -m "Handbuch und Änderungsprotokoll 0.77.10: Zertifikate von Let's Encrypt"`

---

### Task 11: Abnahme im Testverbund (mit dem Anwender)

Voraussetzungen: Cloudflare-API-Token mit „DNS bearbeiten“ im PDH der Testserver; Zweig gepusht und per Verbund-Update eingespielt (`sudo pdh-ha update start <Commit> --version 0.77.10` am aktiven Server) – beides nur mit Freigabe des Anwenders.

- [ ] **Fall 1:** Schalter „Testumgebung“ an, Zertifikate für den Test-PDH-Namen und `nc.strobl-home.net` holen. Erwartet: beide „gültig bis …“, `sudo nginx -t` ohne Fehler, `openssl s_client -connect 127.0.0.1:443 -servername nc.strobl-home.net` zeigt einen Aussteller mit „STAGING“.
- [ ] **Fall 2:** UniFi-Adresse und API-Schlüssel eintragen, Fingerabdruck bestätigen, bei beiden Namen „Im Haus eintragen“. Erwartet: in UniFi Network stehen zwei Einträge Typ A auf 10.10.0.15, andere Einträge unverändert; Karte zeigt „im Haus direkt“; Browser im Haus erreicht beide Namen (Warnung wegen Testumgebung erwartet).
- [ ] **Fall 3:** Rollentausch. Erwartet: Dateien unter `/etc/ssl/pdh` auf beiden Servern gleich, beide Namen im Haus und von außen erreichbar.
- [ ] **Fall 4:** „Jetzt holen/erneuern“ bei gültigem Zertifikat. Erwartet: neuer Fingerabdruck, nginx neu geladen, voriges Zertifikat in der Datenbank als Vorgänger.
- [ ] **Fall 5:** Token ohne „DNS bearbeiten“. Erwartet: Karte nennt das fehlende Recht, kein Auftrag beginnt.
- [ ] **Fall 6:** Testumgebung aus, echte Zertifikate holen, „Full (strict)“ einschalten. Erwartet: von außen und im Haus ohne Warnung in Browser, Desktop-Client und Handy-App; „Zurückstellen“ stellt den vorigen Stand her.

Ergebnis je Fall im Ledger `.superpowers/sdd/2026-11-11-cloudflare-zertifikate/progress.md` festhalten. Danach Gesamtprüfung, Prüfung vor dem Merge, PR; Merge nur mit Freigabe.
