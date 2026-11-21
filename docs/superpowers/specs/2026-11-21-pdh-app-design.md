# PDH-App für Android und iOS – Entwurf

Stand: 21.11.2026 · Status: Entwurf, Prüfer-Rückfragen eingearbeitet, vom Anwender noch nicht freigegeben

## 1. Absicht und Warum

Die PDH-App soll Mitarbeitern im Betrieb das **Abarbeiten von Vorgängen**
(Störungen, Tickets, Aufgaben, Wartungen) am Handy erleichtern – direkt an der
Anlage, per QR-Code, auch dort, wo das WLAN schlecht ist. **Vorgänge aus der
Produktion anlegen** ist eine zweite, je Abteilung/Rolle freischaltbare Funktion.

Vorbild für den Aufbau ist die Companion-App von Home Assistant: eine App für
jeden Server, Cockpitseiten mit Kacheln, Push-Mitteilungen, Widgets auf dem
Startbildschirm und Sensoren des Telefons, die der Mitarbeiter selbst freigibt.

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**

- Eine App (Android + iOS) aus **einem** Code, im App Store und bei Google Play.
- **Eine Installations-App im Store für alle PDH-Server** (vom Anwender
  festgelegt). Jeder Server hat seine eigene Firmen-Domain; sie wird bei der
  Installation abgefragt. Danach gehört die App fest zu diesem Server und
  übernimmt von ihm Logo, Farben und alle Einstellungen. Neue Funktionen
  kommen nur per App-Update (Apple 2.5.2: kein nachgeladener Programmcode;
  der Server liefert Daten und Freischaltungen, keinen Code).
- Zugang nur nach Freigabe durch einen Administrator; nach der Installation
  ist die App **sofort gesperrt**.
- Fester, an das Gerät gebundener Zugangstoken; Sperre jederzeit möglich.
- Cockpitseiten (Dashboards, Leitstände) werden automatisch je Rolle/Abteilung
  erzeugt; zentrale Grundeinstellung, Vorgaben je Benutzergruppe.
- Eigene App-Rechte in der Rollen- und Rechte-Matrix.
- Vorhandene PDH-QR-Codes öffnen die passende Funktion in der App.
- Arbeiten ohne Verbindung; beim Wiederverbinden wird zusammengeführt.
- Push-Assistent, Sensorik-Widgets, Sensoren des Telefons (nur mit Freigabe des
  Mitarbeiters).
- Adressen als FQDN; Name und IP sind beide erlaubt.

**Nicht-Ziele**

- Kein Nachbau der gesamten Web-Oberfläche. Verwaltung (Rechte, Stammdaten,
  Einstellungen) bleibt im Web.
- Keine Hülle um die PDH-Webseite (Apple-Ablehnung nach Richtlinie 4.2; ohne
  Verbindung nicht nutzbar).
- Keine Änderung der bestehenden `/api/v1` (lehnt Geräte-Tokens weiter ab).

**Anti-Ziele** (wäre ein Fehlschlag, auch wenn es technisch läuft)

- Die App darf **mehr** als der Benutzer im Web.
- Ein Update sperrt jemanden aus oder macht gedruckte Etiketten unbrauchbar.
- Beim Zusammenführen geht stumm etwas verloren oder es entstehen doppelte
  Vorgänge.
- Das Telefon meldet Daten über den Mitarbeiter, ohne dass er es freigegeben
  hat oder ohne dass er es sieht.

## 3. Rahmenbedingungen aus dem Bestand

- Rechte sind Datenbankzeilen (`permissions`, `role_permissions` mit
  Abteilungsebene, `user_permissions`); jedes Neue braucht ein Recht, jeder
  Bereich ein `.admin`-Recht (`AGENTS.md`, „Rechte mitpflegen“).
- Migrationen nur hinzufügend, nie ändern, in `migrations/checksums.txt`
  eintragen; alle `id`-Spalten `UUID DEFAULT pdh_uuid_v7()`
  (`TestUUIDv7Policy`). Alte und neue Server-Versionen müssen mit alten und
  neuen App-Versionen zusammenarbeiten.
- Vorhandene Kopplung (`terminal_devices`, `terminal_pairing_codes`,
  Migration 171, `internal/web/terminal_devices.go`,
  `terminal_pair_api.go`): Code → Geheimnis, nur Hash gespeichert, Sperre über
  Cache (30 s). Wird als Vorbild genutzt, nicht verändert.
- PDH selbst spricht kein TLS; davor steht nginx (Zertifikate über den
  Update-Agenten) oder Cloudflare Tunnel. Die App darf sich deshalb **nicht**
  auf TLS-Client-Zertifikate verlassen – die Identität des Geräts wird in der
  Anwendung geprüft (Abschnitt 6).
