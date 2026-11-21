# Firmenstammdaten (0.82.18) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Die eigene Firma einmal pflegen (Reiter „Firma“ an jedem obersten Infrastruktur-Knoten) und in Mails, Druckkopf, Wartungsprotokoll und M365-Signaturen verwenden; Ansprechpartner ist die auslösende Person.

**Architecture:** Eine Tabelle `company_profile` mit genau einer Zeile, gelesen über einen kleinen Zwischenspeicher im Handler. Reine Formatierfunktionen (Fußzeile, Anschrift, Pflichtangaben, Ansprechpartner) werden von Mailversand, Druckkopf, PDF und Signaturen gemeinsam genutzt. Bearbeiten nur mit `company.edit`, das überall gilt.

**Tech Stack:** Go 1.27, chi, html/template + htmx, pgx, fpdf.

**Spec:** `docs/superpowers/specs/2026-11-21-firmenstammdaten-design.md`

## Global Constraints

- Version 0.82.18; CHANGELOG `### Neu` und `### Sicherheit` (neues Recht).
- Migration 189 (`migrations/189_company_profile.up.sql`), danach `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- Recht `company.edit` „Firmenstammdaten bearbeiten“, Kategorie „Stammdaten“, an Rollen/Benutzer mit `system.server_config`, `m365.templates.manage` oder `system.manage_roles` (mit deren `department_id`, Muster `migrations/179_esppdh.up.sql:71-98`).
- Bearbeiten nur, wenn `h.rbac.Scope(u.ID, role, "company.edit")` `all == true` liefert.
- Leere Stammdaten (kein `name`): Mails, Druckkopf, PDF und Signaturen wie heute.
- Alle Werte in HTML escapen (html/template bzw. `template.HTMLEscapeString`).
- Texte deutsch; Fiori/Fluent, hell/dunkel, 320–1366 px.
- Tests in Git Bash mit Labor-Datenbank (`deploy/lab/mssql/LIESMICH.md`).

## Review Focus

1. Nur ein Teil der Felder gefüllt (z. B. nur Name und Ort): Fußzeile und Pflichtangaben-Zeile ohne leere Trenner („ ·  · “). → Tests in Task 2.
2. Sonderzeichen/HTML in Firmenfeldern (`<b>`, `&`, Anführungszeichen) erscheinen als Text in Mail, Signatur-Regel und Druckkopf. → Tests in Task 2 und 5.
3. Recht `company.edit` nur in einer Abteilung: Ansicht ja, Speichern 403. → Test in Task 6.
4. `PDH_SMTP_FROM` mit eigenem Namen („PDH <pdh@firma.de>“) behält diesen Namen; ohne Namen kommt der Firmenname, Umlaute korrekt kodiert. → Test in Task 3.
5. Am Spiegel (Lesebetrieb) wird nicht gespeichert (bestehende Schreibsperre greift; Formular zeigt die Meldung). → Prüfung im Browser, Hinweis in Task 6.

---

### Task 1: Tabelle, Recht, Speicher und Prüfung

**Files:**
- Create: `migrations/189_company_profile.up.sql`, `internal/web/company.go`, `internal/web/company_test.go`
- Modify: `migrations/checksums.txt`

**Interfaces:**
- Produces:
  - `type companyProfile struct { Name, NameAddition, Street, PostalCode, City, Country, Phone, Fax, Email, Website, RegisteredOffice, RegisterCourt, RegisterNo, Management, SupervisoryChair, VatID, TaxNo, BankName, IBAN, BIC string; UpdatedAt time.Time; UpdatedBy string }`
  - `func (h *Handler) company(ctx context.Context) companyProfile` – aus dem Zwischenspeicher (`h.companyCache`, wie `brandCache`), Lesefehler → leeres Profil.
  - `func (h *Handler) saveCompany(ctx context.Context, p companyProfile, userID string) error` – UPDATE der einen Zeile, leert den Zwischenspeicher, Protokoll `componentLog("stammdaten")` mit Benutzer und geänderten Feldnamen.
  - `func validateCompany(p *companyProfile) error` – trimmt alle Felder; prüft Längen laut Entwurf, E-Mail (`net/mail.ParseAddress`, nur Adresse), Website `http(s)://` mit Rechner, IBAN (Leerzeichen raus, groß, mod-97 = 1), BIC 8 oder 11 Zeichen, USt-IdNr. `^[A-Z]{2}[A-Z0-9]{2,12}$` (Leerzeichen raus); Fehlertext nennt das Feld deutsch.

