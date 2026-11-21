# ESPPDH: alle Sensoren, Webserver, Werte an Anlage und Terminal, Handshake

Stand: 21.11.2026 – Teil 1 und Teil 2 vom Anwender freigegeben (21.11.2026);
Teil 3 noch zu besprechen.
Baut auf `docs/superpowers/specs/2026-11-18-esppdh-design.md` (Paket 1, 0.82.0)
und `2026-11-19-esphome-aktuell-design.md` (0.82.1) auf.

Drei Teile, je eigene Korrekturversion und eigene Freigabe:

| Teil | Version | Inhalt | Stand des Entwurfs |
|---|---|---|---|
| 1 | 0.82.11 | Webserver v3, Adressmeldung, Reiter „ESPPDH“ an der Anlage, Sensorwerte am Anlagenterminal, Handshake-Zähler | ausgearbeitet |
| 2 | 0.82.12 | Sensorkatalog aus ESPHome mit Gruppen und Feinschliff | ausgearbeitet |
| 3 | 0.82.13 | Plausibilitätsprüfung mit Betriebszeiten der Anlage | nur Wunsch erfasst, wird vor dem Bau eigens besprochen |

## Warum

Der Anwender will mit ESPPDH nicht nur Strom und Stückzahl messen, sondern
alles, was ESPHome an Sensoren kennt – ohne YAML zu schreiben. Die Werte sollen
dort sichtbar sein, wo im Betrieb gesucht wird: an der Anlage und am
Anlagenterminal für den Instandhalter. Ein laufender Zähler (Handshake) zeigt
auf einen Blick, ob eine Quelle noch lebt.

Wörtlich (21.11.2026):

- „es sollen alle sensoren von esphome zur verfügung stehen sortiert nach
  gruppierungen, der webserver v3 soll standard für alle sein und ein link im
  infrastruktur element ebenso sollen die daten im infrastrukturelement als
  zusätzlicher reiter esppdh incl. verlinkung zum esppdh element“
- „und im terminal sollen die dazugehörigen sensoren abgefragt werden können
  als instandhalter“
- „baue auch überall den lesbaren handshake dass im anlagenstand der handshake
  zähler sichtbar läuft“
- „es soll auch eine plausibilitätsabfrage der sensoren stattfinden zu lange
  keine änderung und änderung im zeitraum nicht plausiebel minimal und
  maximalzeit, temperatur bereiche automatisches aktivieren nach schichtplan
  (arbeitstage,pausenzeiten,wartung und störung) der Anlage gleich mit
  einbauen“ (→ Teil 3)

## Bestand (geprüft am 21.11.2026)

- Bausteine: vier von Hand gebaute Arten in `internal/modules/esppdh/blocks.go`
  (S0-Zähler, SCT-013, PZEM-004T, Stückzähler) mit deutscher Hilfe,
  Pin-Prüfung je Platine (`Board.PinsFor`) und festen Themen
  `esppdh/<kennung>/<art>-<nr>/<wert>`. Gespeichert in `esppdh_blocks`
  (`kind`, `nr`, `params` JSONB) – allgemein genug für neue Arten.
- Erzeugte Konfiguration (`yaml.go`): `wifi` + `captive_portal` (oder
  `ethernet`), `mqtt` mit `retain: true`, `discovery: false`, Geburts- und
  Abschiedsnachricht. **Kein** `web_server`, **kein** `ota`, keine Meldung der
  eigenen Adresse, kein Laufzeit-Zähler.
- ESPHome hält sich seit 0.82.1 selbst aktuell: mehrere Versionen nebeneinander
  unter `/opt/pdh-esphome-versions/<version>/`, Zeiger `active`/`previous`
  (`versions.go`). Die Version kann sich je Server und Tag ändern.
- Das PDH speichert die Messwerte der ESPPDH-Geräte noch nicht (Paket 2). Der
  eingebaute Broker (`internal/core/mqttbroker`) kann intern abonnieren
  (`SubscribeTopic`); weil die Geräte mit `retain` senden, liegt der letzte
  Wert jedes Themas im Broker.