- Eigene CA vorhanden (`internal/web/pki.go`), wird für die App nicht benötigt.
- QR-Codes enthalten Web-Adressen: `/a/<uuid>` (Anlage), `/inventory/<teil>/booking[?loc=]`
  (Lager), `/k/<karte>` (Kanban); Grundadresse aus `PDH_PUBLIC_URL`, sonst aus der
  Anfrage (`publicBaseURL`, `internal/web/labels.go:79`).
- Doppel-Absende-Sperre per UUIDv7-Schlüssel (`request_once`, Migration 122,
  `internal/web/request_once.go`).
- Web-Push (VAPID) vorhanden (`webpush.go`, `push.go`, Migration 116),
  Annehmen von Alarmen `/alert/{id}/accept`.
- Dashboard-Widgets (`internal/web/dashboard_widgets.go`, `WidgetDef` mit
  Recht), Leitstände (`leitstaende`, Migration 145, Auflösung Benutzer →
  Gruppe → Abteilung → Standard in `leitstand_boards.go`), Firmenvorgaben
  (`internal/web/user_defaults.go`, Recht `system.defaults`).
- Benutzergruppen (`user_groups`) tragen heute keine Rechte; Rechte hängen an
  Rollen. Daran ändert die App nichts.
- Tickets haben keine Art/Kategorie-Spalte; `created_by` ist Pflicht.
- Universal Links (iOS) und App Links (Android) gehen nur über einen
  Namen mit gültigem Zertifikat und Dateien unter `/.well-known/`.
- Apple prüft jede Version und braucht dafür einen erreichbaren Vorführ-Server
  mit Prüfzugang.

## 4. Bauwege (Approaches)

| Weg | Kurz | Abwägung |
|---|---|---|
| **A Flutter** (gewählt) | Ein Dart-Code für beide Systeme; Widgets und Telefon-Sensoren mit kleinen Kotlin/Swift-Teilen | Offline-Logik und Konfliktregeln nur einmal; einheitliche Oberfläche; neue System-Funktionen teils mit Verzögerung |
| B Zwei eigene Apps (Kotlin + Swift) | So wie Home Assistant | Beste Widgets/Sensoren, aber alles doppelt, Apps laufen auseinander – für ein kleines Team zu teuer |
| C Kotlin Multiplatform | Gemeinsame Logik, Oberflächen je System | Oberfläche trotzdem doppelt, jüngere Werkzeuge |
| ~~Hülle um die Webseite~~ | verworfen | Apple lehnt ab; ohne Verbindung nutzlos (htmx, Seiten vom Server) |

**Gewählt vom Anwender: A (Flutter).** Server bleibt Go im PDH.

## 5. Gesamtbild

```
 Telefon (Flutter-App)                         PDH-Server (Go)
 ┌───────────────────────────┐                ┌────────────────────────────┐
 │ Geräteschlüssel (Keystore │  /api/app/v1   │ app_devices, app_requests  │
 │  / Secure Enclave)        │ ─────────────▶ │ Rechte app.*               │
 │ Token (sicherer Speicher) │  signierte     │ Cockpit-Erzeugung          │
 │ SQLite: Vorgänge, Schritte│  Anfragen      │ Schritt-Annahme + Merge    │
 │ Scanner, Push, Widgets,   │ ◀───────────── │ Push-Auftrag an Dienst     │
 │ Sensoren                  │                │ APP-Tickets                │
 └───────────────────────────┘                └─────────────┬──────────────┘
        ▲  Weckruf (nur Kennung)                             │ Server-Schlüssel
        │                                                    ▼
   Apple APNs / Google FCM  ◀──────────────  Push-Dienst `pdh-push` (zentral)
```

Repositories: **`pdh-app`** (Flutter-App), **`pdh-push`** (zentraler
Push-Dienst, Go), Server-Teile im `pdh-server`.

## 6. Zugang, Freigabe, Token, Sperre

### 6.1 Installation

1. Benutzermenü (`base.gohtml`, Benutzermenü) bekommt **„App installieren“**
   (Recht `app.use` oder jeder angemeldete Benutzer – siehe 6.5). Der Eintrag
   zeigt einen QR-Code und Store-Knöpfe.
2. Der QR-Code enthält `https://<firmen-domain>/app/setup`. Mit der Kamera
   gescannt: die Seite führt ohne App zum Store, mit App direkt in die
   Einrichtung (`pdh://setup?d=<firmen-domain>`).
3. **Beim ersten Start fragt die App die Firmen-Domain ab:** denselben
   QR-Code scannen (empfohlen) oder die Domain eintippen. Die App prüft, dass
   dort ein PDH antwortet (`GET /api/app/v1/server`), zeigt Firmenname und
   Logo zur Bestätigung und gehört danach **fest zu diesem Server**. Ein
   Wechsel auf einen anderen Server geht nur über „App zurücksetzen“
   (löscht Schlüssel, Token und Daten).
4. Die Ausweichadressen (IP, von außen) holt die App vom Server
   (Abschnitt 10).