- [ ] **Step 1: Tests** `TestValidateCompany` (Tabelle: alles leer → ok; `email: "kein@"` → Fehler mit „E-Mail“; `website: "firma.de"` → Fehler „Website“; `iban: "DE89 3704 0044 0532 0130 00"` → ok und gespeichert als `DE89370400440532013000`; `iban: "DE89370400440532013001"` → Fehler „IBAN“; `vat_id: "de 123456789"` → `DE123456789`; Name mit 201 Zeichen → Fehler „Firma“) und `TestCompanyProfileRoundTrip` (Build-Tag `integration`, Labor-DB wie in den übrigen Integrationstests von `internal/web`: speichern, lesen, `UpdatedBy` gesetzt, Zwischenspeicher nach Speichern neu).
- [ ] **Step 2:** `go test ./internal/web -run TestValidateCompany` → FAIL (nicht definiert).
- [ ] **Step 3:** Migration: Tabelle wie im Entwurf (`id boolean PRIMARY KEY DEFAULT true CHECK (id)`, alle Textfelder `text NOT NULL DEFAULT ''`, Land Standard `'Deutschland'`, `updated_at timestamptz`, `updated_by uuid NULL REFERENCES users(id) ON DELETE SET NULL`), `INSERT … VALUES (true)`, Name aus `app_settings` `m365_company_name` übernehmen, Schlüssel löschen, Recht anlegen und vergeben (siehe Global Constraints). Danach `company.go`.
- [ ] **Step 4:** `go test ./internal/web -run TestValidateCompany` und `go test -tags integration ./internal/web -run TestCompanyProfileRoundTrip` → PASS; `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen` → PASS.
- [ ] **Step 5:** Commit `Firmenstammdaten: Tabelle, Recht company.edit, Speicher und Prüfung (0.82.18)`.

### Task 2: Gemeinsame Bausteine – Anschrift, Pflichtangaben, Ansprechpartner, Fußzeile

**Files:**
- Create: `internal/web/company_format.go`, `internal/web/company_format_test.go`

**Interfaces:**
- Consumes: `companyProfile` (Task 1).
- Produces:
  - `func companyAddressLine(p companyProfile) string` – „Straße · PLZ Ort“ (Land nur, wenn nicht „Deutschland“), leere Teile entfallen.
  - `func companyLegalLine(p companyProfile) string` – „Sitz: … · Registergericht: … HRB … · Geschäftsführung: … · USt-IdNr.: …“ (Aufsichtsrat, falls gesetzt, nach Geschäftsführung), leere Teile entfallen.
  - `type contactPerson struct{ Name, Title, Phone, Mobile, Email string }`
  - `func contactPhone(userPhone, userInternal, companyPhone string) string` – `userPhone`, sonst `companyPhone + "-" + userInternal` (nur wenn beides gesetzt), sonst `userInternal`.
  - `func (h *Handler) contactPersonFor(ctx context.Context, userID string) *contactPerson` – nil bei leerer ID oder Fehler; liest `first_name, last_name, job_title, phone, phone_internal, phone_mobile, email`.
  - `func companyFooterHTML(p companyProfile, c *contactPerson) template.HTML` – leer, wenn `p.Name == ""`; sonst Block „Ansprechpartner“ (nur mit `c`; ohne `c` „Kontakt: Telefon · E-Mail der Firma“), Firma (+ Zusatz), Anschrift, „Telefon · E-Mail · Website“, Pflichtangaben-Zeile; Stil wie die bestehende graue Fußzeile (`color:#888;font-size:12px`).

