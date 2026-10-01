# Änderungsprotokoll

Alle nennenswerten Änderungen am PDH-Server. Die Versionsnummer steht in der
Datei `VERSION` und folgt [Semantic Versioning](https://semver.org/lang/de/)
(Haupt.Neben.Fehlerkorrektur; vor 1.0 steigt die Nebenversion mit jedem
Funktionspaket). Versionen bis 0.9.0 stammen aus den Commit-Nachrichten, 0.10.0
bis 0.14.0 wurden nachträglich aus der Git-Historie zusammengefasst.

Aufbau je Version: `## [x.y.z] – JJJJ-MM-TT`, darunter `### Neu`, `### Geändert`,
`### Behoben`, `### Sicherheit` mit Stichpunkten. Das Handbuch zeigt diese Datei
im Kapitel „Versionen & Updates“ an.

## [0.20.0] – 2026-10-01

### Neu
- Dashboard-Widgets: Jeder Benutzer stellt sich sein Dashboard selbst zusammen – Widgets aus dem Katalog ins Raster ziehen, verschieben, Breite ändern, einstellen; gespeichert am Konto. 15 Widgets: Schnellaktionen, Eigene Links, Kennzahlen (offene Tickets, aktive Störungen, Wartung fällig, Nachbestellen, meine Aufgaben, meine Stunden, Verlauf 14 Tage), Listen (Mir zugewiesen, aktuelle Störungen, Wartungen nächste 7 Tage, Bestellliste) sowie Meine Schichten und Notizzettel. Widgets aktualisieren sich selbst und erscheinen nur mit passender Berechtigung
- Uhr in der Kopfleiste aller Seiten mit Wochentag, Datum und sekundengenauer Uhrzeit (Kalenderwoche als Hinweis); sie läuft mit der Uhr des PDH-Servers, damit alle Geräte dieselbe Zeit zeigen, und öffnet per Klick die Zeiterfassung. Auf dem Smartphone wird nur die Uhrzeit gezeigt, auf dem Tablet Wochentag und Uhrzeit

### Geändert
- Die feste Kennzahlenzeile auf dem Dashboard ist jetzt Teil der Widgets; ohne eigene Zusammenstellung zeigt das Dashboard dieselben Kennzahlen plus Schnellaktionen und „Mir zugewiesen“

## [0.19.0] – 2026-10-01

### Neu
- Terminal-Standort: Systembenutzern (Terminals) lässt sich in den Stammdaten ein Standort im Infrastruktur-Baum geben; beim Anlegen von Tickets, Störungen & Co. an diesem Terminal klappt die Infrastruktur-Auswahl bis dorthin auf und hebt ihn hervor – auch bei kurzer Anmeldung mit eigenem Konto
- Die Infrastruktur-Auswahl klappt beim Bearbeiten bis zur bereits gewählten Anlage auf und markiert die Auswahl

- Theme-Galerie: 57 bekannte Home-Assistant-Themes (Nordic, Caule Themes Pack, iOS Themes, Metro & Fluent) lassen sich unter „Erscheinungsbild“ mit einem Klick übernehmen oder direkt als Firmenstandard setzen – mit Vorschau für Hell und Dunkel
- Schriftart und Größe: Der Administrator legt Schrift (z. B. Inter, Roboto, Atkinson Hyperlegible) und Größe (70–160 %) als Firmenstandard fest
- Jeder Benutzer stellt im Benutzermenü unter „Darstellung“ Farbschema, Schriftart und Größe (−/+) ein; mit „Speichern“ gilt das an seinem Konto auf allen Geräten

- Bedienung per Smartphone und Tablet: Navigation als ausfahrbares Menü (☰), Chat/Suche/Hilfe als Leiste von rechts, kompakte Kopfzeile, einspaltige Formulare, seitlich wischbare Tabellen und größere Tippflächen
- Lange drücken ersetzt auf Touch-Geräten den Rechtsklick (Kontextmenüs, auch auf iPhone/iPad)
- Zeitbalken in der Zeiterfassung lassen sich auch mit Finger oder Stift verschieben
- PDH lässt sich wie eine App auf den Startbildschirm von Smartphone oder Tablet legen

### Geändert
- Auf Smartphone und Tablet öffnet sich die Chat-Leiste bei neuen Nachrichten nicht mehr von selbst (nur der Zähler)
- Das eigene Farbschema wird jetzt am Benutzerkonto statt nur im Browser gespeichert; eine bisher nur im Browser gewählte Farbe muss einmal neu gewählt und gespeichert werden
- Home-Assistant-Import: Dateien mit mehreren YAML-Dokumenten (---) werden vollständig gelesen, unsichtbare Rahmenfarben (transparent) durch passende ersetzt

### Behoben
- Die „…“-Menüs und das Benutzermenü hatten durch einen Fehler im Stylesheet keine Abstände; die Einträge standen teils nebeneinander

### Sicherheit
- Neue Berechtigung „Infrastruktur anlegen, bearbeiten & deaktivieren“: Den Anlagenbaum ändern dürfen nur noch Rollen mit dieser Berechtigung (anfangs Administratoren und Manager); bisher durfte das jeder angemeldete Benutzer

## [0.18.0] – 2026-10-01

### Neu
- Farbschemata: Unter „Server-Einstellungen → Erscheinungsbild“ legt der Administrator die Farben der Oberfläche für alle fest – sieben Vorlagen (PDH-Blau, Petrol, Industriegrün, Signalorange, Bordeaux, Violett, Anthrazit) oder eine eigene Firmenfarbe, die für Hell und Dunkel automatisch lesbar angepasst wird
- Themes aus Home Assistant übernehmen (z. B. Nordic): Theme-Datei hochladen oder einfügen – Akzent-, Hintergrund-, Karten-, Text- und Rahmenfarben werden übernommen, getrennt für Hell und Dunkel
- Jeder Benutzer kann im Benutzermenü (oben rechts auf den Namen) ein eigenes Farbschema wählen; der Administrator kann das abschalten

### Geändert
- Die Anmeldeseite zeigt das gewählte Farbschema

## [0.17.0] – 2026-09-30

### Neu
- Unter „Mein Konto“ lässt sich zusätzlich zum Geschäftskonto ein privates Microsoft-Konto (Outlook.com, Hotmail, Microsoft 365 Single/Family) verbinden – mit eigener Kalenderauswahl, Synchronisierung und Busy-Blockern im Schichtplan

### Geändert
- Kalenderauswahl, Synchronisierung und Trennen gelten jetzt getrennt je Microsoft-Konto; bestehende Verknüpfungen werden als Geschäftskonto übernommen

## [0.16.0] – 2026-09-30

### Neu
- Eigenes PDH-Symbol (Favicon) im Browser-Tab, in Lesezeichen und beim Ablegen auf dem Startbildschirm von Handy und Tablet; ein hochgeladenes App-Logo hat weiterhin Vorrang

## [0.15.0] – 2026-09-30

### Neu
- Handbuch in der Hilfe (bebildert, Kontexthilfe je Seite, eigene Bildschirmfotos)
- Datensicherung & Wiederherstellung mit Auswahl der Bestandteile, Zeitplänen, Upload/Download und automatischer Sicherheitskopie vor jeder Wiederherstellung
- Automatische Vollsicherung vor jedem Update (vor den Datenbank-Änderungen)
- Löschvormerkung für Stammdaten: Sperren, Deaktivieren, Zum Löschen vormerken, Bereinigungslauf mit Chat-Bericht an alle Administratoren
- Systembenutzer „PDH-System“ mit Chat-Hinweisen bei Änderungen an Tickets, Störungen, Aufgaben, Projekten und Wartungen
- Kategorien (#Energie, #Optimierung, #Zufriedenheit, #Versuch, #Produktiv, #Efficio) für alle Module mit Listenfilter und Übersichtsseite
- Einrichtungsassistent für die Erstinstallation (Datenbank prüfen oder automatisch anlegen, ersten Administrator anlegen)
- Server-Einstellungen in der Oberfläche; Werte liegen in der Datenbank (Geheimnisse verschlüsselt), bestehende `.env` wird automatisch übernommen
- Ausführliches Server-Protokoll mit Filtern, Live-Ansicht und Download
- Erscheinungsbild: Anzeigename und Logos für App, Exporte (PDF/Excel) und Druck
- Versionierung: Versionsnummer in der Kopfzeile, Änderungsprotokoll im Handbuch (Kapitel „Versionen & Updates“), bei der Update-Prüfung Anzeige der neuen Version mit allen Änderungen; Versionsnummer auch in Sicherungen, Server-Protokoll und `/health`

### Geändert
- Buchungen auf gesperrte Ersatzteile und Lagerplätze werden von der Datenbank abgewiesen

### Sicherheit
- `POST /api/v1/users/register` ist nicht mehr öffentlich; die Admin-Rolle vergeben nur Administratoren

## [0.14.0] – 2026-09-29

### Neu
- Infrastrukturbezogene Historie und Kommentarverwaltung mit Filter und Suche
- Hersteller & Lieferanten: Stammdaten, Support-Hotline, Portale mit Einbettung, verschlüsselte Shop-Zugangsdaten, täglicher Bestellvorschlag
- Interner Chat im Teams-Stil (Seitenleiste, Teams und Kanäle)
- Reiter, Feldsätze und Listen für alle Module; Bildsuche im Internet für Ersatzteile; Etikettendruck mit QR-Code
- Broker-Verteilung nicht zugewiesener Meldungen aus dem Leitstand
- Benutzerstamm mit Reitern, privaten Daten, Nachweisen und Einzelrechten; Rücksprung zur vorherigen Seite

## [0.13.0] – 2026-09-28

### Neu
- Docker-Installation, Update-Prüfung und Update-Agent, Core-Einstellungen
- Leitstand (globales Dashboard) mit Zeitstrahl
- Microsoft-365-Anbindung, Benutzerverwaltung mit Organigramm
- Import/Export: MQTT (Broker, Sniffer, Zuordnung), OPC UA, Modbus, SQLite, MySQL, MS SQL, Excel, CSV, REST/Web
- Standard-Fälligkeiten für Tickets und Aufgaben

## [0.12.0] – 2026-07-31

### Neu
- Lagerort-Baum mit Bestand je Lagerort und Buchungs-Popup; Kostenstellen für Infrastruktur
- Wartungsausführung mit Checkliste, Maßnahmen, Ersatzteilen und Folgeaufträgen
- Aufgaben- und Projektmodul, Synchronisation Störung ↔ Ticket ↔ Aufgabe, Gantt-Zeitstrahl
- Zeiterfassung mit eigener Zeitachse, Schichtplanung per Klick
- Rollen- und Berechtigungsverwaltung, Auto-Logout, Systemnutzer-Override, Anmeldung per Benutzername oder RFID

## [0.11.0] – 2026-06-01

### Neu
- Mehrere Feldsätze und Auswahllisten für Ersatzteile, Lager in der Ersatzteil-Auswahl
- Checklisten-Vorlagen für Wartungspläne (mehrere je Plan)
- Einheitliche Symbol-Schaltflächen; Werkzeuge für Nextcloud-Betrieb

## [0.10.0] – 2026-05-20

### Neu
- Wiederverwendbare Widgets: Infrastruktur-Auswahlbaum, Aktionsschaltflächen, Design-Umschalter
- Datensatz-Galerie, Historie und Archiv

## [0.9.0] – 2026-05-17

### Neu
- Detailseiten, Upload (Galerie/Kamera/Dokument), Checklisten, Kontextmenü, IT-Assets

## [0.8.0] – 2026-05-17

### Neu
- Web-Oberfläche mit Templates, Anmeldung, Dashboard und HTMX

## [0.7.0]

### Neu
- Ersatzteillager: Verwaltung, Buchungen, Mindestbestand, Statistiken

## [0.6.0]

### Neu
- Wartungsplanung: Pläne, Aufträge, Intervalle, automatische Erzeugung

## [0.5.0]

### Neu
- Infrastruktur-Topologie: Baum, Verwaltung, Suche, Statistiken

## [0.4.0]

### Neu
- Zeiterfassung: Start/Stopp, Übersicht, Bezüge

## [0.3.0]

### Neu
- Schichtplanung: Modelle, Zuweisungen, Wochenplan

## [0.2.0]

### Neu
- Erste lauffähige Version: Programm, systemd-Dienst, Störungen mit Copilot, `.env`-Konfiguration