- Netz: Geräte haben über WLAN oder Kabel ein `segment_id`; `esppdhSelect`
  liefert `NetSubnet`. Das Netz ist bei Kabel freiwillig, `net_segments.subnet`
  darf leer sein.
- **Heartbeat gibt es schon:** Zuordnungen können Heartbeat-Zuordnungen sein
  (`import_mappings.heartbeat_seconds`, Migration 162); „still“ heißt: letzter
  Wert älter als 3 × Intervall (`heartbeatSilentSQL`). Der TIA-Baustein sendet
  als Heartbeat `{"ts": …, "count": <UDInt, zählt hoch>}` (Vorlage
  `internal/modules/tiamqtt/templates/common.tmpl`) und legt die
  Heartbeat-Zuordnung beim Herunterladen an. Angezeigt wird heute nur „still
  seit …“ in der Zuordnungsübersicht, nicht der Zähler.
- Anlagenterminal (`terminal_page.go`): Vollbild für den Wandmonitor,
  `/terminal/info` zeigt offene Störungen und Tickets der Anlage samt
  Unteranlagen; Kartenanmeldung einer Person (`terminal_card*.go`), Rechte der
  Kartenperson prüft `cardPersonCan(ctx, person, recht, "infrastructure", id)`.
- Anlagen-Schnellübersicht (`plant_watch.go`): Karten je Anlage mit Ampel.
- Anlagenseite `infra_detail.gohtml` mit Reitern `overview`, `master`,
  `history`, `docs`, `links`, `fields`, `events`, `checks`, `hmi`, `setup`,
  `easy`, `chamber`.
- Rechte: `esppdh.view`, `esppdh.edit`, `esppdh.admin` (Abteilungsebene über die
  Anlage). Am Testverbund hat `esppdh.view` nur die Rolle `admin`. Der
  Instandhalter ist die Rolle `technician` („Techniker“).
- Sicherheit: Das Anmelde-Cookie `pdh_token` ist im Browser lesbar (nicht
  `HttpOnly`, `handler.go`), die Oberfläche liest es an mehreren Stellen
  (`base.gohtml`). Fremdes Programm unter der PDH-Adresse könnte die Anmeldung
  abgreifen – deshalb keine Durchreiche (siehe Ansätze).

## Ziele

Teil 1:

1. **Webserver Version 3 auf jedem Gerät** als Vorgabe (je Gerät
   abschaltbar), mit Kennwort je Gerät, ohne Firmware-Upload und ohne Schalten.
2. **Das Gerät meldet seine Adresse** per MQTT; die Geräteseite und der Reiter
   zeigen einen **Direktlink** zur Webseite des Geräts.
3. **Reiter „ESPPDH“ an der Anlage**: je Gerät dieser Anlage Zustand,
   Handshake, letzte Werte, Links zur Geräteseite und direkt zum Gerät.
4. **Anlagenterminal:** Die per Karte angemeldete Person mit dem Recht sieht
   die Geräte und Sensoren dieser Anlage mit letzten Werten und Handshake.
5. **Handshake überall lesbar:** Jede Quelle mit Lebenszeichen zeigt ihren
   Zähler, der sichtbar hochläuft – ESPPDH-Geräte (Laufzeit-Zähler) und alle
   Heartbeat-Zuordnungen (TIA-Baustein und andere). Sichtbar im Reiter
   „ESPPDH“, am Terminal und auf der Karte der Anlagen-Schnellübersicht.

Teil 2:

6. **Alle lesenden Sensoren von ESPHome** (`sensor`, `binary_sensor`,
   `text_sensor`) aus der aktiven ESPHome-Version, **nach Gruppen** sortiert,
   mit Suchfeld; die gängigen mit Feinschliff.

## Nicht-Ziele

- Keine Ausgänge, Schalter, Lichter, Motoren (`switch`, `output`, `light`,
  `number`, `button` …) – das PDH schaltet nicht an Anlagen (Entwurf Paket 1).