### 6.2 Sperre ab Installation

- Beim ersten Start erzeugt die App einen **Geräteschlüssel** (P-256) im
  Android Keystore (bevorzugt StrongBox) bzw. in der iOS Secure Enclave. Er
  verlässt das Telefon nie, wird nicht gesichert und nicht übertragen.
- Bis zur Freigabe zeigt die App **nur das PDH-Logo und „Zugang anfragen“**.

### 6.3 Zugang anfragen → APP-Ticket

1. „Zugang anfragen“ fragt **Name** und **Abteilung** ab (Abteilungsliste vom
   Server, ohne Anmeldung abrufbar, nur Namen).
2. `POST /api/app/v1/requests` mit Name, Abteilung, Gerätebezeichnung,
   Betriebssystem, App-Version und **öffentlichem Geräteschlüssel**.
   Begrenzung wie bei der Terminal-Kopplung (Versuche je IP).
3. Der Server legt eine Zeile in `app_requests` an und ein **APP-Ticket**:
   - normales Ticket (damit Liste, Verlauf, Benachrichtigung, Zuweisung wie
     gewohnt funktionieren), Titel „App-Zugang: <Name> (<Abteilung>)“,
   - gekennzeichnet über eine neue Spalte `tickets.app_request_id`
     (hinzufügend, Fremdschlüssel auf `app_requests`),
   - `created_by` = eigener Systembenutzer „PDH-App“,
   - zugewiesen über die vorhandene Gruppen-Zuweisung
     (`tickets.assigned_group_id`, Migration 085) an eine Gruppe
     „App-Administratoren“, die per Migration angelegt wird (Mitglieder pflegt
     der Administrator; Bearbeiten nur mit `app.admin`).
4. Die App zeigt danach einen **QR-Code mit Anfrage-Kennung und Fingerabdruck
   des Geräteschlüssels** und den Text „Wartet auf Freigabe“.

### 6.4 App-Konfiguration durch den Administrator

Aus dem APP-Ticket öffnet der Administrator die Seite **„App-Konfiguration“**:

1. **Gerät bestätigen:** Standard ist der Vergleich eines kurzen
   **Bestätigungscodes** (6 Ziffern, aus dem Fingerabdruck des
   Geräteschlüssels), den Telefon und App-Konfiguration beide zeigen – geht
   auch per Telefon/Zuruf und ohne Kamera. Wo das Web über HTTPS läuft, kann
   der Administrator den QR-Code am Telefon zusätzlich mit der
   Browser-Kamera scannen. Ohne Bestätigung keine Freigabe. *(Festlegung des
   Entwurfs.)*
2. **Benutzer zuordnen:** vorhandenen PDH-Benutzer suchen und wählen (Abfrage
   der Benutzer im PDH, Vorschlag nach Name/Abteilung der Anfrage).
3. **Rechte:** „Rechte aus PDH übernehmen“ (Standard; die App kann dann genau,
   was der Benutzer im Web kann) oder zusätzlich **einzelne App-Rechte**
   vergeben/entziehen (`app.*`, Abschnitt 7). Nur Rechte vergeben, die man
   selbst hat (`mayGrant`/`mayGrantIn`).
4. **Vorgangsfreigabe:** welche Vorgangsarten die App zeigt und bearbeitet
   (Störungen, Tickets, Aufgaben, Wartungen, Lager …) und ob „Anlegen aus der
   Produktion“ freigeschaltet ist.
5. **Cockpit:** automatisch erzeugtes Cockpit (Abschnitt 8) oder ein anderes.
6. **Freigeben** – oder **Ablehnen/Sperren**. Das APP-Ticket wird mit Ergebnis
   geschlossen; der Verlauf bleibt.

Ein Administrator oder Manager darf das nur für seinen Bereich (Abteilung samt
Unterabteilungen, `h.hasPermRec`) und nur für Benutzer, deren Rolle er
überragt (`Outranks`, wie bei der Terminal-Kopplung).

### 6.5 Fester Zugangstoken

- Die App fragt nach der Anfrage alle paar Sekunden (zunehmender Abstand,
  höchstens 1 Minute) `GET /api/app/v1/requests/{id}` – signiert mit dem
  Geräteschlüssel. Nach der Freigabe kommt **einmalig der Zugangstoken**
  (`pdha_<zufall>`) zurück. Niemand tippt oder sieht ihn.
- Der Token liegt im sicheren Speicher (Keystore/Schlüsselbund, „nur dieses
  Gerät“). Im PDH steht nur sein SHA-256 (`app_devices.token_sha256`).
- **Gerätebindung:** jede Anfrage trägt Token **und** eine Signatur mit dem
  Geräteschlüssel über Methode, Pfad, Zeitstempel, Zufallswert und Hash des
  Inhalts (Kopfzeilen `Authorization: PDHApp <token>`, `X-PDH-Sig`,
  `X-PDH-Ts`, `X-PDH-Nonce`). Der Server prüft Signatur, Zeitfenster
  (±5 Minuten) und Wiederholung (Nonce). Ein kopierter Token ist ohne das
  Telefon wertlos.
