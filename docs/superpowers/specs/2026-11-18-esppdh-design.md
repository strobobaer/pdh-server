# ESPPDH – eigene Mess- und Zählgeräte auf ESP-Basis

Stand: 18.11.2026 · Entwurf von Claude, **vom Anwender noch nicht bestätigt**.
Grundlage: die am 15.11.2026 freigegebene Beschreibung (Seite Import → ESPPDH,
ESPHome als Compiler, Bausteine Produktionsdaten und Stromaufnahme, MQTT-Zugang
je Gerät, USB-Flash im Browser, OTA, Überwachung, Rechte `esppdh.*`, drei Pakete).

## Warum

Viele Anlagen liefern keine Daten: keine Steuerung mit Netz, kein Zähler mit
Schnittstelle. Ein ESP32 für wenige Euro mit einem Sensor (Lichtschranke,
Stromzange, S0-Zähler) schließt die Lücke. Bisher muss dafür jemand ESPHome
installieren, YAML schreiben, MQTT-Zugangsdaten eintragen und im PDH die
Zuordnungen von Hand anlegen. ESPPDH macht daraus einen Ablauf im PDH:
Gerät anlegen → Bausteine wählen → flashen → Daten sind da.

## Was es im PDH schon gibt (Bestand, geprüft am 18.11.2026)

- **Eingebauter MQTT-Broker** mit eigenem Namen, TLS-Port und Prüfliste
  (0.80.20–0.80.25). Er kennt **genau einen Zugang je Verbindung** und **keine
  Themen-Rechte** (`credentialAuthHook`, `OnACLCheck` liefert immer „ja“).
- **Zuordnungen** (`import_mappings`): Thema bzw. `thema#jsonpfad` → Anlage.
  Gespeichert wird nur der letzte Wert. Per Code anlegbar (Muster TIA-Baustein).
- **Energie-Messstellen** (`energy_meters`) hängen an einer Zuordnung und
  schreiben Minutenwerte; Schwellwerte und Alarm-Tickets gibt es (0.80.15).
- **Produktionsdaten** (Stückzähler, Taktzeit, Zustand, Betriebsstunden) haben
  **keine eigenen Tabellen**. Es gibt nur Rüstzeiten (0.80.3).
- **Ausfall-Wächter mit Alarm-Ticket** gibt es zweimal (Netz-Abgleich, Energie),
  jeweils mit Karenz und Verantwortlichen – als Muster, nicht als gemeinsames Teil.
- **Update-Agent**: richtet am Server ein, was das PDH selbst nicht darf
  (nginx, Firewall, Zertifikate). `install_update.sh` installiert Zusatzsoftware
  nach dem Muster „nur wenn eingerichtet, Fehler ist nur eine Warnung“.
- **Spiegelserver**: `uploads/` wird gespiegelt; Hintergrunddienste laufen nur
  am aktiven Server.

## Ziele

1. **Geräte verwalten:** Seite **Import → ESPPDH** mit Liste und Detailseite je
   Gerät (Name, Anlage, Platine, WLAN, Bausteine, Zustand, Firmware-Stand).
2. **Bausteine statt YAML:** Der Anwender wählt Bausteine und trägt nur ein,
   was er wissen kann (Anschluss/Pin, Zangentyp, Impulse je kWh). Das PDH
   erzeugt daraus die ESPHome-Konfiguration.
   - Produktionsdaten: Stückzähler, Taktzeit, Zustand (läuft/steht),
     Betriebsstunden.
   - Stromaufnahme: SCT-013 (Stromzange), PZEM-004T, ADE7953/BL0942,
     S0-Impulsausgang, Modbus RTU (Zähler mit RS485).
   - Freier Editor für alles, was die Bausteine nicht abdecken.
3. **Kompilieren am Server:** ESPHome liegt fest in der Installation (eigene
   Python-Umgebung, vom Update mit aktuell gehalten). Ein Knopf baut die
   Firmware; der Verlauf ist im Browser sichtbar.