- **Keine Durchreiche** der Geräte-Webseite durch das PDH (Entscheidung des
  Anwenders, siehe Ansätze).
- Kein Verlauf und keine Diagramme; Werte dauerhaft speichern ist Paket 2.
- Kein OTA und keine Flotten-Updates (Paket 3 von ESPPDH). Bestehende Geräte
  bekommen Webserver, Adresse und Handshake erst mit dem nächsten Bau und
  Flashen.
- Keine Übersetzung aller ESPHome-Hilfetexte.
- Kein freier YAML-Editor.

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- Über das Sensorformular lässt sich beim Bauen fremder Code auf dem Server
  ausführen (`lambda`, `external_components`, `packages`, `includes`,
  `!include`, eigene Quelltexte).
- Das Webserver-Kennwort steht lesbar in Datenbank, Sicherung, Protokoll oder
  erzeugter Konfiguration.
- Über die Webseite des Geräts lässt sich Firmware aufspielen oder etwas
  schalten.
- Ein Link zeigt auf eine Adresse, die das Gerät nur behauptet (fremdes Netz,
  Server selbst).
- Der Handshake sieht „läuft“ aus, obwohl die Quelle still ist (z. B. weil nur
  der zuletzt gehaltene Wert angezeigt wird).
- Das Terminal zeigt Werte, ohne dass die Person dafür angemeldet und
  berechtigt ist.
- Der Katalog passt nicht zur ESPHome-Version, mit der gebaut wird, und der
  Bau scheitert an Feldern, die das Formular angeboten hat.
- Das PDH braucht ESPHome, Internet oder das Geräte-VLAN, um zu laufen.

## Ansätze (vom Anwender gewählt am 21.11.2026)

### Woher die vielen Sensoren kommen – gewählt: C

- **A – automatisch aus ESPHome:** vollständig, wächst mit ESPHome; weniger
  geführt.
- **B – von Hand gepflegt:** deutsche Hilfe, Pin-Prüfung, Skizze; nur 20–30
  Sensoren.
- **C – beides:** A für alle, die gängigen zusätzlich mit B. **Gewählt.**

### Wie man zur Webseite des Geräts kommt – gewählt: A (nach Prüfung)

Zuerst hatte der Anwender „Durchreiche über das PDH als Standard, Direktlink
zusätzlich“ gewählt. Die Prüfung des Entwurfs ergab: Unter der PDH-Adresse
liefe das Programm der Geräte-Webseite mit den Rechten des PDH im Browser und
könnte die Anmeldung jedes Betrachters abgreifen; ein eigener Port hilft
nicht (Cookies gelten je Rechnername). Neu zur Wahl gestellt:

- **A – keine Durchreiche:** Das PDH zeigt die Werte selbst (Reiter,
  Terminal, Handshake); Direktlink für Leute im Netz der Geräte. **Gewählt.**
- **B – Durchreiche abgeschottet** (Sandbox, kurz gültiger Link): viel Aufwand,
  Live-Anzeige fraglich.
- **C – eigener Rechnername für Geräte** mit Zertifikat: sicher, aber Wirkung
  nach außen (DNS, nginx, Zertifikat auf beiden Servern).

## Aufbau Teil 1 (0.82.11)

### 1. Firmware: Webserver, Adresse, Handshake, Kennwort (`yaml.go`)

- Neues Feld am Gerät „Webseite des Geräts“ (Vorgabe ein; Migration für
  bestehende Geräte: ein). Ist es ein, bekommt die Konfiguration
  `web_server: { port: 80, version: 3, auth: { username: pdh, password: !secret web_password } }`.
  Kein `ota` mit `platform: web_server` – der Webserver nimmt keine Firmware
  an (mit der aktiven ESPHome-Version prüfen). Da das Gerät nur lesende
  Bausteine hat, gibt es auf der Seite nichts zu schalten.
- **Ohne Internet am Browser:** `local: true` (Seite eingebettet), wo der
  Speicher reicht. *Eigene Entscheidung:* ESP32-Familien immer; ESP8266, wenn
  der Bau passt, sonst ohne `local` mit Hinweis an der Geräteseite („Webseite
  des Geräts braucht Internet am Browser“). Der Bauer prüft die Größe je
  Familie.