- [ ] **Step 1: Tests:** `TestCompanyAddressLine` („Hauptstr. 1 · 86150 Augsburg“; mit Land „Österreich“ angehängt; nur Ort → „Augsburg“); `TestCompanyLegalLine` (alle Felder; nur Registergericht+Nr. → „Registergericht: Augsburg HRB 12345“ ohne „ · “ am Rand); `TestContactPhone` (`"+49 821 1", "", ""` → erste; `"", "123", "+49 821 99"` → `"+49 821 99-123"`; `"", "123", ""` → `"123"`); `TestCompanyFooterHTML` (leerer Name → `""`; Name `Muster & Söhne <GmbH>` → enthält `Muster &amp; Söhne &lt;GmbH&gt;`; mit Person → enthält „Ansprechpartner“ und Name; ohne Person → enthält „Kontakt“).
- [ ] **Step 2:** `go test ./internal/web -run 'TestCompany(Address|Legal|Footer)|TestContactPhone'` → FAIL.
- [ ] **Step 3:** Implementieren (`html/template` für die Fußzeile, kein Zusammenkleben roher Strings).
- [ ] **Step 4:** Dieselben Tests → PASS.
- [ ] **Step 5:** Commit `Firmenstammdaten: Anschrift, Pflichtangaben, Ansprechpartner und Mail-Fußzeile (0.82.18)`.

### Task 3: Mails – Absendername und Fußzeile

**Files:**
- Modify: `internal/web/purchase_report.go:462-481` (`sendMail`), `internal/web/user_mail.go:72,125`, `internal/web/server_config.go:675`, `internal/web/password.go:152`, `internal/web/purchase_report.go:512`
- Test: `internal/web/company_mail_test.go`

**Interfaces:**
- Consumes: `h.company`, `h.contactPersonFor`, `companyFooterHTML`.
- Produces:
  - `func (h *Handler) sendMailBy(to []string, subject, html, actorID string) error` – hängt `companyFooterHTML(h.company(ctx), h.contactPersonFor(ctx, actorID))` an (Hilfsfunktion `mailAppendFooter(html string, footer template.HTML) string`: vor dem letzten `</div>` bzw. `</body>`, sonst ans Ende) und versendet wie bisher; `sendMail(to, subject, html)` ruft `sendMailBy(…, "")`.
  - `func mailFromHeader(from, company string) string` – hat `from` einen Anzeigenamen (`mail.ParseAddress`), bleibt er; sonst `(&mail.Address{Name: company, Address: adresse}).String()`; leerer `company` → `from` unverändert. Nur im SMTP-Weg verwenden (Graph: Postfach bestimmt den Namen).
- [ ] **Step 1: Tests:** `TestMailFromHeader` (`"pdh@firma.de","Muster GmbH"` → `"\"Muster GmbH\" <pdh@firma.de>"`; `"PDH <pdh@firma.de>","Muster GmbH"` → unverändert; `"pdh@firma.de",""` → `"pdh@firma.de"`; Umlaut „Müller GmbH“ → enthält `=?utf-8?`); `TestMailAppendFooter` (`"<div>x</div>"` + Fußzeile → Fußzeile vor dem letzten `</div>`; leere Fußzeile → unverändert).
- [ ] **Step 2:** `go test ./internal/web -run 'TestMailFromHeader|TestMailAppendFooter'` → FAIL.
- [ ] **Step 3:** Implementieren; Aufrufer mit Person: `UserTestMailWeb` (`getUser(r).ID`), Diagnose-Testmail (`u.ID`); ohne Person: Statusmeldungen, Kennwort zurücksetzen, Bestellvorschlag.
- [ ] **Step 4:** `go test ./internal/web -run 'Mail'` → PASS (bestehende Mail-Tests bleiben grün).
- [ ] **Step 5:** Commit `Mails: Firmenname als Absender und Fußzeile mit Ansprechpartner (0.82.18)`.

### Task 4: Druckkopf und Wartungsprotokoll

**Files:**
- Modify: `internal/web/handler.go` (`BaseData`, `baseData` :1443), `web/templates/base.gohtml:1154`, `internal/web/maintenance_protocol.go` (`maintProtocol` :24, `loadMaintProtocol` :258, `renderMaintProtocolPDF` :337, :355)
- Test: `internal/web/maintenance_protocol_test.go`, `internal/web/company_print_test.go`