- Der Token läuft nicht ab. Er gilt, bis er gesperrt wird.

### 6.6 Sperren

- Im PDH an Gerät, Benutzer oder APP-Ticket: „App sperren“. Wirkt beim nächsten
  Kontakt (Cache höchstens 30 s, wie `terminal_devices`). Ein deaktivierter
  PDH-Benutzer sperrt alle seine Apps.
- Die App bekommt `401 app_revoked`, **löscht Token und lokale Daten** (noch
  nicht übertragene Schritte werden vorher angezeigt und können nicht mehr
  gesendet werden) und zeigt wieder Logo + „Zugang anfragen“.
- Entsperren gibt es nicht; ein gesperrtes Gerät stellt eine neue Anfrage.

## 7. Rechte (neue Kategorie „App“)

| Recht | Bedeutung | Bei Migration vergeben an |
|---|---|---|
| `app.use` | App nutzen, „App installieren“ sehen | allen bestehenden Rollen (niemand ausgesperrt; Freigabe je Gerät bleibt Pflicht) |
| `app.create` | Vorgänge aus der App anlegen (Zweitfunktion) | admin, manager |
| `app.cockpit` | Cockpits zentral anpassen und zusammenstellen | admin |
| `app.sensors.view` | Von Telefonen gemeldete Sensorwerte sehen | admin |
| `app.admin` | Bereichsadministrator: Anfragen bearbeiten, Geräte freigeben, sperren, endgültig löschen | admin, manager (in ihrer Abteilung) |

- **Wirksames Recht = Rolle/Benutzer ∩ Gerät.** `app.*` wird wie jedes Recht
  über Rolle bzw. `user_permissions` vergeben (Abteilungsebene, `mayGrant`).
  Die Einstellungen am Gerät (Vorgangsfreigabe, Anlegen) können **nur
  einschränken**, nie etwas geben, was der Benutzer nicht hat.
  *(Festlegung des Entwurfs.)*
- App-Rechte gelten **zusätzlich** zu den normalen Rechten. Jede Aktion über
  `/api/app/v1` prüft das normale Recht (`hasPermRec`) **und** die
  Vorgangsfreigabe des Geräts. Die App kann nie mehr als der Benutzer im Web.
- Abteilungsebene wie überall (`role_permissions.department_id`).
- „Anlegen aus der Produktion“ ist damit je Rolle **und** Abteilung
  einschaltbar; zusätzlich je Gerät in der Vorgangsfreigabe.

## 8. Cockpitseiten

- **Bausteine:** vorhandene Dashboard-Widgets (`WidgetDef`) und
  Leitstand-Blöcke, dazu App-eigene Kacheln (Störung melden, Scannen, Meine
  Vorgänge, Sensorwerte). Jeder Baustein hat sein Recht; was der Benutzer nicht
  darf, erscheint nicht.
- **Automatisch erzeugt:** beim Einspielen und bei neuen Rollen/Abteilungen
  erzeugt der Server je Rolle (und je Abteilung, wo vorhanden) ein Cockpit aus
  Regeln, z. B.
  - Instandhalter/Techniker: Meine Vorgänge · Störungen meiner Abteilung ·
    fällige Wartungen · Scannen,
  - Werker: Störung melden · Scannen · meine Meldungen,
  - Manager: Kennzahlen der Abteilung · offene Störungen · Leitstand.
  Erzeugte Cockpits sind als „automatisch“ markiert und werden bei neuen
  Bausteinen ergänzt, solange niemand sie von Hand geändert hat.
- **Wer welches Cockpit bekommt:** eigene Wahl → Benutzergruppe → Abteilung →
  Rolle → zentrale Grundeinstellung (Reihenfolge wie bei den Leitständen).
- **Zentral einstellen:** Seite „App-Cockpits“ im Web (Recht `app.cockpit`):
  ansehen, anpassen, neu zusammenstellen, Gruppen/Abteilungen zuordnen,
  Grundeinstellung festlegen.
- **Aufbau in der App** wie Home Assistant: Leiste unten mit den Cockpitseiten,
  Kacheln, Wischen zwischen Seiten; Fiori/Fluent und hell/dunkel nach
  PDH-Einstellung.
- Speicherung: neue Tabelle `app_cockpits` (JSONB-Bausteine, Zuordnung zu
  Gruppe/Abteilung/Rolle, Kennzeichen automatisch/geändert).

## 9. Arbeiten ohne Verbindung und Zusammenführen

### 9.1 Was auf dem Telefon liegt

- Offene Vorgänge des Benutzers und seiner Cockpits, die Anlagen und
  Lagerorte, die er per QR-Code öffnen darf (nur Stammdaten, die die App
  braucht), Inhalte der Cockpit-Kacheln mit Stand-Zeit.