- **Adresse:** `text_sensor: wifi_info` bzw. `ethernet_info` mit `ip_address`
  auf `esppdh/<kennung>/info/adresse`, `retain: true`.
- **Handshake:** `sensor: uptime` (Sekunden seit Start, zählt hoch) auf
  `esppdh/<kennung>/info/laufzeit`, Aktualisierung alle 10 s, **ohne**
  `retain` – ein still gewordenes Gerät hinterlässt so keinen scheinbar
  frischen Wert.
- **Kennwort je Gerät:** neues Geheimnis `web_password`, verschlüsselt wie
  MQTT- und Notzugangs-Kennwort, beim Anlegen erzeugt; bestehende Geräte
  bekommen es beim nächsten Speichern oder Bauen; neu setzen wie beim
  MQTT-Kennwort.
- Geräteseite: Ist die letzte Firmware ohne diese Teile gebaut, steht dort
  „Neu bauen und flashen, damit Webseite, Adresse und Handshake ankommen.“

### 2. Werte im PDH (Speicher, kein neues Ziel)

- Ein Dienst abonniert am eingebauten Broker je ESPPDH-Verbindung `esppdh/#`
  (`SubscribeTopic`) und hält im Speicher je Thema den letzten Wert mit
  Empfangszeit. Nach Neustart oder Umschaltung kommen die gehaltenen Werte
  durch `retain` wieder; der Handshake (ohne `retain`) erst mit der nächsten
  Meldung. *Eigene Entscheidung:* keine neue Tabelle (Paket 2 speichert
  dauerhaft).
- Läuft der Dienst auf dem Spiegel oder ist der Broker aus, zeigen Reiter und
  Terminal „Werte gerade nicht verfügbar“ statt veralteter Zahlen.
- Thema → Sensor: Der Erzeuger kennt je Baustein Themen, Einheiten, Gruppe und
  Anzeigenamen; Reiter und Terminal lesen daraus, nicht aus den Themen.

### 3. Direktlink zum Gerät

- Link `http://<Adresse>/` (neuer Tab) nur, wenn die gemeldete Adresse
  gleich der **Absenderadresse der MQTT-Verbindung** des Geräts ist (der
  Broker kennt sie) und keine Loopback-, Link-Local-, Multicast- oder Adresse
  der Server bzw. der gemeinsamen Adresse ist. Ist zusätzlich ein Netzbereich
  bekannt, muss sie darin liegen. Passt es nicht (z. B. NAT dazwischen):
  „Adresse nicht bestätigt“, kein Link.
- Hinweis am Link: „nur im Netz der Geräte erreichbar“.
- „Kennwort anzeigen“ an der Geräteseite, nur mit `esppdh.edit` für das Gerät;
  jedes Anzeigen wird protokolliert (wer, wann, welches Gerät).

### 4. Reiter „ESPPDH“ an der Anlage

- Neuer Reiter `tab=esppdh` in `infra_detail.gohtml`; sichtbar, wenn an der
  Anlage mindestens ein ESPPDH-Gerät hängt und die Person das neue Recht
  `esppdh.values` oder `esppdh.view` für diese Anlage hat.
- Je Gerät eine Karte: Name, online/offline, Handshake, Firmware-Stand, Link
  zur Geräteseite `/import/esppdh/{id}` (nur mit `esppdh.view`), Direktlink;
  darunter die Sensoren nach Gruppen mit letztem Wert, Einheit und „vor …“.
  Werte älter als 3 × Aktualisierungsintervall grau.
- Aktualisiert sich alle 10 s per htmx (kein Ereignisstrom je Browser).
- *Eigene Entscheidung:* nur Geräte genau dieser Anlage, nicht der
  Unteranlagen.

### 5. Anlagenterminal