4. **Flashen ohne Zusatzprogramm:** erstes Flashen per USB aus dem Browser,
   danach Updates über das Netz (OTA).
5. **MQTT-Zugang je Gerät:** eigenes Kennwort je Gerät, und das Gerät darf nur
   unter seinem eigenen Thema senden. Ein verlorenes Gerät wird einzeln gesperrt.
6. **Zuordnungen automatisch:** Aus den Bausteinen entstehen die Zuordnungen
   und – bei Stromaufnahme – die Energie-Messstellen von selbst.
7. **Überwachung:** online/offline je Gerät, Störmeldung nach Karenz wie beim
   Netz-Abgleich.

## Nicht-Ziele

- Kein Ersatz für Home Assistant, keine ESPHome-eigene API (nur MQTT).
- Keine fremden Firmwares (Tasmota, eigene Arduino-Programme).
- Kein Schalten von Ausgängen an Anlagen aus dem PDH (nur lesen/messen).
- Keine geeichte Messung – ESPPDH misst für die Betriebsführung, nicht für
  die Abrechnung.
- Kein Schaltplan-Zeichner; das Handbuch zeigt je Baustein eine Anschluss-Skizze.

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- Das PDH braucht ESPHome oder Internet, um zu laufen. (ESPPDH ist Zusatz:
  ohne die Python-Umgebung zeigt die Seite einen Hinweis, alles andere läuft.)
- Ein Kompilierlauf bremst das PDH für die Benutzer oder füllt die Platte.
- WLAN- oder MQTT-Kennwörter stehen lesbar in Datenbank, Sicherung, Protokoll
  oder in einer heruntergeladenen Datei, die nicht ausdrücklich dafür da ist.
- Ein Gerät kann unter fremden Themen senden und so Werte anderer Anlagen fälschen.
- Nach einer Umschaltung auf den zweiten Server sind Geräte „offline“ und es
  hagelt Tickets.
- Ein fehlerhaftes OTA-Update macht ein verbautes Gerät unbrauchbar.

## Aufbau

### Compiler (ESPHome)

- Eigene Python-Umgebung unter `/opt/pdh-esphome` (venv), angelegt und
  aktualisiert von `install_update.sh` über den Update-Agenten – nur wenn
  ESPPDH in den Server-Einstellungen eingeschaltet ist. Fehler dabei sind eine
  Warnung, kein Abbruch des Updates.
- Die ESPHome-Version ist **im Repository festgelegt** (eine Zeile, wird mit
  PDH-Versionen angehoben). So bauen beide Server und alle Betriebe dasselbe;
  eine neue ESPHome-Version kommt geprüft mit einem PDH-Update.
- Erster Kompilierlauf je Platinentyp lädt Werkzeuge aus dem Internet
  (PlatformIO, mehrere hundert MB). Die Seite sagt das vorher.
- Es läuft **immer nur ein Kompilierlauf**, mit niedriger Priorität und
  Zeitlimit; weitere warten in einer Schlange. Platzbedarf wird vorher geprüft.
- Kompiliert wird nur am **aktiven** Server. Fertige Firmware liegt unter
  `uploads/esppdh/` und wird gespiegelt – nach einer Umschaltung ist sie da,
  nur die Werkzeuge lädt der zweite Server beim ersten Lauf selbst nach.
- **Docker-Betrieb:** Das Programm-Abbild enthält kein Python. Vorschlag:
  ESPPDH im Docker-Betrieb zunächst nicht anbieten (Hinweis auf der Seite);
  produktiv läuft systemd.

### Gerät und Konfiguration

- Tabellen `esppdh_devices` (Name, Anlage, Platine, WLAN, Zustand, Firmware,
  Zugangsdaten verschlüsselt) und `esppdh_blocks` (Baustein, Einstellungen).