- Lokale SQLite-Datenbank, verschlüsselt; beim Sperren gelöscht.
- Abgleich bei jedem Start, im Vordergrund regelmäßig und nach
  Push-Mitteilungen; ändernde Stände kommen als „seit Stand X“.

### 9.2 Schritte statt Datensatz-Kopien

- Jede Handlung ist ein **Schritt** (z. B. Status setzen, Kommentar, Zeit
  buchen, Foto, Messwert, Maßnahme, Wartungspunkt abhaken, Störung melden).
- Jeder Schritt und jeder neu angelegte Vorgang bekommt seine **UUIDv7 schon
  auf dem Telefon** und behält sie für immer (auch als `id` des Vorgangs im
  PDH). Der Server nimmt jeden Schritt **genau einmal** an (Schlüssel = Kennung
  des Schritts, Tabelle `app_steps`, Muster wie `request_once`).
- Schritte werden in der Reihenfolge ihrer Entstehung gesendet.

### 9.3 Regeln beim Zusammenführen (vom Anwender festgelegt)

1. **Der Status im PDH gilt.** Wurde der Vorgang inzwischen von jemand anderem
   geändert (z. B. geschlossen), übernimmt die App diesen Stand; der
   Statusschritt aus der App entfällt. Keine Rückfrage.
   „Inzwischen“ heißt: jeder Statusschritt trägt den Stand (Status +
   Änderungszeit), den das Telefon zuletzt gesehen hat. Hat sich der Status
   im PDH seitdem **nicht** geändert, gilt der Schritt aus der App ganz
   normal. *(Festlegung des Entwurfs.)*
2. **Nur Hinweis:** die App zeigt z. B. „Störung 1234 wurde inzwischen von
   M. Huber geschlossen. Dein Kommentar wurde angehängt.“ Der Hinweis steht
   auch im Verlauf des Vorgangs.
3. **Alles andere wird zusammengeführt:** Kommentare, Zeiten, Fotos,
   Messwerte, Maßnahmen werden an **den bestehenden Vorgang** angehängt –
   auch wenn er inzwischen geschlossen ist.
4. **Nie ein neuer Datensatz** als Ersatz oder Kopie.
5. **Offline neu gemeldete Störung:** ist an derselben Anlage schon eine
   Störung offen, wird die Meldung als Hinweis an diese gehängt statt eine
   zweite anzulegen (Vorschlag, vom Anwender angenommen). Maßgeblich ist,
   was beim **Eintreffen** offen ist (auch Störungen, die erst nach dem
   Verbindungsverlust gemeldet wurden). Die Telefon-Kennung bleibt als
   **dauerhafter Verweis** auf die bestehende Störung erhalten
   (Tabelle `app_id_aliases`); spätere Schritte (Foto, Zeit), die auf die
   Telefon-Kennung zeigen, landen dort. Die App übernimmt die Kennung der
   bestehenden Störung für die Anzeige. *(Festlegung des Entwurfs.)*
6. Ein Schritt, den der Benutzer zum Zeitpunkt des Eintreffens nicht mehr darf
   (Rechte entzogen), wird abgelehnt und in der App mit Grund angezeigt – nie
   stumm verworfen.

### 9.3a Was ohne Verbindung geht

- Ohne Verbindung: Störungen, Tickets, Aufgaben, Wartungen (ansehen,
  bearbeiten, Störung melden).
- **Nur mit Verbindung:** Lagerbuchungen und Kanban-Karten – Bestände
  vertragen kein nachträgliches Zusammenführen (negativer Bestand, Teil schon
  verbraucht). Die App zeigt dort „Nur mit Verbindung möglich“.
  *(Festlegung des Entwurfs; später erweiterbar.)*

### 9.4 Hinweis bei Verbindungsverlust

- Fester Balken oben: „Keine Verbindung – n Änderungen warten“. Er verschwindet
  erst, wenn alles übertragen ist. Kachelinhalte zeigen ihre Stand-Zeit.

## 10. Adressen (FQDN und IP)

- Die App speichert je Server **mehrere Adressen** (z. B. intern per Name,
  intern per IP, von außen per Name/Cloudflare) und nimmt die erste, die
  antwortet; Reihenfolge lernt sie aus Erfolg.
- Der Server nennt seine Adressen (`GET /api/app/v1/server`): neue Einstellung
  „Adressen für die App“ in den Core-Einstellungen; `PDH_PUBLIC_URL` ist die
  erste.
- **QR-Codes:** neue Etiketten und Seiten verwenden den Namen aus
  `PDH_PUBLIC_URL` (bisher schon, sofern gesetzt). Gedruckte Etiketten mit IP
  bleiben gültig: der Scanner der App erkennt **jede bekannte Adresse** seines
  Servers und wertet nur den Pfad aus.