- `/terminal/info` bekommt einen Bereich bzw. eine Kachel „Sensoren“, sichtbar
  nur, wenn eine Person per Karte angemeldet ist und
  `cardPersonCan(…, "esppdh.values", "infrastructure", <Anlage>)` gilt.
  Ohne Kartenanmeldung bleibt das Terminal wie bisher.
- Inhalt wie der Reiter, für Berührung und Abstand lesbar (große Schrift, ohne
  Links ins PDH), Geräte der Anlage **samt Unteranlagen** wie die übrigen
  Terminal-Listen.
- Endet die Kartensitzung, verschwindet der Bereich.

### 6. Handshake überall lesbar

- **Anzeige** (ein gemeinsamer Baustein für alle Stellen): Zählerstand, „läuft“
  (grün) solange der Zähler innerhalb von 3 × Intervall weitergezählt hat,
  sonst „steht seit …“ (rot); dazu ein dezenter Puls bei jedem neuen Wert, damit
  man das Weiterzählen sieht. Ein gehaltener, aber nicht mehr steigender Wert
  gilt als stehend.
- **Quellen:**
  - ESPPDH-Geräte: `info/laufzeit` (Intervall 10 s).
  - Heartbeat-Zuordnungen (`import_mappings.heartbeat_seconds`): der Zähler aus
    der Nachricht – beim TIA-Baustein das Feld `count`; bei anderen Quellen der
    Wert der Zuordnung, wenn er eine Zahl ist, sonst nur „läuft/steht“ nach
    Empfangszeit. *Für den Bauer:* prüfen, ob die vorhandene
    TIA-Heartbeat-Zuordnung die ganze Nachricht oder schon `#count` speichert;
    ggf. die Zuordnung beim nächsten Herunterladen auf `…/heartbeat#count`
    umstellen, bestehende per Migration nachziehen.
- **Orte:** Reiter „ESPPDH“ (je Gerät), Anlagenterminal (je Gerät und je
  Heartbeat-Zuordnung der Anlage), Karte der Anlagen-Schnellübersicht (eine
  Zeile „Handshake“ je Quelle der Anlage samt Unteranlagen; die Karte färbt
  sich gelb, wenn ein Handshake steht). „Anlagenstand“ meint die
  Anlagen-Schnellübersicht (vom Anwender bestätigt am 21.11.2026).

### Rechte Teil 1

- **Neues Recht `esppdh.values`** – „ESPPDH: Sensorwerte ansehen (Anlage und
  Terminal)“, Kategorie „Daten & Schnittstellen“, Abteilungsebene über die
  Anlage. *Eigene Entscheidung:* per Migration an `admin`, `manager`,
  `asset_manager`, `technician` (nicht `worker`, nicht `viewer`); der
  Anwender kann es unter „Rollen & Rechte“ anpassen. Niemand verliert etwas,
  weil es neu ist.
- `esppdh.view` reicht ebenfalls für den Reiter (wer Geräte sehen darf, darf
  auch ihre Werte sehen).
- „Kennwort anzeigen“: `esppdh.edit` für das Gerät.
- Handshake auf der Schnellübersicht: die Rechte der Schnellübersicht; Werte
  von ESPPDH-Geräten dort nur mit `esppdh.values`/`esppdh.view`, sonst nur
  „läuft/steht“.
- Neue Routen in `gateRules`; Handbuch „Administration“ und Änderungsprotokoll
  (`### Sicherheit`: Webserver-Kennwort, Kennwort anzeigen, neues Recht).

### Handbuch Teil 1

Kapitel ESPPDH (Webseite des Geräts, Kennwort, Direktlink, Handshake),
Anlagenseite (Reiter), Anlagenterminal (Sensoren nach Kartenanmeldung),
Anlagen-Schnellübersicht (Handshake), Zuordnungen (Heartbeat-Zähler); Skizzen
in `help_figures.gohtml` anpassen.

## Aufbau Teil 2 (0.82.12) – Sensorkatalog (Ansatz C)

**Katalog erzeugen.**