- Die ESPHome-Konfiguration entsteht bei jedem Bau neu aus den Bausteinen plus
  einem optionalen freien Teil. Kennwörter stehen darin nur als Verweise; die
  Geheimnis-Datei schreibt das PDH für die Dauer des Baus und löscht sie danach.
- **Lizenz:** Seite und Handbuch nennen „ESPPDH baut auf ESPHome auf“
  (GPLv3 bzw. MIT). Die Konfiguration jedes Geräts ist herunterladbar (ohne
  Kennwörter), ebenso die gebaute Firmware.

### MQTT-Zugang je Gerät

- Der eingebaute Broker bekommt zusätzlich zu seinem bisherigen Zugang eine
  **Liste von Gerätezugängen** mit Themen-Rechten: Ein Gerät darf nur unter
  `esppdh/<gerät>/…` senden und nur seine eigenen Befehls-Themen lesen.
  Der bisherige Zugang bleibt unverändert (Steuerungen, TIA-Baustein).
- Geräte verbinden im Haus über den TLS-Port (mqtts) mit dem Namen des
  Brokers, wenn er eingerichtet ist; sonst unverschlüsselt über den MQTT-Port
  mit deutlichem Hinweis.
- Sperren eines Geräts wirkt sofort (Verbindung wird getrennt).

### Flashen

- **USB im Browser** (ESP Web Tools): Braucht Chrome oder Edge und eine
  **https-Adresse** des PDH – das verlangt der Browser für den Zugriff auf
  USB-Geräte. Über `http://…:8090` geht es nicht; dort bietet die Seite die
  Firmware zum Herunterladen an.
- **OTA:** Vorschlag: Das Gerät holt sich die neue Firmware selbst vom PDH ab
  (Abruf über https mit Prüfsumme), ausgelöst über MQTT. Das PDH muss dafür
  keine Verbindung zum Gerät aufbauen – es funktioniert auch nach einer
  Umschaltung und durch Firewalls. ESPHome behält bei einem fehlgeschlagenen
  Start die alte Firmware (Rückfall).

### Daten

- Stromaufnahme → Zuordnung + Energie-Messstelle (Art Zählerstand, Verbrauch
  oder Momentanwert), damit laufen Auswertung, Efficio-Export und
  Schwellwerte ohne weiteres Zutun.
- Produktionsdaten brauchen ein **neues Ziel**: Zählerstände und Zustände mit
  Verlauf je Anlage (Tabellen für Stückzahl je Zeitraum, Zustandswechsel,
  Betriebsstunden). Das ist die Grundlage für die später geplante
  Produktionsüberwachung und deshalb ein eigenes Paket.

### Überwachung

- Jedes Gerät meldet „online“ beim Verbinden; der Broker meldet „offline“,
  wenn die Verbindung abreißt (Letzter Wille). Nach der Karenz entsteht ein
  Alarm-Ticket mit Ursache-Pflicht – aufgebaut wie beim Netz-Abgleich.
- Nach Start des PDH oder einer Umschaltung gilt ein Gerät erst als offline,
  wenn es sich an **diesem** Server schon einmal gemeldet hat oder die Karenz
  seit dem Start verstrichen ist (Regel wie bei SNMP, 0.80.22).

## Rechte

Neue Kategorie-Einträge unter „Daten & Schnittstellen“:

| Recht | Darf |
|---|---|
| `esppdh.view` | Geräte und Zustand ansehen |
| `esppdh.edit` | Geräte anlegen, Bausteine ändern, kompilieren, flashen, OTA auslösen |
| `esppdh.admin` | Bereichsadministrator: alles, dazu Geräte endgültig löschen und den freien Editor nutzen |

- Vergabe per Migration an die Rollen, die heute `import.write` haben
  (`view`, `edit`) bzw. `import.admin` (`admin`), mit Abteilungsebene über die
  Anlage des Geräts (`h.hasPermRec`).