**Interfaces:**
- Consumes: `h.company`, `companyAddressLine`, `companyLegalLine`, `h.contactPersonFor`.
- Produces:
  - `BaseData.Company companyPrint` mit `type companyPrint struct{ Name, Address string }` (gefüllt in `baseData`).
  - `maintProtocol` neue Felder `CompanyAddress, Legal, Contact string`; `func maintFooterLines(p *maintProtocol, created time.Time) (left1, left2 string)` – Zeile 1 „Erstellt … · <Firma oder PDH>“, Zeile 2 Pflichtangaben und „Ansprechpartner: Name, Funktion, Telefon, E-Mail“ (leer, wenn nichts da).
- [ ] **Step 1: Tests:** `TestMaintFooterLines` (ohne Firma: Zeile 1 endet auf „· PDH“, Zeile 2 leer; mit Firma und Kontakt: Zeile 1 endet auf „· Muster GmbH“, Zeile 2 enthält „Ansprechpartner: Anna Beispiel“); `TestPrintBrandUsesCompany` (Vorlage `base.gohtml` mit `Company.Name = "Muster GmbH"`, `Company.Address = "Hauptstr. 1 · 86150 Augsburg"` rendern → `.print-brand` enthält beides; ohne Firma → enthält App-Name wie bisher); `TestRenderMaintProtocolPDF` bleibt grün.
- [ ] **Step 2:** `go test ./internal/web -run 'TestMaintFooterLines|TestPrintBrand|TestRenderMaintProtocolPDF'` → FAIL.
- [ ] **Step 3:** Druckkopf: `{{if .Company.Name}}` Firmenname · Titel, darunter Anschrift klein; sonst wie bisher. PDF: `Company` = Firmenname (sonst App-Name), Anschrift als zweite Unterzeile im Kopf, Fuß zweizeilig über `maintFooterLines` (`SetY(-16)`); `Contact` aus `contactPersonFor(ctx, userID)` in `loadMaintProtocol`.
- [ ] **Step 4:** Dieselben Tests → PASS.
- [ ] **Step 5:** Commit `Druckkopf und Wartungsprotokoll mit Firmenstammdaten (0.82.18)`.

### Task 5: M365-Signaturen

**Files:**
- Modify: `internal/web/m365_signatures.go` (:32-42, :90-102, :121, :251-258, :388, :412), `internal/web/m365_templates.go:83,116`, `web/templates/m365.gohtml:167-168`, `internal/web/handler.go` (Route `POST /m365/company` entfernen)
- Test: `internal/web/m365_test.go`

**Interfaces:**
- Consumes: `h.company`, `companyAddressLine`.
- Produces:
  - `sigFields` um `{Firmenanschrift, ""}`, `{Firmentelefon, ""}`, `{Website, ""}` (leeres Exchange-Feld = fester Text).
  - `func signatureRuleHTML(src string, fixed map[string]string) string` – feste Platzhalter durch `template.HTMLEscapeString(wert)` ersetzen, die übrigen wie bisher durch `%%Exchange%%`.
  - `func companyFixedPlaceholders(p companyProfile) map[string]string` – `Firmenanschrift` = `companyAddressLine(p)`, `Firmentelefon` = `p.Phone`, `Website` = `p.Website`.
- [ ] **Step 1: Tests:** `TestSignaturePlaceholders` erwartet weiter `%%Company%%` für {Firma}; neu `TestSignatureFixedCompanyPlaceholders` (`{Firmenanschrift}` mit `Hauptstr. 1 <b>` → `Hauptstr. 1 &lt;b&gt;`; leere Werte → Platzhalter verschwindet); `TestMergeSignatureValues` unverändert; `TestM365PageRenders` zeigt statt des Eingabefelds den Firmennamen und den Verweis „Firmenstammdaten“.
- [ ] **Step 2:** `go test ./internal/web -run 'TestSignature|TestMergeSignature|TestM365'` → FAIL.
- [ ] **Step 3:** `signatureValues` und `M365PushEntraWeb` nehmen `h.company(ctx).Name` statt `m365CompanySetting`; `M365CompanySaveWeb`, Konstante und Route entfallen; M365-Seite: Anzeige + Link `/infrastructure` mit Hinweis „Firmenname, Anschrift, Telefon und Website pflegst du in der Infrastruktur am obersten Knoten (Reiter Firma). Nach einer Änderung die Signatur-Regel neu übertragen.“
- [ ] **Step 4:** Dieselben Tests → PASS; `grep -rn m365_company_name internal web` → nur die Migration.
- [ ] **Step 5:** Commit `M365-Signaturen: Firmenname und neue Platzhalter aus den Stammdaten (0.82.18)`.