- Python-Programm im Repository (`scripts/esphome-catalog.py`), läuft mit dem
  Python **der jeweiligen ESPHome-Umgebung** und schreibt einen Katalog als
  JSON: je Plattform (`sensor`, `binary_sensor`, `text_sensor`) Name,
  Kurzbeschreibung, benötigte Grundbausteine (Bus oder Hub, s. u.), Felder mit
  Typ (Pin, ganze Zahl, Zahl, Auswahl, Schalter, Dauer, Text, Verweis auf
  Bus/Hub/anderen Sensor), Pflicht/Vorgabe, gelieferte Werte mit Einheit.
- Erzeugt, wenn eine ESPHome-Version eingerichtet oder aktiv wird
  (Update-Agent bzw. Zeiger `active`), abgelegt je Version; fehlt er, erzeugt
  das PDH ihn beim ersten Öffnen der Auswahl.
- Nicht in den Katalog: `lambda`, Vorlagen-Ausdrücke, `on_…`-Auslöser,
  `external_components`, `includes`, Dateipfade. Plattformen, die ohne solche
  Felder nicht sinnvoll gehen (z. B. `template`), fehlen.
- Gruppen: Tabelle Plattform → Gruppe im Repository
  (`internal/modules/esppdh/catalog_groups.go`) anhand der
  ESPHome-Dokumentation: Umwelt, Luftqualität, Strom & Energie, Abstand &
  Füllstand, Licht, Gewicht & Kraft, Thermoelemente, Bewegung & Lage, Analog &
  Digital, Sonstiges (unbekannte neue Plattformen).
- Test: Katalog der Grundversion (`scripts/esphome.version`) unter `testdata/`
  gegen Erzeuger und Formular.

**Grundbausteine am Gerät.** *Eigene Entscheidung:* Busse **und** Hubs sind
eigene Einträge am Gerät, die Sensoren auswählen: I²C, SPI, UART, 1-Wire,
ADS1115, Modbus-Steuerung, Zeitquelle (SNTP, für Tageszähler), BLE-Empfänger.
Sensoren, die auf einen anderen Sensor verweisen (`ct_clamp`, `integration`,
`total_daily_energy`, `copy`), wählen ihn aus einer Liste der Sensoren
desselben Geräts. **Filter** nur aus einer festen Liste ohne Code: Versatz,
Faktor, lineare Kalibrierung, gleitender Mittelwert, Drosseln (`throttle`),
nur bei Änderung (`delta`), Begrenzen (`clamp`).

**Formular.** Auswahl mit Gruppen als Abschnitte, Suchfeld, „geführt“-Kennzeichen
oben; Pflichtfelder zuerst, übrige unter „Weitere Einstellungen“; Pins gegen
die Platine geprüft, soweit als Pin bekannt; Themen
`esppdh/<kennung>/<plattform>-<nr>/<wert>` (feste Nummer je Art). Vor dem
Speichern `esphome config` der aktiven Version, wenn vorhanden; Fehler in
Klartext am Formular.

**Feinschliff** (deutsche Feldnamen, Hilfe, Vorgaben, Pin-Prüfung,
Anschluss-Skizze):

| Gruppe | Sensoren |
|---|---|
| Umwelt | DHT22/DHT11, DS18B20 (1-Wire), BME280/BMP280, SHT3x, AHT10/AHT20 |
| Luftqualität | SCD4x (CO₂), PMS5003 (Feinstaub), SGP30/SGP4x |
| Licht | BH1750 |
| Abstand & Füllstand | HC-SR04 (Ultraschall), VL53L0X |
| Gewicht & Kraft | HX711 |
| Strom & Energie | INA219, INA226, ADE7953, BL0942, HLW8012, Modbus-Zähler (SDM), dazu S0, SCT-013, PZEM-004T |
| Thermoelemente | MAX6675, MAX31855, MLX90614 (berührungslos) |
| Analog & Digital | ADC (Analog-Pin), ADS1115, GPIO-Eingang (binary_sensor), Stückzähler |

Die heutigen vier Bausteine bleiben unverändert (Themen, Nummern, Felder) und
erscheinen in ihrer Gruppe. Rechte: wie heute (`esppdh.edit`).