- Der **freie Editor** steht nur dem Bereichsadministrator offen: Freies YAML
  kann beim Bauen fremden Code auf dem Server ausführen (ESPHome erlaubt
  eigene Programmteile und externe Komponenten).
- ESPPDH ein-/ausschalten und die Python-Umgebung einrichten: Server-Einstellungen,
  bestehendes Recht für Server-Einstellungen.

## Pakete

**Paket 1 – Gerät bauen und flashen (nächste Korrekturversion)**
Compiler-Umgebung samt Einschalter und Zustandsanzeige; Geräteliste und
Detailseite; Bausteine Stromaufnahme (S0, SCT-013, PZEM) und Stückzähler;
Kompilieren mit Verlauf; Firmware herunterladen und USB-Flash; MQTT-Zugang je
Gerät mit Themen-Rechten; Rechte; Handbuch-Kapitel mit Anschluss-Skizzen.
Ergebnis: Ein Gerät sendet Werte an den Broker, sichtbar im Explorer.

**Paket 2 – Daten ankommen lassen**
Zuordnungen und Energie-Messstellen automatisch; neues Ziel für
Produktionsdaten (Stückzähler, Taktzeit, Zustand, Betriebsstunden) mit
Anzeige an der Anlage; übrige Bausteine (ADE7953/BL0942, Modbus RTU);
Überwachung online/offline mit Störmeldung.

**Paket 3 – Betrieb der Flotte**
OTA einzeln und für alle; Hinweis „Firmware veraltet“ nach PDH-Update mit
neuer ESPHome-Version; freier Editor; Gerät tauschen (gleiche Einstellungen
auf neue Platine).

## Prüfen

- Automatisch: Erzeuger der Konfiguration gegen feste Erwartungsdateien;
  `esphome config` (nur prüfen, ohne Bauen) in den Tests, wenn die Umgebung
  vorhanden ist; Broker-Tests für Gerätezugang und Themen-Rechte; Rechte- und
  Abteilungstests; Spiegelserver baut nicht.
- Testverbund: Umgebung per Verbund-Update einrichten, ein Gerät kompilieren,
  Umschaltung, Firmware ist am zweiten Server da.
- Echte Platine: USB-Flash und ein Sensor – das kann nur der Anwender
  (oder ein ESP32 am Windows-Rechner der Entwicklung).

## Offene Entscheidungen für den Anwender

1. **Paketschnitt** wie oben? (Vor allem: Produktionsdaten-Ziel erst in Paket 2.)
2. **Docker-Betrieb** zunächst ohne ESPPDH?
3. **OTA als Abruf durch das Gerät** statt Aufspielen vom Server aus?
4. **Freier Editor nur für den Bereichsadministrator** und erst in Paket 3?
5. **ESPHome-Version fest im Repository** (kommt mit PDH-Updates) statt
   „immer die neueste beim Update holen“?
6. **Platinen – entschieden am 18.11.2026: „verschieden“.** Es gibt keine
   feste Platine. Die Auswahl in Paket 1 nennt deshalb die gängigen Familien
   (ESP32 DevKit, ESP32-S3, ESP32-C3, ESP8266/D1 mini, ESP32 mit Ethernet wie
   WT32-ETH01 und Olimex ESP32-POE) und dazu **„Andere Platine“** mit der
   Platinen-Kennung von ESPHome als Freitext. Je Familie hinterlegt das PDH
   die für die Bausteine erlaubten Anschlüsse (z. B. welche Pins analog
   messen können) und warnt bei einer unpassenden Wahl; bei „Andere Platine“
   entfällt diese Prüfung. Ethernet-Platinen gehören damit zu Paket 1.
7. **Netz der Geräte:** eigenes WLAN/VLAN für die Geräte oder das Betriebs-WLAN?
   (Bestimmt die Vorgabe für neue Geräte und die Hinweise im Handbuch.)
