# Firmenstammdaten (0.82.21) – Entwurf

Stand 21.11.2026, zur Bestätigung durch den Anwender.

## Anlass und Ziel

Wunsch des Anwenders: „Was noch fehlt ist ein Firmen Stammdaten Fenster. Am
einfachsten in Infrastruktur beim übergeordneten Knoten. Die dort
eingegebenen Daten gelten unternehmensweit, als Ansprechpartner im Mail- und
Postverkehr gilt der username als Ansprechpartner.“

Das PDH soll die eigene Firma einmal kennen – Name, Anschrift, Kontakt,
Pflichtangaben – und sie überall verwenden, wo es nach außen schreibt oder
druckt. Ansprechpartner ist die Person, die die Mail oder den Ausdruck auslöst.

## Entscheidungen des Anwenders (21.11.2026)

- **A jetzt, B danach:** Dieses Paket (A) bringt die Daten in alle Mails des
  PDH, in den Druckkopf, in das Wartungsprotokoll-PDF und in die
  M365-Signaturen. Die Bestellung als PDF an den Lieferanten (B) ist ein
  eigenes Paket danach.
- **Variante 1:** Es gibt **eine** Firma. Die Daten liegen einmal vor; der
  Reiter steht an jedem obersten Knoten der Infrastruktur und zeigt dieselben
  Daten. Werke werden unter den obersten Knoten gehängt; eigene Angaben eines
  Werks (z. B. Lieferanschrift) kommen später mit B an den Werk-Knoten.

## Ziele

- Firmenstammdaten an einer Stelle pflegen, sichtbar für alle, die die
  Infrastruktur sehen.
- Jede Mail des PDH trägt den Firmennamen als Absendernamen und eine
  Fußzeile mit Ansprechpartner, Firma, Anschrift und Pflichtangaben.
- Druckkopf und Wartungsprotokoll zeigen Firma und Anschrift statt nur den
  App-Namen.
- {Firma} der M365-Signaturen kommt aus den Stammdaten (keine zweite Stelle
  mehr für den Firmennamen).

## Nicht-Ziele

- Bestellung als PDF, Lieferanschriften je Werk (Paket B).
- Mehrere Firmen / Mandanten.
- Logo: bleibt im Erscheinungsbild (Server-Einstellungen), der Reiter verweist
  nur darauf.
- App-Name (Kopfzeile „PDH“), Anmeldeseite, Nextcloud-Theming,
  Zertifizierungsstelle bleiben unverändert.
- Mails der Nextcloud-Brücke (fremder Inhalt) bekommen keine Fußzeile.

## Anti-Ziele

- Leere Stammdaten dürfen nichts verschlechtern: ohne Angaben sehen Mails und
  Ausdrucke aus wie heute.
- Ein Recht, das nur in einer Abteilung gilt, darf die unternehmensweiten
  Daten nicht ändern.

## Ansätze

- **1 – Eigene Tabelle mit einer Zeile, Reiter an jedem obersten Knoten**
  (gewählt): klare Felder mit Prüfung, eine Quelle für alle Verwender.
- 2 – Daten je oberstem Knoten mit „Hauptsitz“: verworfen (Variante 2 vom
  Anwender abgelehnt).
- 3 – Feldsatz am Knoten: verworfen (keine Feldtypen für E-Mail/IBAN, nicht
  zuverlässig von Mails und PDFs lesbar).
- 4 – Seite in den Server-Einstellungen: verworfen (Anwender will es in der
  Infrastruktur).

## Daten

Neue Tabelle `company_profile` mit genau einer Zeile (`id boolean PRIMARY KEY
DEFAULT true CHECK (id)`), Migration 193:

| Feld | Bedeutung | Länge |
|---|---|---|
| `name` | Firma mit Rechtsform, z. B. „Muster GmbH“ | 200 |
| `name_addition` | Zusatz (Abteilung, „Werk Augsburg“) | 200 |
| `street`, `postal_code`, `city`, `country` | Anschrift (Land Standard „Deutschland“) | 200/20/100/100 |
| `phone`, `fax`, `email`, `website` | allgemeiner Firmenkontakt | 50/50/200/200 |
| `registered_office` | Sitz der Gesellschaft | 100 |
| `register_court`, `register_no` | Registergericht, Registernummer (z. B. HRB 12345) | 100/50 |
| `management` | Geschäftsführung / Vorstand (Freitext) | 300 |
| `supervisory_chair` | Vorsitz Aufsichtsrat (optional) | 200 |
| `vat_id`, `tax_no` | USt-IdNr., Steuernummer | 30/30 |
| `bank_name`, `iban`, `bic` | Bankverbindung (optional, nur Anzeige im Reiter) | 100/34/11 |
| `updated_at`, `updated_by` | wer zuletzt geändert hat | – |

Die Migration legt die Zeile leer an, übernimmt einen vorhandenen Wert aus
`app_settings.m365_company_name` nach `name` und löscht diesen Schlüssel.

Prüfung beim Speichern: E-Mail-Form, Website mit http(s), IBAN mit
Prüfsumme (mod 97), USt-IdNr. zwei Buchstaben plus 2–12 Zeichen; leere Felder
sind erlaubt.

## Rechte

- Neues Recht `company.edit` „Firmenstammdaten bearbeiten“, Kategorie
  „Stammdaten“. Per Migration an die Rollen, die heute `system.server_config`,
  `m365.templates.manage` oder `system.manage_roles` haben (den Firmennamen
  für Signaturen pflegt heute, wer `canManageM365` erfüllt:
  `m365.templates.manage` oder Rollenverwaltung – so verliert niemand etwas).