- **Mit der normalen Kamera:** fremde Firmen-Domains lassen sich nicht in
  eine einzige Store-App eintragen (iOS „Associated Domains“ und Android App
  Links stehen beim Bau fest). Deshalb gibt es **keine** Universal Links /
  App Links. Stattdessen:
  - **Eigenes Schema `pdh://`**: die Webseite eines gescannten Etiketts
    zeigt am Handy oben „In der App öffnen“ und springt mit einem Tipp in
    die App (`pdh://open?d=<domain>&p=<pfad>`). Die App öffnet nur Ziele
    ihres eigenen Servers; eine fremde Domain wird abgelehnt.
  - Auf den Etiketten bleibt die Web-Adresse – sie funktionieren mit und
    ohne App, auch alte Etiketten mit IP.
- **Mit dem Scanner der App:** öffnet immer direkt (erkennt jede bekannte
  Adresse seines Servers).
- App-Verbindungen nur über HTTPS – Ausnahme: Adresse im eigenen Netz, die der
  Administrator in „Adressen für die App“ ausdrücklich freigibt
  (Gerätebindung schützt die Anfragen auch dann).

## 11. QR-Codes in der App

| Pfad im Code | Funktion in der App |
|---|---|
| `/a/<uuid>`, `/e/<uuid>` | Anlage: offene Störungen, Tickets, Aufgaben, Wartungen; „Störung melden“ (mit `app.create`) |
| `/inventory/<teil>/booking[?loc=]` | Lagerbuchung |
| `/k/<karte>` | Kanban-Karte |
| `/app/setup?…` | Server eintragen |
| bare UUID / sonst | wie `/scan?code=` heute: Anlage oder Lagersuche |

Neue Ziele kommen künftig mit einer Tabelle Pfad → Funktion hinzu; unbekannte
Pfade öffnen die Webseite im Browser.

## 12. Push-Mitteilungen und Push-Assistent

- **Zustellung über einen zentralen Push-Dienst `pdh-push`** (vom Anwender
  festgelegt, Weg b). Grund: eine Store-App hat genau einen Schlüssel bei
  Apple (APNs `.p8`) und Google (FCM-Dienstkonto); läge er auf den Servern
  verschiedener Firmen, könnte jeder Server alle Nutzer aller Firmen
  erreichen.
  - Nur `pdh-push` hält die Apple-/Google-Schlüssel.
  - Jeder PDH-Server meldet sich einmal bei `pdh-push` an und bekommt einen
    **eigenen Server-Schlüssel** (in `server_settings`, `enc:v1:`). Damit
    erreicht er **nur die Geräte, die sich bei ihm angemeldet haben**
    (Zuordnung Gerät ↔ Server im Dienst; die App meldet ihr Push-Kennzeichen
    über ihren PDH-Server an).
  - Die Mitteilung enthält **nur eine Kennung** (und ggf. einen neutralen
    Text wie „Neue Störung“), keine Firmendaten. Inhalte holt die App direkt
    vom Firmen-Server. `pdh-push` speichert keine Inhalte und protokolliert
    nur Zähler.
  - `pdh-push` ist klein (Go, ein Dienst, eine Datenbank), wird vom
    Herausgeber betrieben (Anwender), braucht HTTPS mit öffentlicher Domain.
    Fällt er aus, holt die App Neuigkeiten beim Öffnen selbst ab.
  - Ein PDH-Server braucht dafür ausgehend Internet zu `pdh-push`. Ohne
    Internet: die App holt Mitteilungen nur bei geöffneter App selbst ab
    (keine Zustellung im Hintergrund – wird im Assistenten gesagt).
- Die vorhandenen Benachrichtigungswege (Web-Push, Alarme) bekommen das
  App-Gerät als zusätzliches Ziel; Annehmen direkt aus der Mitteilung wie
  `/alert/{id}/accept`.
- **Push-Assistent** bei der Einrichtung und unter „Einstellungen“: wählt, was
  meldet (neue Störung in meiner Abteilung, mir zugewiesen, Wartung fällig,
  Konflikthinweis …), Ruhezeiten. Vorgabe je Benutzergruppe/Abteilung vom
  Administrator (gleiche Auflösung wie Cockpits), der Benutzer darf abweichen.

## 13. Sensorik-Widgets

- **(a) PDH-Messwerte anzeigen:** ESPPDH-, MQTT- und Energie-Werte als Kacheln
  im Cockpit und als Widgets auf dem Startbildschirm (Android App Widgets,
  iOS WidgetKit). Auswahl nur aus Werten, die der Benutzer sehen darf.
  Widgets zeigen ihren Stand; Aktualisierung so oft, wie das System es erlaubt.