## Teil 3 (0.82.13) – Plausibilität (nur erfasst)

Wunsch des Anwenders: je Sensor Prüfungen „zu lange keine Änderung“,
„Änderung im Zeitraum nicht plausibel“, „Minimal- und Maximalzeit“,
Wertebereiche (z. B. Temperatur), aktiv nur nach Betriebszeiten der Anlage
(Arbeitstage, Pausen), ruhend bei Wartung und Störung. Vorschlag bisher:
melden wie die Energie-Schwellwerte (0.80.15: Ticket mit Ursache-Pflicht,
Rückschaltzeit, keine Ticketflut). Offen: Woher die Betriebszeiten der Anlage
kommen (Schichtmodell an der Anlage / aus dem Personal-Schichtplan / eigene
Zeiten je Anlage) und was „Minimal- und Maximalzeit“ genau heißt. Wird vor dem
Bau von Teil 3 eigens besprochen und hier ergänzt.

## Prüfen

- Automatisch: Erwartungsdateien des Erzeugers (mit/ohne Webserver,
  `wifi_info`/`ethernet_info`, `uptime`, Busse und Hubs, je eine
  Beispielkonfiguration der gängigen Sensoren); Katalog der Grundversion;
  Adressbestätigung (Absender gleich/ungleich, Loopback, Server, gemeinsame
  Adresse, fremdes Netz); Handshake (läuft, steht, gehaltener Wert gilt als
  stehend); Reiter und Terminal (nur eigene Anlage, ohne Recht unsichtbar,
  ohne Kartenanmeldung unsichtbar, Abteilung); neues Recht per Migration bei
  den vier Rollen.
- `esphome config` gegen alle Beispielkonfigurationen, wenn die Umgebung da
  ist.
- Testverbund: Reiter, Terminal (mit und ohne Karte), Schnellübersicht in
  Fiori/Fluent, hell/dunkel, 320–1366 px; Umschaltung → Werte und Handshake am
  zweiten Server wieder da.
- Echte Platine (Anwender): Webseite direkt, Handshake läuft und steht nach
  Stromtrennung, ein Sensor über I²C und einer über 1-Wire (Teil 2).

## Festlegungen des Anwenders

1. Sensoren: Ansatz C (21.11.2026).
2. Webseite des Geräts: Ansatz A – keine Durchreiche, Werte im PDH,
   Direktlink (21.11.2026, nach dem Befund zur Anmeldung).
3. Sensorwerte am Terminal für Instandhalter, Handshake überall,
   Plausibilität als eigener Teil (21.11.2026).
4. Handshake auf der Anlagen-Schnellübersicht („Anlagenstand“), Teil 1 und 2
   freigegeben (21.11.2026).

## Eigene Entscheidungen (zum Gegenlesen)

- Nur lesende Arten, keine Ausgänge.
- Webserver als Vorgabe ein, je Gerät abschaltbar; `local: true`, wo der
  Speicher reicht.
- Handshake der Geräte über `uptime` ohne `retain`.
- Werte nur im Speicher, keine neue Tabelle.
- Direktlink nur bei Übereinstimmung mit der MQTT-Absenderadresse.
- Neues Recht `esppdh.values` für `admin`, `manager`, `asset_manager`,
  `technician`.
- Reiter ohne Unteranlagen, Terminal mit Unteranlagen (wie die übrigen
  Terminal-Listen).
- Busse und Hubs als Grundbausteine; Verweise auf Sensoren desselben Geräts;
  feste Filterliste.

## Offen

- Teil 3 vollständig (siehe oben).
- Vorlagen für Messaufgaben sind ein eigenes Projekt (`AGENTS.md`, Punkt 17).

## Offen für den Bauer

- Technik des Katalog-Auslesens aus ESPHome (Schemas der Komponenten).
- Absenderadresse der MQTT-Verbindung im Broker abfragen.
- Form der TIA-Heartbeat-Zuordnung (siehe §6).
- Aufteilung in Dateien und Tests.