- Bearbeiten nur, wenn das Recht **überall** gilt (nicht nur in einer
  Abteilung) – die Daten sind unternehmensweit.
- Ansehen: wer die Infrastruktur sehen darf (`canViewInfra`).
- Kein Löschen (Felder leeren genügt), daher kein `.admin`-Recht.
- Rechte nur weitergeben, die man hat (bestehende Regel).

## Oberfläche

- Reiter **„Firma“** auf der Detailseite jedes obersten Knotens
  (`parent_id IS NULL`). Inhalt per htmx von `GET /infrastructure/company`,
  Speichern `POST /infrastructure/company`. Oben der Satz „Gilt für das
  ganze Unternehmen – an jedem obersten Knoten gleich.“
- Ansicht mit Gruppen „Firma & Anschrift“, „Kontakt“, „Pflichtangaben“,
  „Bank“; mit Recht als Formular.
- Daneben eine **Vorschau**: Mail-Fußzeile (mit dem angemeldeten Benutzer als
  Ansprechpartner) und Briefkopf.
- Verweis auf das Logo im Erscheinungsbild (nur mit `system.server_config`).
- Auf der M365-Seite ersetzt eine Anzeige mit Verweis auf den Reiter das
  bisherige Eingabefeld für den Firmennamen.
- Fiori und Fluent, hell und dunkel, 320–1366 px.

## Ansprechpartner

Aus dem Benutzerstamm der auslösenden Person: Vor- und Nachname, Funktion
(`job_title`), Telefon (`phone` wie in den Signaturen; ist es leer, die
Firmennummer mit der Durchwahl `phone_internal` dahinter), Mobil, E-Mail
(Festlegung beim Entwurf: die Durchwahl allein ist nach außen keine
vollständige Nummer). Leere Felder
entfallen. Mails ohne auslösende Person (Bestellvorschlag, Statusmeldungen,
Kennwort zurücksetzen) nennen statt einer Person den allgemeinen
Firmenkontakt (Telefon, E-Mail der Firma).

## Mails

- **Absendername:** Firmenname, wenn `PDH_SMTP_FROM` keinen eigenen Namen
  enthält (ein ausdrücklich eingestellter Name geht vor). Bei Versand über
  Microsoft Graph bestimmt das Postfach den Namen – Hinweis im Handbuch.
- **Fußzeile** (HTML – die Mails des PDH sind reines HTML, `sendMail` in
  `purchase_report.go`) unter jeder Mail des PDH:
  Ansprechpartner-Block, Firma (+ Zusatz), Anschrift, Telefon · E-Mail ·
  Website, eine Zeile Pflichtangaben (Sitz · Registergericht Registernummer ·
  Geschäftsführung · USt-IdNr.). Fehlt `name`, gibt es keine Fußzeile.
- Betroffen: Bestellvorschlag, Statusmeldungen, Testmails, Kennwort
  zurücksetzen (alle über den gemeinsamen Versand).

## Ausdrucke

- **Druckkopf** jeder Seite (`base.gohtml`, `.print-brand`): Logo, Firmenname,
  darunter Anschrift einzeilig; ohne Stammdaten wie heute (App-Name).
- **Wartungsprotokoll-PDF:** Kopf mit Firmenname und Anschrift (statt
  App-Name), Fuß mit Pflichtangaben einzeilig und „Ansprechpartner: …“ der
  Person, die das Protokoll erzeugt.

## M365-Signaturen

Entscheidung des Anwenders (21.11.2026, „B“): {Firma} bleibt das
Exchange-Feld `%%Company%%`.

- „Werte nach Microsoft 365 übertragen“ schreibt `companyName` aus
  `company_profile.name` in die Konten (bisher aus `m365_company_name`); ist
  der Name leer, bleibt wie bisher der Wert aus Entra.
- Die Vorschau der Signatur zeigt für {Firma} den Firmennamen aus den
  Stammdaten (sonst Entra).
- Neue Platzhalter {Firmenanschrift}, {Firmentelefon}, {Website}: dafür gibt
  es kein Exchange-Feld – sie kommen beim Erzeugen der Regel als fester Text
  aus den Stammdaten hinein. Nach einer Änderung der Stammdaten zeigen der
  Reiter „Firma“ und die M365-Seite den Hinweis, die Regel neu zu übertragen.

## Handbuch und Protokoll

- Neues Kapitel „Firmenstammdaten“ (Kontexthilfe auf der Infrastruktur-
  Detailseite), Anleitung „So geht's“, Hinweis zu Pflichtangaben.
- Kapitel „Administration“ (neues Recht), CHANGELOG `### Neu`,
  `### Sicherheit`.
- Jede Änderung landet im Server-Protokoll (Bereich „stammdaten“) mit
  Benutzer und geänderten Feldern; der Reiter zeigt „zuletzt geändert von … am
  …“ (`updated_by`, `updated_at`). Eine Datensatz-Historie gibt es nicht
  (`record_history` braucht eine UUID).

## Dem Bauenden überlassen

- Zwischenspeicher für die Stammdaten (wie `brandCache`) oder Lesen je Aufruf.
- Genaue Anordnung von Formular und Vorschau.
- Wie der gemeinsame Versand die auslösende Person erfährt (Parameter oder
  Kontext).