- **(b) Sensoren des Telefons ans PDH melden** (Akku, WLAN/Netz, grobe
  Halle/Bereich über das WLAN, ggf. weitere):
  - jeder Sensor ist **aus**, bis der **Mitarbeiter selbst** ihn einschaltet;
    er sieht jederzeit, was gemeldet wird, und schaltet es wieder aus,
  - keine GPS-Spuren; Halle nur grob,
  - Ansehen im PDH nur mit `app.sensors.view`,
  - Werte werden nach einer festen Frist gelöscht (Builder schlägt Frist vor,
    Anwender bestätigt).
  - Hinweis: (b) betrifft Mitarbeiterdaten – Betriebsrat/Datenschutz vor dem
    Einschalten im Betrieb (Aufgabe des Anwenders).

## 14. Store und Auslieferung

- **Eine Installations-App** mit neutralem PDH-Namen und -Symbol für alle
  Firmen; Firmenlogo und -farben erst in der App (Abschnitt 14a). Ein
  Store-Eintrag je System, eine Apple-Prüfung je Version.
- Im **App Store als „nicht gelistete“ App** (nur über Link auffindbar) und
  bei **Google Play**; zusätzlich APK-Download aus dem PDH für Android ohne
  Play Store.
- **Neue Versionen müssen installiert werden** (vom Anwender festgelegt):
  der Server nennt die neueste und die **mindestens nötige** App-Version
  (`GET /api/app/v1/server`, Einstellung in den Core-Einstellungen). Neuere
  Version vorhanden → Hinweis mit Knopf zum Store. Unter der Mindestversion →
  App sperrt die Bedienung bis zur Aktualisierung (noch nicht übertragene
  Schritte werden vorher noch gesendet, sofern die Schnittstelle sie
  annimmt).
- **Wie lange alte Versionen mitlaufen** (vom Anwender festgelegt): gar nicht
  bis höchstens **einen Schritt zurück**. Einstellung „Ältere App-Version
  erlauben: 0 / 1“ in den Core-Einstellungen; Grundwert 1 *(Festlegung des
  Entwurfs: wer mitten in der Schicht steht, kann noch fertig arbeiten)*.
  Die Mindestversion ergibt sich daraus automatisch aus der neuesten
  Version.
- „Verfügbar“ gilt **je System**: eine Version zählt erst, wenn sie im
  jeweiligen Store freigegeben ist (Apple prüft teils Tage länger als
  Google). Die neueste Version je System meldet **`pdh-push`** den
  PDH-Servern (der Herausgeber trägt sie dort beim Veröffentlichen ein);
  Rückfall: Eintrag von Hand in den Core-Einstellungen. *(Festlegung des
  Entwurfs.)*
- Damit muss ein PDH-Server nur die **aktuelle und die vorige** App-Version
  bedienen; die Schnittstelle wird trotzdem nur hinzufügend erweitert.
- **Umgekehrt (Firmen aktualisieren ihr PDH unterschiedlich schnell):** der
  Server nennt seine Schnittstellen-Stufe. Die App unterstützt PDH-Server ab
  einer festen Mindeststufe; Funktionen, die ein älterer Server noch nicht
  kann, blendet sie aus. Ist der Server zu alt, zeigt sie „Bitte PDH-Server
  aktualisieren“ statt Fehlern. *(Festlegung des Entwurfs.)*
- Vom Anwender selbst anzulegen: Apple-Developer-Konto der Firma (99 $/Jahr),
  Google-Play-Konto (25 $ einmalig), Firebase-Projekt, Betrieb von
  `pdh-push` (kleiner Server mit öffentlicher Domain und HTTPS).
- **Vorführ-Server** mit Testdaten und Prüfzugang für die Apple-Prüfung,
  ohne Firmendaten.
- Datenschutzangaben in beiden Stores (Sensoren!) und Datenschutzerklärung.

## 14a. Logo und Farben aus dem PDH

Vom Anwender festgelegt: die App übernimmt Logo und Farben des PDH bei der
ersten Installation bzw. beim ersten Start nach einem Update.

- Quelle ist das vorhandene Erscheinungsbild (`internal/web/branding.go`):
  Firmenname (`AppName`), App-Logo (`branding.logo.app`, liegt öffentlich
  unter `/uploads/branding/`), Farbschema (`Theme`), eigene Firmenfarbe
  (`ThemeColor`), Design-Stil Fiori/Fluent (`Style`), Schrift.
- `GET /api/app/v1/branding` (ohne Anmeldung, wie die Anmeldeseite) liefert
  diese Werte samt Logo-Adresse und einem **Stand** (Prüfsumme).
- **In der App** (Sperrbildschirm „Zugang anfragen“, Startbild, Kopf,
  Akzentfarbe, Cockpits): beim ersten Start und beim ersten Start nach jedem
  Update geholt und gespeichert – damit trägt schon der Sperrbildschirm das
  Firmenlogo. Ändert sich das Erscheinungsbild im PDH später, holt die App es
  beim nächsten Abgleich (Stand verglichen) *(Festlegung des Entwurfs; sonst
  bliebe ein altes Logo bis zum nächsten Update stehen)*.
