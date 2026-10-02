# Änderungsprotokoll

Alle nennenswerten Änderungen am PDH-Server. Die Versionsnummer steht in der
Datei `VERSION` und folgt [Semantic Versioning](https://semver.org/lang/de/)
(Haupt.Neben.Fehlerkorrektur; vor 1.0 steigt die Nebenversion mit jedem
Funktionspaket). Versionen bis 0.9.0 stammen aus den Commit-Nachrichten, 0.10.0
bis 0.14.0 wurden nachträglich aus der Git-Historie zusammengefasst.

Aufbau je Version: `## [x.y.z] – JJJJ-MM-TT`, darunter `### Neu`, `### Geändert`,
`### Behoben`, `### Sicherheit` mit Stichpunkten. Das Handbuch zeigt diese Datei
im Kapitel „Versionen & Updates“ an.

## [0.21.0] – 2026-10-01

### Neu
- Neue Zuweisung: Im Feld „Zugewiesen an“ lassen sich neben Personen jetzt auch Gruppen auswählen (Tickets, Aufgaben, Wartungen, Störungen)
- Mehrsprachigkeit (erste Stufe): Deutsch, Englisch, Rumänisch, Türkisch und Mazedonisch; Umschalten über eine Flagge in der Kopfzeile und auf der Anmeldeseite, Vorwahl im Benutzerstamm (Stammdaten → Sprache), sonst Browsersprache; übersetzt sind Navigation, Kopfzeile, Menüs, Seitenleiste, Anmeldung und Seitentitel, Datum/Uhrzeit folgen der Sprache – weitere Seiten folgen schrittweise und erscheinen bis dahin auf Deutsch
- Lernende Text-Vorschläge: In Titeln, Beschreibungen, Maßnahmen, Ursachen, Lösungen, Kommentaren, Schulungsinhalten und Checklisten-Freitext schlägt PDH beim Tippen ganze Formulierungen, das nächste Wort oder das Wortende vor – gelernt aus den gespeicherten Texten; übernommene Vorschläge rücken nach oben (Tab übernimmt)
- Störungen zeigen ähnliche gelöste Fälle mit Ähnlichkeit in Prozent auch ohne Copilot-Analyse
- Rechte je Abteilung: Anlagen bekommen eine Abteilung (Unteranlagen erben sie); Rollen mit Abteilung sehen nur Vorgänge ihrer Abteilung und der Unterabteilungen (Listen, Detailseiten, Bearbeiten, Dashboard-Kennzahlen, -Verlauf, -Listen und Zeitstrahl, Anlagen-Historie samt CSV, Leitstand bei Anmeldung und Schnittstelle) – plus Vorgänge ohne Anlage und alles, woran man beteiligt ist. Instandhaltung, IT, Office und GL sind als übergeordnet vorbelegt und sehen alles
- Abteilungen mit übergeordneter Abteilung (Hierarchie); das Organigramm zeigt den Abteilungsbaum mit Leitung, Mitarbeitenden und Rollen, Rollen zeigen ihre Abteilung
- Schulungs- und Qualifikationsmatrix: Katalog mit Wiederholungsintervall, Vorlauf und Verantwortlichem; Pflicht je Rolle, Abteilung, Gruppe oder Person; Matrix je Person (gültig, läuft bald ab, abgelaufen, fehlt, geplant) mit Filtern; eigener Stand unter „Meine Schulungen“ und im Benutzerstamm (Reiter Schulungen); Pflichtschulungen auch auf der Rollenseite
- Schulungsnachweise: je Termin ein eigener Nachweis mit Inhalt in Stichpunkten, Schulende/r und Teilnehmenden, die am Bildschirm unterschreiben; vollständig unterschrieben wird er archiviert und ist danach unveränderlich; Druckansicht; Wiedervorlage als leeres Formular für den nächsten Termin
- Bei Fälligkeit legt PDH automatisch einen Schulungsnachweis mit allen Fälligen an und benachrichtigt den/die Verantwortliche/n im Chat
- Vorgänge (Tickets, Störungen, Wartungspläne und -aufträge, Aufgaben, Projekte) lassen sich zusätzlich einer Gruppe zuweisen: Mitglieder sehen sie unter „Mir zugewiesen“, bekommen die Änderungshinweise, und die Gruppe steht bei den Beteiligten; Benutzerliste filtert nach Abteilung und Gruppe
- Personalstamm: Abteilungen als Stammdaten mit Leitung (statt Freitext; vorhandene Angaben werden übernommen, Umbenennen wirkt bei allen, Microsoft-Abgleich legt fehlende an) und Gruppen mit Mitgliedern, Abteilung und Leitung – neue Seite „Abteilungen & Gruppen“, Auswahl im Benutzerstamm
- Rollen lassen sich einer Abteilung zuordnen (Rollen & Rechte)
- Einheitlich zwei Zuständigkeiten in allen Modulen („Verantwortlich“ und „Zugewiesen“): neu bei Wartungsplänen (Verantwortlich), Projekten (Zugewiesen, inkl. Bearbeiten auf der Projektseite und Änderungshinweisen) und IT-Assets (Verantwortlich); Aufträge aus einem Wartungsplan übernehmen den Verantwortlichen des Plans
- Wartungs-Checklisten: Messwerte mit Einheit und einzeln aktivierbaren Vorgaben Soll, Min und Max; bei der Durchführung zeigt PDH sofort, ob der Wert im Bereich liegt, und fragt bei Abweichungen nach
- Wartungs-Checklisten: an jedem Punkt Bilder zur Darstellung (in der Vorlage) und Fotos zur Dokumentation (bei der Durchführung, am Handy direkt mit der Kamera)
- Wartungsauftrag: Checklisten-Protokoll mit allen erfassten Werten, Bewertung, wer/wann und Fotos
- Abschluss-Assistent: Der grüne Haken zum Fertigsetzen von Tickets, Störungen, Aufgaben und Wartungen führt Schritt für Schritt durch Material (vormerken oder „kein Material“), Zeit (laufender Timer wird gestoppt), „Wer war dabei?“, Kommentar und Ursache – am Ende „Fertig“ oder „Geht noch weiter“ (bleibt in Bearbeitung)
- Wer beim Abschluss als „dabei“ ausgewählt wird, bekommt denselben Zeitraum als gelben, unbestätigten Eintrag in die Zeiterfassung und einen Chat-Hinweis; bestätigt wird mit dem grünen Haken (Karte „Zu bestätigen“ in der Zeiterfassung)
- Core-Einstellungen: Abteilungen, deren Mitarbeitende im Assistenten vorgeschlagen werden (Standard: Instandhaltung, Elektro, Mechanik)

### Geändert
- Navigation: Einträge in einklappbaren Gruppen (Instandhaltung, Material & Anlagen, Personal, Verwaltung, Daten, Hilfe & Links); „Navigation anpassen“ erlaubt eigene Gruppen, Umbenennen und Verschieben per Ziehen – gespeichert am Benutzerkonto, „Standard“ stellt die Vorgabe wieder her; Gruppen sind standardmäßig zugeklappt, der Klappzustand bleibt im Browser, die Gruppe der geöffneten Seite ist immer offen
- Copilot spricht Claude jetzt über das offizielle Anthropic-SDK für Go (automatische Wiederholung bei Überlast, typisierte Fehler); dafür braucht der Build Go 1.24: Docker-Image `golang:1.24`, die Update-Skripte laden auf Servern mit älterem Go (ab 1.21, z. B. Ubuntu 24.04) die passende Version automatisch nach
- Copilot: Standardmodell für Anthropic ist jetzt claude-opus-5-5 (mit serverseitigem Ausweichmodell bei Ablehnungen); bereits eingetragene Modelle bleiben
- Copilot: ähnliche Fälle werden jetzt inhaltlich gesucht (Titel, Beschreibung, Symptome; Wortformen und Umlaute egal, seltene Fachbegriffe gewichtet; gleiche Anlage/Linie bevorzugt) statt einfach die zuletzt gelösten zu nehmen; die Analyse bekommt Ursachen, Lösungen, Maßnahmen und den Anlagenpfad mit, Ollama liefert erzwungen JSON, die Konfidenz wird auf 0–100 % begrenzt und Fehler der KI-Dienste werden klar gemeldet
- Notizzettel auf dem Dashboard ist jetzt immer direkt beschreibbar und speichert automatisch; der Umweg über „Dashboard anpassen“ → Einstellungen entfällt
- Zeitstrahl (Dashboard, Leitstand, Projekte) folgt jetzt dem Dunkelmodus statt hell zu bleiben
- Kopfzeile: Die Benutzer-Schaltfläche hat jetzt dieselbe Höhe, Schrift und Umrandung wie die übrigen Knöpfe
- Abschließen verlangt jetzt auch eine erfasste Arbeitszeit; Material und Kommentar waren schon Pflicht
- „Auswahl erledigen“ in den Listen von Tickets und Störungen öffnet den Assistenten
- Bei Wartungen mit fälligen Checklistenpunkten werden zuerst die Messwerte erfasst, dann geht es im Assistenten weiter
- Unbestätigte Zeiten zählen nicht in Wochen-/Monatssummen, Diagrammen, Export und im Dashboard-Widget „Meine Stunden“

### Behoben
- Copilot: Analyse und Chat lieferten mit aktuellen Claude-Modellen keine oder unbrauchbare Antworten – das Antwortlimit (1500 Tokens) wurde vom Denken aufgebraucht und es wurde der erste statt des Text-Blocks gelesen; jetzt genug Spielraum, nur Textblöcke, Ablehnungen und API-Fehler werden mit Grund angezeigt
- Copilot-Analyse lief unsichtbar im Hintergrund, Fehler gingen verloren; jetzt sichtbar mit Ergebnis bzw. genauer Fehlermeldung
- Copilot in der Seitenleiste funktionierte außerhalb von Störungen nicht und zeigte Antworten ungeschützt als HTML an
- Copilot: Die angezeigte Ähnlichkeit der Vergleichsfälle war ein fester Platzhalterwert (80/70/60 %)
- Dashboard-Widgets „Mir zugewiesen“ und „Meine Aufgaben“ konnten nicht laden (Abfrage auf eine nicht mehr vorhandene Spalte der Aufgaben)
- Wartungsplan bearbeiten: Die zugewiesene Person wurde bisher nicht gespeichert
- Wartungs-Checklisten: Pflichtpunkte werden beim Abschluss wirklich geprüft (Browser und Server); Messwerte mit Komma werden akzeptiert
- Wartungs-Checklisten: Bearbeiten eines Punkts löscht die Beschreibung nicht mehr
- Wartungsauftrag: Der Abschluss-Assistent öffnet sich nicht mehr von selbst beim Aufrufen eines Auftrags ohne fällige Checklistenpunkte
- Der grüne Haken „Archivieren“ auf den Detailseiten von Tickets und Störungen schloss Vorgänge ohne Maßnahme und ohne Material-Angabe ab; er ist durch den Abschluss-Assistenten ersetzt

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