### Task 6: Reiter „Firma“, Rechte, Handbuch, Version

**Files:**
- Create: `internal/web/company_web.go`, `web/templates/widgets/company.gohtml`, `internal/web/company_web_test.go`
- Modify: `internal/web/infra_web.go` (`InfraDetailData` + `CompanyTab bool`, gesetzt bei :351), `web/templates/infra_detail.gohtml` (Reiter nach ESPPDH :89, Bereich nach :415), `internal/web/handler.go` (Routen), `internal/web/perm_gate.go` (POST-Regel `company.edit`), `internal/web/help.go` (`helpChapterIDs` + `"firma"` nach `"infrastructure"`), `web/templates/help.gohtml` (Kapitel `firma`, `data-pages="infrastructure"`; Kapitel „Administration“ Recht), `VERSION`, `CHANGELOG.md`, `AGENTS.md`

**Interfaces:**
- Consumes: alles aus Task 1–5.
- Produces:
  - `func (h *Handler) canEditCompany(r *http.Request) bool` – `Scope(...).all` für `company.edit`; Testhaken `h.companyEditHook func(*http.Request) bool`.
  - `GET /infrastructure/company` (`canViewInfra`) → Vorlage `company-panel` (Ansicht oder Formular, Vorschau Fußzeile mit `contactPersonFor(getUser(r).ID)` und Briefkopf, „zuletzt geändert von … am …“, Verweis Logo nur mit `system.server_config`, Hinweis Signatur-Regel).
  - `POST /infrastructure/company` (`canEditCompany`, sonst 403) → `validateCompany`, `saveCompany`, antwortet mit `company-panel` samt Meldung.
  - Reiter nur, wenn `node.ParentID == ""` und `canViewInfra(r)`: `<a class="rec-tab" data-tab="firma" href="?tab=firma"><i class="ti ti-building"></i> Firma</a>`, Bereich lädt per `hx-get="/infrastructure/company" hx-trigger="load"`.
- [ ] **Step 1: Tests:** `TestCompanyPanelRenders` (ohne Recht: keine `<form`, Werte sichtbar; mit Recht: Formular mit `name="vat_id"`; Vorschau enthält Firmenname); `TestCompanySaveNeedsGlobalRight` (Haken liefert false → POST 403; true → 200 und Meldung „gespeichert“); `TestInfraDetailCompanyTabOnlyTopLevel` (Knoten mit `ParentID` → kein `data-tab="firma"`); `TestHelpChaptersMatch`, `TestHelpCoversAllPages`, `TestChangelogMatchesVersion` grün.
- [ ] **Step 2:** `go test ./internal/web -run 'TestCompanyPanel|TestCompanySave|TestInfraDetailCompanyTab'` → FAIL.
- [ ] **Step 3:** Bauen; Handbuch-Kapitel „Firmenstammdaten“ mit „So geht's“ (Reiter öffnen → Felder → Speichern → Vorschau), Hinweis Pflichtangaben für geschäftliche Mails und Briefe, Graph-Hinweis zum Absendernamen, Signatur-Regel neu übertragen; CHANGELOG 0.82.18 (`### Neu`: Reiter Firma, Fußzeile, Druckkopf/PDF, Platzhalter; `### Sicherheit`: neues Recht und wer es bekommt); VERSION 0.82.18; AGENTS.md Punkt 19 Stand.
- [ ] **Step 4:** `go build ./... && go vet ./...`, volle Reihe `go test ./...` mit Labor-DB und `go test -tags integration ./internal/web -run 'Terminal|Anlagenterminal|Company'` → PASS.
- [ ] **Step 5:** Commit `Firmenstammdaten: Reiter Firma, Handbuch und Änderungsprotokoll (0.82.18)`.