- **Symbol auf dem Startbildschirm und Name im Store** bleiben das neutrale
  PDH-Symbol bzw. „PDH“ – sie lassen sich nach der Installation nicht ändern
  und sind bei einer App für alle Firmen für alle gleich. Firmenlogo und
  -farben erscheinen ab dem Startbild in der App.
- Fehlt ein eigenes Logo, gilt das PDH-Logo und das Standard-Farbschema.

## 15. Server-Seite im Überblick (pdh-server)

- Migrationen (hinzufügend, UUIDv7): `app_requests`, `app_devices`
  (Benutzer, öffentlicher Schlüssel, Token-Hash, Vorgangsfreigabe,
  Cockpit, gesperrt), `app_steps`, `app_cockpits`, `app_push_targets`,
  `app_sensor_values`; `tickets.app_request_id`; Rechte `app.*`;
  Systembenutzer „PDH-App“; Gruppe „App-Administratoren“.
- `/api/app/v1/*`: Server-Info, Abteilungen, Anfrage, Abholen, Abgleich,
  Schritte, Cockpit, Push-Anmeldung, Sensoren. Eigene Prüfung (Token +
  Signatur + Sperre + Rechte), unabhängig von `/api/v1`.
- Web: Benutzermenü „App installieren“, Seite „App-Konfiguration“ (aus dem
  APP-Ticket), Seite „App-Geräte“ (Übersicht, sperren), „App-Cockpits“,
  Einstellung „Adressen für die App“, Seite `/app/setup` (Einrichtung und
  Store-Weiterleitung), Knopf „In der App öffnen“ auf den QR-Zielseiten am
  Handy, Anmeldung bei `pdh-push` in den Core-Einstellungen.

## 15a. Zentraler Push-Dienst (pdh-push)

- Eigenes Repository `pdh-push`, Go, eine kleine Datenbank (Postgres oder
  SQLite), betrieben vom Herausgeber.
- Hält allein die Apple-/Google-Schlüssel der Store-App.
- Server-Anmeldung: PDH-Server registriert sich mit seiner Domain, bekommt
  einen eigenen Server-Schlüssel; der Herausgeber kann Server sperren.
- Geräte-Anmeldung: läuft über den PDH-Server (die App gibt ihr
  Push-Kennzeichen an ihren Server, der meldet es bei `pdh-push` für sich
  an). Ein Server erreicht nur seine eigenen Geräte.
- Senden: `POST /v1/send` mit Gerät(en) und Kennung; keine Inhalte,
  Begrenzung je Server, nur Zähler im Protokoll.
- Meldet die neueste App-Version je System (Abschnitt 14).
- Handbuch-Kapitel „PDH-App“ mit So-geht's-Schritten; Kapitel
  „Administration“ ergänzen; CHANGELOG je Version.
- Versionen: nächste Korrekturversionen (Regel in `AGENTS.md`).

## 16. Pakete und Reihenfolge

Jedes Paket bekommt einen eigenen Plan und ist für sich einspielbar.

1. **Server-Grundlage:** Rechte, Tabellen, `/api/app/v1` für Anfrage,
   Freigabe, Token, Gerätebindung, Sperre; APP-Ticket; App-Konfiguration;
   Benutzermenü-Eintrag; Adressen.
2. **App-Grundgerüst (Flutter):** Repository `pdh-app`, Geräteschlüssel,
   Domain-Abfrage bei der Einrichtung, Logo/Farben vom Server,
   Sperrbildschirm, Zugang anfragen, Abholen des Tokens, Schema `pdh://`,
   Pflicht-Aktualisierung, Cockpit online, Scanner mit QR-Zielen, Vorgänge
   ansehen und bearbeiten.
3. **Ohne Verbindung:** lokale Datenbank, Schritte, Zusammenführen nach 9.3,
   Hinweisbalken.
4. **Cockpit-Erzeugung und zentrale Cockpits** (Server + App).
5. **Push:** Dienst `pdh-push`, Anmeldung im PDH, Push-Assistent.
6. **Sensorik:** (a) Widgets/Kacheln, danach (b) Telefon-Sensoren.
7. **Store:** Vorführ-Server, Store-Einträge, Datenschutzangaben, Abgabe.

## 17. Dem Builder überlassen

- Flutter-Pakete (Zustand, SQLite/Verschlüsselung, Scanner, Widgets),
  Ordnerstruktur der App, genaue JSON-Formate der Schnittstelle.
- Genaue Regeln für die automatisch erzeugten Cockpits (Vorschlag in 8 als
  Ausgangspunkt).
- Abstände beim Abholen und Abgleichen, Grenzen für Versuche.
- Aufbewahrungsfrist der Telefon-Sensorwerte (Vorschlag an den Anwender).
- Darstellung von Fiori/Fluent in Flutter.
