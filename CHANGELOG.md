# Änderungsprotokoll

Alle nennenswerten Änderungen am PDH-Server. Die Versionsnummer steht in der
Datei `VERSION` und folgt [Semantic Versioning](https://semver.org/lang/de/)
(Haupt.Neben.Fehlerkorrektur; vor 1.0 steigt die Nebenversion mit jedem
Funktionspaket). Versionen bis 0.9.0 stammen aus den Commit-Nachrichten, 0.10.0
bis 0.14.0 wurden nachträglich aus der Git-Historie zusammengefasst.

Aufbau je Version: `## [x.y.z] – JJJJ-MM-TT`, darunter `### Neu`, `### Geändert`,
`### Behoben`, `### Sicherheit` mit Stichpunkten. Das Handbuch zeigt diese Datei
im Kapitel „Versionen & Updates“ an.

## [0.82.32] – 2026-11-22

### Neu

- **Herkunft von Tickets und Störungen:** Jeder Vorgang merkt sich, woher er kam – Easy-Modus, Anlagenterminal (mit Name), App, Fremdsystem über den Abgleich (z. B. „SAP PM“), Alarm (Energiedaten, IT-Netz) oder das PDH selbst (Firewall-Freigabe, Rüstzeit). Sie steht als Schild in den Listen und oben auf der Seite des Vorgangs.
- **Startseite:** „Offene Tickets“ und „Aktive Störungen“ zeigen unter der Zahl, woher die offenen Vorgänge kommen („davon 3 aus SAP PM · 2 aus der App · 1 vom Terminal“); „Aktuelle Störungen“ und „Mir zugewiesen“ zeigen die Herkunft als Schild.
- **Startseite: Hintergrundfarbe je Kachel.** Unter „Dashboard anpassen“ → Einstellungen der Kachel einen hellen Hintergrund wählen (Hellblau, Mint, Hellgrün, Hellgelb, Pfirsich, Rosé, Flieder, Hellgrau); im dunklen Modus erscheint derselbe Ton gedämpft.

### Geändert

- Vorhandene Tickets und Störungen bekommen ihre Herkunft nachträglich, soweit sie sich erkennen lässt (Abgleich, App, Alarm, Terminal, Easy-Modus, Firewall-Freigabe, Rüstzeit). Migration 202.

### Sicherheit

- Den Namen eines Fremdsystems sieht nur, wer den Abgleich sehen darf; sonst steht dort „Fremdsystem“. Die Zählung in den Kacheln gilt nur für Vorgänge der eigenen Abteilungen.

## [0.82.31] – 2026-11-22

### Neu

- **Terminal: Funktionen ein- und ausschalten.** Beim Terminal-Benutzer (Benutzerdetail) lassen sich **Störung, Ticket, Rüsten, Stillstand, HMI, Wartungen** und die **Folienschau als Bildschirmschoner** einzeln abschalten; eine ausgeschaltete Funktion verschwindet von der Seite **Neu**, ihre Seiten antworten mit „nicht gefunden“ (GET und POST). Die Schalter werden mit der Admin-Seite des PDH-Terminals abgeglichen: je Schalter gewinnt die **jüngere Änderung**, bei gleichem Zeitpunkt PDH, Zeitpunkte mehr als 5 Minuten in der Zukunft gelten als Serverzeit. Neue Schnittstelle `PUT /api/terminal/v1/functions`; `GET /api/terminal/v1/device` und das Ereignis `settings` tragen die neuen Felder `functions` und `theme`; der Ereigniskanal `/terminal/events` nimmt auch das Geräte-Geheimnis an. Die Folienschau behält ihr Feld `saver_slides` (ältere Terminals). Migration 201.
- **Terminalseiten in den PDH-Farben.** Hintergrund und Farben der Terminalseiten entsprechen dem übrigen PDH, **hell oder dunkel** (Einstellung beim Terminal-Benutzer); das Terminal bekommt dieselben Farben für seine eigenen Seiten.
- **Kiosk-Terminals: PDH-Terminals.** Die Liste zeigt die gekoppelten PDH-Terminals mit Typ, Version, Online-Status und Kopplung und verweist auf ihre Einstellungen.
- **Bildschirmtastatur am Terminal.** Auf dem Gerät erscheint die Tastatur beim Antippen eines Eingabefelds und verschwindet danach wieder.

### Geändert

- Nur hinzufügend: ältere Terminals und ältere Stände arbeiten in jeder Kombination weiter, vorhandene Werte (auch die Folienschau) bleiben erhalten.

## [0.82.30] – 2026-11-22

### Neu

- **Stillstandsgründe.** Neuer Katalog der Gründe (**Infrastruktur** → Karte **Stillstandsgründe**) und an jeder Anlage der Reiter **Stillstandsgründe**: Gründe zuordnen; eine Zuordnung an einer Halle oder Linie gilt auch für alle Anlagen darunter (wie die Rüsttasten). Vorgabe-Gründe: Störung / Defekt, Material fehlt, Kein Personal, Geplanter Stillstand – sie müssen erst Anlagen zugeordnet werden (Karte im Katalog oder Reiter an der Anlage), vorher zeigt das Terminal sie nicht. Neue Rechte `downtime.view`, `downtime.edit` (Stillstände erfassen, Katalog pflegen) und `downtime.admin` (Gründe löschen); sie folgen aus „Anlagen sehen/bearbeiten“, die Rolle Terminal bekommt ansehen und erfassen. Migrationen 198 bis 200.
- **Terminal: Stillstand melden.** Die Kachel **Stillstand** auf der Seite **Neu** (bisher nur ein Platzhalter) öffnet die Seite **Stillstand**: erst die Anlage wählen (Ebene für Ebene), dann den **Grund**; **Ende** beendet den Stillstand. Die Anmeldung per Karte ist freiwillig: ohne Karte meldet der Terminal-Benutzer (nur in der Gerätesitzung des Terminals, mit freiwilligem Kürzel/Name), mit Karte die Person. Es gilt `downtime.edit` des Terminal-Benutzers für die Abteilung der Anlage. An einer Anlage läuft höchstens ein Stillstand.
- **Auswertung der Stillstände.** Neue Seite **Stillstände** (Arbeitsbereich): Anzahl, Gesamtdauer und mittlere Dauer je Grund für 7 Tage, die letzten 5 Wochen oder 90 Tage, auf Wunsch für eine Anlage samt Unteranlagen; es zählen nur Anlagen, für die das Recht `downtime.view` gilt. Dazu die neue Folienart **Statistik Stillstände** im Infobildschirm (Kennzahlen und Minuten je Grund).

### Geändert

- Ältere Stände und vorhandene Daten bleiben unberührt: der Grund eines Stillstands ist ein freier Verweis (darf leer sein, auch wenn der Grund später gelöscht wird), Migrationen nur hinzufügend.

## [0.82.29] – 2026-11-22

### Neu

- **Netzwerk: Freigabe für ESPPDH-Updates in UniFi anlegen.** Beim Netz der ESPPDH-Geräte legt ein Knopf in UniFi die Firewall-Regel an, damit der Server neue Firmware übers Netz aufspielen kann: Erlauben, nur TCP-Port 3232 und 8266, nur von den Servern zum Adressbereich des Netzes. Liegt der Server im selben Netz oder in derselben Firewall-Zone, braucht es keine Regel; das PDH sagt das.
- **Ticket an die IT, wenn das nicht geht:** Ist UniFi nicht eingerichtet, zu alt (vor UniFi Network 10) oder darf der Schlüssel das nicht, legt das PDH ein Ticket mit Netz, Adressbereich, Server-Adressen, Ports und Grund an – je Netz nur eines, solange es offen ist.

### Sicherheit

- Den Knopf hat nur, wer das Recht „Netzwerk: VLANs und WLANs löschen (Bereichsadministrator)“ hat. Das PDH ändert oder löscht nie eine vorhandene Firewall-Regel.

## [0.82.28] – 2026-11-22

### Neu

- **Terminal: Kachel HMI.** Auf der Seite **Neu** steht neben **Rüsten** die Kachel **HMI**. Sie listet die HMIs der Anlage des Terminals und ihrer Unteranlagen und öffnet sie in einer eigenen Terminalseite (nur ansehen oder bedienen). Es gelten die vorhandenen Regeln des HMI-Fernzugriffs je Abteilung der Anlage: **Ansehen am Terminal** und **Bedienen am Terminal** (jeder / nur mit Karte / nie, mit „Bedienen erlaubt“ verrechnet) sowie die Rechte `hmi.view` und `hmi.control` – bei „jeder“ die des Terminal-Benutzers, bei „nur mit Karte“ die der angemeldeten Person. Die Kachel erscheint nur, wenn mindestens ein HMI sichtbar ist. Die Rolle Terminal bringt `hmi.view` mit (Migration 197), `hmi.control` nicht.
- **Terminal: Wartungen nach der Kartenanmeldung.** Die angemeldete Person sieht die offenen Wartungsaufträge der Anlage des Terminals und ihrer Unteranlagen – nur die, die sie selbst sehen darf (`maintenance.view` je Abteilung). **Öffnen** führt über „PDH öffnen“ in den Auftrag im normalen PDH, wo mit den eigenen Rechten der Person gearbeitet wird (`maintenance.edit`, `maintenance.complete`). Ohne Kartenanmeldung gibt es weder Kachel noch Liste.
- **Terminal: PDH öffnen.** In der Anmeldeleiste (nur mit angemeldeter Person) öffnet **PDH öffnen** das normale PDH im Browser des Terminals, angemeldet als diese Person mit ihren eigenen Rechten. Migration 196.

### Sicherheit

- **PDH-Sitzung am Terminal.** Das Öffnen verlangt die Gerätesitzung des Terminals **und** die aktive Kartenanmeldung derselben Person. Ein Einmal-Ticket (60 Sekunden, nur als Hash gespeichert, nie in einer Adresse) wird per POST eingelöst und setzt eine Sitzung von höchstens **15 Minuten ohne Verlängerung**. Sie ist serverseitig widerrufbar und an das Gerät gebunden: Ohne die Gerätesitzung desselben Terminals (auch an der Schnittstelle) gilt sie nicht. Sie endet bei Kartenabmeldung, Kartenwechsel, Abmelden in PDH und bei jedem Aufruf einer Terminalseite (Ruhezeit, Zurück); danach ist das Terminal wieder in seiner Gerätesitzung. Der normale PDH-Anmeldeweg bleibt unverändert.

## [0.82.27] – 2026-11-22

### Geändert

- **Terminal: Störung und Ticket ohne Anmeldung.** Wie beim Rüsten ist die Anmeldung freiwillig. Ohne Anmeldung meldet der Terminal-Benutzer; ein freiwilliges **Kürzel/Name** steht in der Beschreibung (wie im Easy-Modus). Mit Anmeldung meldet weiter die Person. Berechtigung bleibt `faults.edit` bzw. `tickets.edit` des Terminal-Benutzers.
- **Terminal: Anlagenwahl Ebene für Ebene.** Störung, Ticket und Rüsten zeigen zuerst nur die Anlage des Terminals und ihre direkten Unteranlagen; Antippen einer Anlage mit Unteranlagen öffnet deren Ebene, gewählt werden kann auf jeder Ebene.
- **Terminal: Meldetexte und Rüsttasten erst nach der Anlagenwahl** und die der gewählten Anlage (einschließlich geerbter).
- **Terminal: Feld Kürzel/Name größer** – volle Breite, große Schrift, fingerfreundlich (Störung, Ticket, Rüsten).
- **Terminal: Folienschau als Bildschirmschoner.** Neue Einstellung **Folienschau als Bildschirmschoner** neben „Globales Terminal“ auf der Seite des Systembenutzers (aus, wenn nicht gesetzt). Ist sie an, spielt der Schoner des normalen Terminals die Folien des Infobildschirms statt des Platzhalters. Berührung außerhalb eines Vorgangs und jede Karte wecken das Terminal wie bisher; ein Vorgang lässt sich antippen und nur lesen. Der Zwischenspeicher für den Ausfall bleibt dem globalen Terminal vorbehalten.

## [0.82.26] – 2026-11-22

### Behoben

- **Terminal: Rüsten geht ohne Anmeldung.** Die Kachel **Rüsten** verlangt keine Anmeldung mehr. Ohne Anmeldung rüstet der Terminal-Benutzer; beim Starten und Nachtragen kann ein freiwilliges **Kürzel/Name** eingetragen werden (wie im Easy-Modus). Mit Anmeldung rüstet weiter die angemeldete Person. Das Recht `setup.edit` hat weiterhin der Terminal-Benutzer; ohne es steht „Keine Berechtigung zum Rüsten“. Störung und Ticket brauchen weiter die Anmeldung.

## [0.82.25] – 2026-11-22

### Neu

- **ESPPDH: Firmware übers Netz aufspielen.** Auf der Seite eines Geräts spielt der Knopf **Firmware aufspielen** die zuletzt gebaute Firmware ohne USB-Kabel auf. Als gelungen gilt das Update erst, wenn das Gerät nach dem Neustart den neuen Bau meldet; sonst steht dort „aufgespielt, meldet sich nicht“.
- **ESPPDH: Alle veralteten Geräte aktualisieren.** Auf der Übersicht baut und spielt eine Sammelaktion alle Geräte nacheinander auf, deren Konfiguration geändert ist oder die nicht den letzten Bau melden. Geräte ohne Update-Baustein, ohne bestätigte Adresse oder außerhalb eines freigegebenen Netzes werden mit Grund übersprungen. Abbrechen ist möglich.
- **ESPPDH: Das Gerät meldet seinen Bau.** Die Seite des Geräts zeigt „Auf dem Gerät“ und „Zuletzt gebaut“.

### Geändert

- **ESPPDH:** Die Firmware enthält jetzt einen Update-Baustein mit eigenem Kennwort je Gerät und den Sicherheitsmodus von ESPHome. Nach dem Update gelten deshalb alle ESPPDH-Geräte einmal als „neu bauen“ und brauchen die neue Firmware **einmal per USB**; danach geht es übers Netz.
- **ESPPDH:** Aufgespielt wird nur in Netze, die auf der Seite „Netzwerk“ für ESPPDH-Geräte freigegeben sind und einen Adressbereich haben. Der Server muss das Gerät auf TCP-Port 3232 (ESP32) bzw. 8266 (ESP8266) erreichen.
- **ESPPDH:** „Neues MQTT-Kennwort“ erneuert auch das Kennwort für Updates übers Netz. Ist dieses nach einem Wechsel des Schlüssels nicht mehr lesbar, sagt die Seite des Geräts das, statt still ein neues anzulegen.

### Sicherheit

- **Neues Recht „ESPPDH: Firmware übers Netz aufspielen“** (`esppdh.ota`). Alle, die bisher Geräte bauen und flashen durften (`esppdh.edit`), bekommen es automatisch in derselben Abteilung. Die Sammelaktion braucht das Recht in allen Abteilungen.
- Updates nimmt ein Gerät nur mit seinem eigenen Kennwort an. Das Kennwort steht verschlüsselt im PDH und erscheint nie im Verlauf.

### Behoben

- **Startseite:** Unter der Begrüßung stand fest „Werk Augsburg“ (aus den ersten Beispieldaten). Dort stehen jetzt Firma und Ort aus den Firmenstammdaten (Infrastruktur → oberster Knoten → Reiter „Firma“); ohne Eintrag nur das Datum.

## [0.82.24] – 2026-11-22

### Neu

- App-Schnittstellen für die PDH-App: Vorgänge mit Starten, Maßnahme, Kommentar, Checkliste und Abschließen (der Text beim Abschließen wird als Maßnahme eingetragen).
- Scannen: Die App löst QR-Codes von Anlagen und IT-Assets auf und zeigt deren offene Vorgänge; Lager- und Kanban-Codes öffnen die passende Seite im Browser.
- Störung melden aus der App – nur mit der Freigabe „Vorgänge aus der App anlegen“ am Telefon und dem Recht „App: Vorgänge anlegen“.
- Handbuch: Kapitel „PDH-App“ um „Mit der App arbeiten“ ergänzt.

### Sicherheit

- Die App darf nie mehr als das Web: Es gelten die Rechte der Person, eingeschränkt auf die Freigabe des Telefons; Anlagen außerhalb des eigenen Abteilungsbereichs lassen sich nicht per Scan öffnen.
- Jede schreibende App-Eingabe wird genau einmal ausgeführt (Wiederholung nach einem Verbindungsabbruch trägt nichts doppelt ein); ein Fehler beim Prüfen meldet „vorübergehend nicht verfügbar“ statt eines falschen Konflikts.
- Auf Spiegelservern (nur lesen) und während der Wartungssperre nimmt der Server keine App-Eingaben an.
- Gescannte Adressen werden streng geprüft: nur Adressen dieses Servers, kein verstecktes „..“ (auch doppelt kodiert oder mit Rückwärtsstrich).

## [0.82.23] – 2026-11-22

### Neu

- **Rüsttasten am Anlagenterminal:** Auf der Seite „Neu“ erscheint unterhalb von Störung und Ticket die Kachel **Rüsten**, sobald an der Anlage des Terminals (direkt oder von Halle/Linie darüber geerbt) Rüsttasten vorhanden sind. Eine eigene Terminalseite (`/terminal/rusten`) zeigt die Tasten, das laufende Rüsten mit **Fertig** und **Rüstzeit nachtragen** (Rüsttaste, Beginn und Dauer per Antippen). Es gibt keinen Umweg über die Meldeseite des Easy-Modus. Rüstzeiten werden immer von der am Terminal angemeldeten Person erstellt (Karte oder manuelle Anmeldung); ohne Anmeldung verlangt die Seite zuerst die Anmeldung. Das Recht „Rüstzeiten: erfassen …“ (`setup.edit`) für die Abteilung der Anlage braucht der Terminal-Benutzer (Rolle Terminal), die angemeldete Person ist nur der Akteur. Gleiche Prüfungen und Bremse wie im Easy-Modus. Keine Migration.
- **Störung und Ticket am Anlagenterminal:** Die Kacheln auf „Neu“ öffnen jetzt eigene Terminalseiten (`/terminal/stoerung`, `/terminal/ticket`) statt der Meldeseite des Easy-Modus; sie funktionieren auch bei ausgeschaltetem Easy-Modus. Die Anlage des Terminals ist vorgegeben (eine Unteranlage darunter ist wählbar), dazu Meldetexte der Anlage oder freier Text, Art, „Anlage steht“ und optional Fotos. Gemeldet wird immer von der am Terminal angemeldeten Person (Karte oder manuell; ohne Anmeldung steht „Bitte zuerst anmelden“); der Terminal-Benutzer braucht `faults.edit` bzw. `tickets.edit` für die Abteilung der Anlage (Rolle Terminal bringt sie mit). Anlegen, Benachrichtigungen und Alarm bei „Anlage steht“ wie bisher.

### Behoben

- „Rüsttasten werden nicht angezeigt“: Das Anlagenterminal hatte bisher gar keine Rüsttasten; sie gab es nur auf der Meldeseite des Easy-Modus (und dort nur bei eingeschaltetem Easy-Modus).
- Die Meldeseite des Easy-Modus (`/e/…`) passt sich jetzt der Bildschirmgröße an (Handy, Terminal 1280×800 und 1920×1080, Hochformat) statt eines festen schmalen Streifens: breiteres Raster, größere Schrift und Tasten für Berührung.

## [0.82.22] – 2026-11-21

### Behoben

- Nach dem Öffnen des Reiters „Firma“ in der Infrastruktur antworteten fast alle Seiten mit einem Fehler (auch die Anmeldung), bis der Dienst neu startete. Der Reiter zeigt seine Inhalte jetzt so an, dass die übrigen Seiten unberührt bleiben.

## [0.82.21] – 2026-11-21

### Neu

- Firmenstammdaten: Im Reiter „Firma“ an jedem obersten Knoten der Infrastruktur pflegt ihr Name, Anschrift, Kontakt, Pflichtangaben und Bank eurer Firma – einmal für das ganze Unternehmen.
- Mails des PDH gehen mit dem Firmennamen als Absender hinaus und tragen eine Fußzeile mit Ansprechpartner (wer die Mail auslöst), Anschrift und Pflichtangaben.
- Druckkopf und Wartungsprotokoll zeigen Firmenname und Anschrift; das Protokoll nennt im Fuß Pflichtangaben und Ansprechpartner.
- Neue Platzhalter für M365-Signaturen: {Firmenanschrift}, {Firmentelefon} und {Website}; {Firma} kommt aus den Firmenstammdaten.

### Geändert

- Der Firmenname für M365-Signaturen wird nicht mehr auf der M365-Seite gepflegt, sondern in den Firmenstammdaten; ein vorhandener Wert wird übernommen.

### Sicherheit

- Neues Recht „Firmenstammdaten bearbeiten“ (company.edit); es wirkt nur, wenn es für das ganze Unternehmen gilt. Es bekommen die Rollen und Benutzer, die bisher Server-Einstellungen, M365-Vorlagen oder Rollen verwalten durften.

## [0.82.20] – 2026-11-21

### Neu

- Easy-Modus: beim Rüsten gibt es ein freiwilliges Feld „Kürzel/Name“ (steht bei der laufenden Rüstzeit) und unter „Rüstzeit nachtragen“ lässt sich eine schon beendete Rüstzeit mit Beginn und Ende oder Dauer erfassen – ohne Anmeldung, mit denselben Bremsen wie die Meldungen.

## [0.82.19] – 2026-11-21

### Neu

- Infobildschirm: neue Folien „Statistik Störungen“, „Statistik Tickets“ und „Statistik Rüstzeiten“ (Kennzahlen und Säulendiagramm für 7 oder 30 Tage, für die gewählten Anlagen samt Unteranlagen) sowie „Grafana-Dashboard“ zum Einbetten eines Dashboards.
- Neue fertige Rolle „Terminal“: darf Tickets, Störungen und Rüstzeiten ansehen und melden, sonst nichts; über die Abteilung im Rollen-Dialog auf eine Halle begrenzbar (Handbuch „Anlagenterminal“).

### Sicherheit

- Die Folienschau darf per Content-Security-Policy nur PDH selbst und die Adresse der Grafana-Folien einbetten; Statistik und Grafana-Folien zeigen nur, was der Terminal-Benutzer sehen darf.

## [0.82.18] – 2026-11-21

### Neu

- PDH-App, Zugang (Paket 1): Ein Telefon kann den Zugang anfragen; dabei entsteht ein APP-Ticket für die Gruppe „App-Administratoren“ und das Telefon zeigt einen Code.
- Neue Seite „App-Konfiguration“: Der Administrator gibt den Code ein, wählt die Person und die erlaubten Vorgangsarten und gibt das Telefon frei oder lehnt die Anfrage ab; das Ticket wird dabei geschlossen.
- Neue Seite „App-Geräte“ unter Verwaltung: eingerichtete Telefone mit Benutzer, System, App-Version und letztem Zugriff, Sperren sowie Einstellungen für die App (zusätzliche Adressen, neueste und vorige App-Version, Store-Adressen).
- Im Benutzermenü gibt es „App installieren“ mit QR-Code und Store-Knöpfen; am Benutzer stehen seine App-Geräte samt Sperren.
- Das Handbuch hat ein neues Kapitel „PDH-App“ mit Skizze und Schritt-für-Schritt-Anleitungen. Die App selbst folgt mit den nächsten Paketen.

### Sicherheit

- Neue Rechte „App: nutzen“, „App: Vorgänge anlegen“, „App: Cockpits verwalten“, „App: Telefon-Sensoren ansehen“ und „App: Geräte freigeben und sperren“ (Gruppe „App“); wirksam ist, was die Person und das Gerät gemeinsam dürfen – das Gerät kann nur einschränken.
- Jedes Telefon hat einen eigenen Schlüssel; Anfragen sind signiert und nur kurz gültig, der Zugangsschlüssel wird nur als Prüfwert gespeichert. Die bisherige Schnittstelle `/api/v1` lehnt App-Zugänge ab.
- Eine Sperre trennt das Telefon sofort, spätestens nach 30 Sekunden. Danach muss die App zurückgesetzt und der Zugang neu angefragt werden.
- Die Abteilungsliste für die Anfrage ist ohne Anmeldung lesbar und enthält nur Abteilungsnamen.

## [0.82.17] – 2026-11-21

### Geändert

- Ein MQTT-Name wird auch ohne Eintrag im DNS im Haus scharf geschaltet, sobald Zertifikat und Firewall stehen; der fehlende Eintrag steht in der Prüfliste als Hinweis.

## [0.82.16] – 2026-11-21

### Neu

- Am gekoppelten PDH-Terminal öffnet ein kleines Zahnrad oben rechts die Einstellungen des Terminals (in allen Ansichten, auch Schoner und Folienschau).

## [0.82.15] – 2026-11-21

### Behoben

- Ein neuer MQTT-Name kann den Zugang zum PDH nicht mehr unterbrechen: Der Update-Agent prüft nach jeder neuen nginx-Seite, ob alle anderen Namen ihr Zertifikat behalten, und nimmt sie sonst zurück.
- Eine nginx-Seite des Update-Agenten hört nur noch dann auf IPv6, wenn alle anderen Seiten auf Port 443 das auch tun.
- Nach einer Änderung des WebSocket-Ports zeigt die Tunnel-Zeile des MQTT-Namens selbst auf den neuen Port.

### Geändert

- Ein neuer oder geänderter MQTT-Name ist zuerst vorgemerkt und wird erst scharf geschaltet, wenn Zertifikat, Eintrag im Haus und Firewall stehen; was das PDH nicht selbst darf, zeigt die Prüfliste mit dem genauen Eintrag.
- Der Cloudflare-Tunnel erreicht die Dienste über 127.0.0.1 statt localhost; vorhandene Zeilen mit localhost gelten weiter als richtig.

## [0.82.14] – 2026-11-21

### Neu

- Auf den Seiten „Kiosk-Terminals“ und beim Systembenutzer (Gekoppelte Geräte) führt der Link „Terminal-Software herunterladen“ zur neuesten Ausgabe der Terminal-Software auf GitHub: Raspberry-Pi-Abbild, Windows-Setup (EXE/MSI) und Updates.

## [0.82.13] – 2026-11-21

### Neu

- Am Systembenutzer eines Terminals lässt sich einstellen, wie ein Windows-Terminal im Hintergrund auf eine gelesene Karte aufmerksam macht: Windows-Mitteilung, Fenster in der Mitte (Vorgabe), Leiste oben, Terminal nach vorn oder keine Benachrichtigung. Die Einstellung erreicht das Terminal sofort; ältere Terminals ignorieren sie.
- Am Systembenutzer eines Terminals gibt es das Kästchen „Globales Terminal (Infobildschirm)“. Dazu gehören ein Speicher für Folien (Bild, Text, Vorgänge mit Dauer und Reihenfolge) und das neue Recht „Infobildschirm bearbeiten“ (Admin und Manager); die Umschaltung erreicht das Terminal sofort.
- Ein globales Terminal (Infobildschirm) zeigt jetzt die Folien: Bild, Text oder die offenen Störungen und Tickets ausgewählter Anlagen, jede so lange, wie ihre Dauer sagt. Tippen oder Wischen nach links blättert weiter; ein angetippter Vorgang öffnet eine reine Leseansicht (Titel, Status, Anlage, Beschreibung, die letzten fünf Verlaufseinträge, keine Anhänge, nur „Zurück“), die nach der Rückkehrzeit zur Folienschau zurückkehrt. Zu sehen ist nur, was der Terminal-Benutzer für die gewählten Anlagen sehen darf. Ein globales Terminal liest keine Karten und bietet keine Anmeldung. Das Terminalprogramm erhält zudem einen Zwischenspeicher der Folien für den Betrieb ohne PDH; ältere Terminals ignorieren das.
- Neue Seite „Infobildschirm“ (Administration, mit dem Recht „Infobildschirm bearbeiten“): Folien für globale Terminals anlegen, ändern, löschen und mit Hoch/Runter ordnen – als Bild (JPEG, PNG, WebP, bis 5 MB), Text (bis 2000 Zeichen) oder Vorgänge (Störungen und Tickets ausgewählter Anlagen), je mit Dauer und Aktiv-Haken. Jede Änderung erreicht die Infobildschirme sofort. Das Handbuch hat dazu das Kapitel „Infobildschirm“.
- Windows-Terminals melden nach jeder Anmeldung und Abmeldung per Karte das Ergebnis an das lokale Terminalprogramm, damit es im Hintergrund benachrichtigen kann. Ältere Terminals ohne diese Schnittstelle laufen unverändert weiter.

### Geändert

- Eine Karte weckt jetzt auch den Bildschirmschoner eines Terminals (auch am Raspberry Pi): Die Störungsinfo öffnet sich und die Person ist gleich angemeldet.

### Behoben

- Meldet das Terminal die Abmeldung per Karte an das lokale Terminalprogramm, gilt sie nur noch dann als gelungen, wenn PDH sie angenommen hat; bei einem Fehler zeigt die Benachrichtigung die Störung statt „Abgemeldet“.
- Infobildschirm bei Ausfall von PDH: Nach drei fehlgeschlagenen Abrufen der Folien oder des Ereignisstroms in Folge wechselt ein globales Terminal zur Startseite des Terminalprogramms, die die gespeicherte Folienschau zeigt und nach PDH zurückkehrt, sobald es antwortet. Auch „Zurück“ und die Rückkehrzeit der Vorgang-Ansicht prüfen vorher, ob PDH antwortet.
- Ein Terminal mit Anlage oder ohne Anlage wird sofort zur Folienschau, wenn es als globales Terminal gekennzeichnet wird; bisher galt das nur für die Info- und Neu-Seiten. Eine noch offene Kartensitzung eines globalen Terminals wird beim Wechsel beendet, und die Meldung „Keine Folien eingerichtet“ nennt den Namen der Instanz statt „PDH“.

## [0.82.12] – 2026-11-21

### Geändert

- PDH steht jetzt unter der Lizenz AGPL-3.0-or-later (vorher GPL-3.0-only); was PDH für dich erzeugt (z. B. SCL-Bausteine, ESPHome-YAML, Exporte), gehört dir (Ausnahme in `LICENSE-EXCEPTION.md`), und `THIRD_PARTY_LICENSES.md` nennt die Lizenzen aller Fremdbestandteile.
- Das PDH heißt jetzt „PDH – Pulse Data Hub“ (vorher „Prozess Data Hub“); der lange Name steht auf der Anmeldeseite, im Handbuch und in der Nextcloud-App, in der Kopfzeile bleibt es beim kurzen „PDH“.
- Handbuch, Hilfetexte und Eingabefelder zeigen als Beispiel jetzt neutrale Adressen (z. B. 192.0.2.202 und 2001:db8::15) statt Adressen aus einem echten Netz.

## [0.82.11] – 2026-11-21

### Neu

- ESPPDH-Geräte haben eine eigene Webseite (ESPHome-Webserver Version 3) mit eigenem Kennwort; sie lässt sich je Gerät ausschalten und ist nur im Netz der Geräte erreichbar.
- ESPPDH-Geräte melden ihre Adresse; die Geräteseite zeigt einen Knopf „Webseite des Geräts“, sobald die Adresse zur Verbindung des Geräts passt.
- Die Geräteseite zeigt die letzten Werte des Geräts nach Gruppen und seinen Handshake (Laufzeit-Zähler: grün „läuft“, rot „steht seit …“).
- An der Anlage gibt es den neuen Reiter „ESPPDH“ mit den Geräten dieser Anlage, ihren letzten Werten, dem Handshake und Links zum Gerät.
- Am Anlagenterminal sieht ein Instandhalter nach der Kartenanmeldung die Sensoren und Handshakes der Anlage samt Unteranlagen.
- Die Anlagen-Schnellübersicht zeigt je Anlage die Handshakes von ESPPDH-Geräten und Steuerungen (z. B. TIA-Baustein) mit laufendem Zähler; steht einer, wird die Karte gelb.

### Sicherheit

- Das Kennwort der Geräte-Webseite steht verschlüsselt im PDH; „Kennwort anzeigen“ braucht das Recht zum Bearbeiten, und jedes Anzeigen steht im Server-Protokoll.
- Über die Geräte-Webseite lässt sich im Betrieb keine Firmware aufspielen (nur im Notzugang), und nichts schalten.
- Neues Recht „ESPPDH: Sensorwerte ansehen (Anlage und Terminal)“ für Administrator, Manager, Anlagenverantwortliche und Techniker.

## [0.82.10] – 2026-11-21

### Behoben

- Das Startseiten-Widget „Bestellliste“ (Ersatzteile unter Mindestbestand) blieb leer und schrieb bei jedem Laden eine Warnung ins Server-Protokoll; es zeigt die Teile jetzt wieder, mit dem Lagerplatz, an dem die meisten liegen.

## [0.82.9] – 2026-11-21

### Behoben

- Fälligkeiten mit reinem Datum (Kanban, Rahmenverträge, Bestellungen, Projekte, Schulungen, Qualifikationen, Vertragsende bei Partnern, Wiedervorlage im Leitstand) und die Startseiten-Liste rechnen jetzt mit dem Kalendertag in Ortszeit; vorher galt zwischen Mitternacht und 1 Uhr (Sommer 2 Uhr) noch der Vortag, und eine Schulung, die am letzten Tag der Vorwarnfrist abläuft, wurde nicht als „bald fällig“ gezeigt.
- Den Knopf „Neues Projekt“ gibt es nur noch mit dem Recht „Projekte bearbeiten“ – ohne das Recht scheiterte bisher erst das Speichern. Das Recht hieß unter „Rollen & Rechte“ bisher „Projekte: Zusatzfelder (Feldsätze) bearbeiten“, obwohl es seit 0.76.4 auch das Anlegen und Bearbeiten von Projekten regelt.
- Ändert man am aktiven Server die DNS-Server oder stellt die Adresse fest ein, erscheint „Seite erreichbar – Einstellung behalten“ erst, wenn auch die gemeinsame Adresse wieder da ist; vorher schaltete schnelles Bestätigen die Wartung aus, während sie noch fehlte.

## [0.82.8] – 2026-11-20

### Geändert

- Kann PDH die Einstellungsdatei nicht speichern, nennt die Seite „Server-Einstellungen“ jetzt den Grund (etwa „Die Datei gehört „root“, PDH läuft als „michael“.“) und den Befehl zur Abhilfe am Server; geprüft wird auch der Ordner, in dem PDH beim Speichern eine Hilfsdatei anlegt.

### Sicherheit

- Die Seite „Server-Einstellungen“ warnt, wenn die Einstellungsdatei für andere Benutzer des Servers lesbar ist – sie enthält Kennwörter und Schlüssel –, und nennt den Befehl, um das abzustellen.

## [0.82.7] – 2026-11-20

### Behoben

- Handbuch und Änderungsprotokoll zu 0.82.5 sagen jetzt richtig, wie ein Netzlaufwerk den neuen Besitzer übernimmt: Laufwerk öffnen und „Speichern“ drücken; „Trennen“ hätte das automatische Einbinden ausgeschaltet.

## [0.82.6] – 2026-11-20

### Behoben

- Ist am Update-Agenten nur die Gruppe für Netzlaufwerke fest eingestellt (`PDH_MOUNT_GID`), gilt sie jetzt auch, wenn der Benutzer selbst ermittelt wird; vorher konnte die Gruppe des Dienstes sie überschreiben.

## [0.82.5] – 2026-11-20

### Behoben

- „Testen“ bei einem Netzlaufwerk nennt jetzt den echten Grund, wenn PDH nicht schreiben kann (Zugriff verweigert, nur lesend eingebunden, kein Speicherplatz frei oder Verbindung zum NAS gestört), statt immer „Freigabe-/Dateirechte prüfen“; die Liste der Laufwerke zeigt den Grund neben „nicht beschreibbar“.
- Der Zeuge im Docker-Container zieht wieder selbst auf die neue Version nach; seit 0.82.4 fehlte beim Bau ein Programmteil, er blieb deshalb auf 0.82.2 stehen.
- Läuft der PDH-Dienst unter einem anderen Benutzer als „pdh“, gehören eingebundene SMB-Laufwerke jetzt diesem Benutzer, sodass PDH dort schreiben darf; ein schon eingebundenes Laufwerk dafür einmal öffnen und „Speichern“ drücken (nicht „Trennen“ – das schaltet das automatische Einbinden aus).

## [0.82.4] – 2026-11-20

### Neu

- Die Seite „Netzwerk“ schlägt Netze und WLANs aus UniFi (Network ab Version 10.0) und Omada vor, du übernimmst sie mit einem Klick oder gleichst abweichende Einträge an.
- WLAN-Kennwörter werden verschlüsselt mit übernommen, wenn der Controller sie herausgibt; sonst weist die Seite darauf hin, sie von Hand einzutragen.
- Vorschläge lassen sich ausblenden, und Einträge, die am Controller nicht mehr vorhanden sind, werden markiert, aber nie gelöscht.
- Neue Karte „Dieser Server“ zeigt Adresse, Gateway, DNS-Server und ob die Adresse fest oder automatisch ist, im Verbund auch die des Partners.
- Administratoren können die jetzige Adresse fest einstellen und die DNS-Server ändern; bleibt die Bestätigung nach 2 Minuten aus, stellt der Server die vorige Einstellung selbst wieder her.

### Geändert

- Der Ausfallschutz meldet dem Partner-Server jetzt die eigenen Netzwerkdaten, damit die Seite „Netzwerk“ beide Server zeigen kann.
- Die Anleitung zu festen IP-Adressen verweist auf die neue Karte; das Konsolen-Skript funktioniert weiter wie bisher.

## [0.82.3] – 2026-11-19

### Neu

- Zwei neue Rechte: „Projekte: ansehen“ und „KVP: alle Vorschläge ansehen“ bestimmen, wer Projekte bzw. alle KVP-Vorschläge sieht, auch begrenzt auf eine Abteilung.
- Alle bisherigen Rollen haben beide Rechte, bis ein Administrator sie entzieht; neu angelegte Rollen bekommen sie nicht von selbst.
- Wer an einem Projekt oder einer KVP-Idee beteiligt ist, sieht sie immer, auch ohne das Ansehen-Recht.

### Geändert

- Ohne „Projekte: ansehen“ fehlt der Menüpunkt Projekte, die Liste zeigt nur „Meine Projekte“, und Detail, Kalender, Schnittstelle, Copilot und Zähler zeigen nur sichtbare Projekte.
- Ohne „KVP: alle Vorschläge ansehen“ zeigt die KVP-Seite nur „Meine Vorschläge“ und „Idee einreichen“, ohne Kennzahlen und Board; Einreichen dürfen weiter alle.
- Das Widget „KVP-Vorschläge“ auf der Startseite erscheint nur noch mit dem Recht und zählt nur sichtbare Ideen.
- „Projekte bearbeiten“ und „KVP steuern“ wirken ohne das Ansehen-Recht nur an Vorgängen, an denen man beteiligt ist.
- Aufgaben, Partner und Anlagen-Kommentare lassen sich nur noch mit Projekten verknüpfen, die man sehen darf; ein schon verknüpftes Projekt bleibt beim Speichern erhalten.
- Ein Projekt-Link im Chat zeigt den Projektnamen nur, wenn der Absender das Projekt sehen darf.
- Die Historie von Aufgaben und Wartungen sieht jetzt, wer die Aufgabe oder Wartung öffnen darf, wie die Seite selbst.
- Im Server-Protokoll erscheinen die Felder „von“ und „bis“ nur noch, wenn beim Zeitraum „frei wählen …“ ausgewählt ist.

### Behoben

- Der Reiter „Zuständigkeiten“ auf der Benutzerseite war für alle leer und zeigt wieder die offenen Vorgänge.
- Die Reiter der Projektübersicht zählen bei Rollen mit Abteilung dieselben Projekte, die die Liste zeigt.
- Auf der KVP-Seite zeigen der Reiter „Meine Vorschläge“ und der Filter „Meine“ dieselben Ideen, auch solche, bei denen man nur für eine Maßnahme eingetragen ist.
- Der aktive Knopf der Sortier-Leiste hat in Fiori wieder einen 1 Pixel dünnen Unterrand.
- In der KVP-Karte „Do“ bricht die Sortier-Leiste nicht mehr auf mehrere Zeilen um, sondern ist ein Auswahlfeld.
- Eine einzelne Zeile direkt unter einer aufgeklappten Gruppe in Verläufen, Maßnahmen und Kommentaren hat jetzt Abstand.
- Im Server-Protokoll überlappt das Feld „Stufe“ nicht mehr das Feld „bis“, sondern bricht in die nächste Zeile um.

### Sicherheit

- Der Leitstand-Kalender ohne Anmeldung zeigt Gästen genau die Einstellung des Leitstands, keine anderen Arten, Abteilungen oder Personen über die Adresse und keine Auswahllisten von Abteilungen, Gruppen und Personen; ein unbekannter Leitstand ergibt „nicht gefunden“.
- Unter `/uploads/` gibt es keine Ordnerlisten mehr.
- Datensatz-Kennungen in anderer Schreibweise in Adressen werden abgewiesen, Kennungen in Großbuchstaben auf die normale Schreibweise umgeleitet; vorher umgingen solche Adressen die Abteilungssperre und die Sichtprüfung.
- Die Rechteprüfung beim Speichern und Löschen erkennt die angemeldete Person jetzt genau wie die Anmeldung selbst und weist Anfragen ab, deren Anmeldung nicht mehr gilt; vorher ließ sie sich mit einer leicht veränderten Anmeldung umgehen.
- Die Schnittstelle (API) weist beim Abruf einzelner Vorgänge, von Vorgangslisten und von KVP-Anhängen Zugriffs-Token ab, deren Benutzer sich nicht bestimmen lässt oder deaktiviert ist, auch wenn das Token mit Leerzeichen geschrieben ist; vorher umgingen solche Anfragen die Abteilungssperre.
- Die Liste der Anhänge einer KVP-Idee und das Hochladen über die Schnittstelle gehen nur noch mit dem Ansehen-Recht oder als Beteiligter; wer den Link einer Datei hat, kann sie weiterhin öffnen.

## [0.82.2] – 2026-11-19

### Neu

- Über Verläufen, Maßnahmen, Kommentaren und Protokoll-Listen steht jetzt die Leiste „Sortieren:“ mit Neueste zuerst, Älteste zuerst, Häufigste zuerst, Gleiches zusammen, Nach Person und Nach Art; bei den KVP-Maßnahmen zusätzlich Nach Termin.
- „Häufigste zuerst“ und „Gleiches zusammen“ fassen gleiche Einträge zu einer Zeile wie „12× Text – zuletzt 18.11.2026“ zusammen, die sich zum Ansehen der Einzeleinträge aufklappen lässt.
- Texte, die sich nur in Groß- und Kleinschreibung, Umlauten, Satzzeichen oder Leerzeichen unterscheiden, zählen als gleich.
- PDH merkt sich die Sortierung je Benutzer und Listenart; am Spiegelserver gilt sie nur für den Aufruf.
- Auf schmalen Bildschirmen (bis 760 Pixel) wird die Leiste zu einem Auswahlfeld.
- Die Historie eines Datensatzes zeigt zuerst 30 Einträge und lädt mit „Weitere laden“ nach.
- Sortiert und zusammengefasst werden die neuesten 5000 Einträge, darüber steht ein Hinweis.
- Die Anlagen-Historie hat die Sortier-Leiste neben den Filtern (die Filter bleiben beim Umschalten erhalten) und berücksichtigt bis zu 2000 Einträge.
- Sortieren lassen sich außerdem Lagerbewegungen, Prüfnachweise und HMI-Zugriffe einer Anlage, VPN-Ereignisse, ESPPDH-Bauläufe, Sicherungen, die beiden Listen der Bereinigung, Abgleich-Läufe, Efficio-Export-Läufe und das Server-Protokoll.

### Geändert

- Die Maßnahmen an einer Wartung stehen jetzt immer im Reiter „Übersicht“ (dort nur zum Lesen; eingetragen wird weiter beim Abschließen).
- Maßnahmen an Störung, Ticket, Aufgabe und Wartung sowie Kommentare an Ticket und KVP sind je ein gemeinsamer Baustein und aktualisieren sich ohne Neuladen der Seite.
- Eine abgewiesene oder ungültige Eingabe bei Maßnahmen und Kommentaren zeigt eine Meldung auf Deutsch im Baustein.
- Kommentare an Tickets sind auf 4000 Zeichen begrenzt.
- Im Server-Protokoll dreht „Älteste zuerst“ nur die zuletzt geladenen Zeilen um; ein Hinweis sagt das.

### Sicherheit

- Das Löschen einer Maßnahme über die Schnittstelle (API) prüft jetzt, ob die Maßnahme zu dem Vorgang gehört, unter dem sie gelöscht wird.
- Die Historie einer Anlage und eines Lagerorts kann nur noch abrufen, wer diese Anlage oder diesen Lagerort sehen darf; vorher ging das für jeden angemeldeten Benutzer.

## [0.82.1] – 2026-11-19

### Neu

- ESPPDH: ESPHome hält sich selbst aktuell – jeder Server prüft einmal am Tag auf eine neuere Version, richtet sie neben der laufenden ein, probt sie an den Geräten und schaltet nur um, wenn die Probe klappt.
- Die Probe prüft die Konfiguration aller nicht gesperrten Geräte und baut am aktiven Server je verwendeter Chip-Familie probeweise eine Firmware mit einem Gerät, dessen letzter Bau geklappt hat.
- Scheitert die Probe, bleibt die bisherige Version, und die ESPPDH-Bereichsadministratoren bekommen einmal je Version eine Meldung.
- Das PDH merkt sich alle gescheiterten ESPHome-Versionen und versucht keine davon täglich neu.
- Ein Neustart des PDH während der Probe, zu wenig freier Platz oder ein Gerät, das schon mit der bisherigen Version nicht durchgeht, zählt nicht gegen die neue Version.
- Server-Einstellungen, Reiter **ESPPDH**: Schalter „ESPHome selbst aktuell halten“ (ab Werk an), Knöpfe „Jetzt prüfen“ und „Zurück zur vorigen Version“ sowie Anzeige von letzter Prüfung, Ergebnis und gescheiterter Version mit Ausgabe.
- Die Seite eines Geräts weist darauf hin, wenn seine Firmware mit einer älteren ESPHome-Version gebaut wurde als die, die der Server jetzt hat; neu bauen ist möglich, aber nicht nötig.
- Handbuch: Unterabschnitt „ESPHome bleibt von selbst aktuell“ im Kapitel „ESPPDH – eigene Mess- und Zählgeräte“.

### Geändert

- Die ESPHome-Version, die mit dem PDH kommt, ist jetzt die Mindestversion; „veraltet“ zeigt der Reiter nur noch bei einer älteren Umgebung.
- PDH-Updates stufen ESPHome nicht mehr herab: eine neuere, selbst geholte Version bleibt bestehen.
- Ein echter Bau und eine Probe laufen nie zugleich: Ein Bau, der während einer Probe startet, ist nach dem laufenden Prüfschritt dran und lässt sich bis dahin abbrechen.

### Sicherheit

- Der Update-Agent holt nur das Paket `esphome` von PyPI und nimmt nur Versionsangaben der Form JJJJ.M.P an.
- Eine neue ESPHome-Version muss mindestens 3 Tage veröffentlicht sein, bevor das PDH sie probt (Karenz).
- Der Update-Agent stuft nie von selbst auf eine ältere ESPHome-Version herab – zurück geht es nur auf die eine aufbewahrte vorige Version – und löscht keine Umgebung, solange eine eingerichtet wird.
- Der Update-Agent richtet keine ESPHome-Version ein, die er danach nicht einschalten dürfte, und nimmt den Ordner der Versionen nicht aus einer Umgebungsvariable.
- Kein neues Recht: Schalter und Knöpfe prüfen dasselbe Recht wie „Umgebung einrichten“.
- Die Meldung bei gescheiterter Probe geht an alle mit dem Recht `esppdh.admin`.

## [0.82.0] – 2026-11-19

### Neu

- Neue Seite **Daten → ESPPDH**: eigene Mess- und Zählgeräte aus ESP32- und ESP8266-Platinen anlegen, je Gerät mit Name, Kennung, Platine und Netzanschluss (WLAN von der Seite „Netzwerk“ oder Kabel).
- Bausteine für die Geräte: S0-Zähler, Stromzange SCT-013, Energiemesser PZEM-004T und Stückzähler (Lichtschranke); das PDH prüft die Anschlüsse der Platine und meldet falsche Belegungen verständlich.
- Server-Einstellungen, neuer Reiter **ESPPDH**: Umgebung für die Firmware einrichten (ESPHome 2026.10.2 in fest vorgegebener Version, je Server einmal) und ESPPDH ein- oder ausschalten; jedes PDH-Update bringt die passende ESPHome-Version mit.
- Die Firmware wird am Server gebaut, mit Warteschlange und Protokoll je Lauf; sie sendet die Messwerte an den eingebauten MQTT-Broker.
- Firmware herunterladen oder direkt im Browser per USB auf die Platine spielen (Chrome oder Edge, über eine https-Adresse).
- Zustand je Gerät: verbunden, nicht verbunden oder gesperrt; Geräte lassen sich sperren, wieder freigeben und trennen.
- Handbuch: Kapitel „ESPPDH – eigene Mess- und Zählgeräte“. Noch nicht an echter Hardware geprüft: Platinen, Bausteine und USB-Flash wurden bisher nur automatisch getestet.

### Geändert

- MQTT-Broker: Gerätezugänge dürfen sich nur mit ihrer eigenen Client-ID verbinden und können keine fremde Sitzung übernehmen.
- MQTT-Broker: Der Hauptbenutzer des Brokers darf nicht mit „esppdh-“ beginnen; diese Namen sind für ESPPDH-Geräte reserviert.
- Seite „Netzwerk“: Jedes WLAN hat den neuen Haken „Für ESPPDH-Geräte nutzbar“ (ab Werk aus), freigegebene WLANs tragen in der Liste das Abzeichen „ESPPDH“.

### Sicherheit

- ESPPDH bietet nur WLANs an, die auf der Seite „Netzwerk“ für ESPPDH-Geräte freigegeben sind, denn wer Geräte bauen darf, kann das WLAN-Kennwort aus der Firmware lesen; den Haken setzen darf nur, wer die Seite „Netzwerk“ pflegt (`system.network`).
- Neue Rechte **ESPPDH: Geräte und Zustand ansehen** (`esppdh.view`), **… anlegen, ändern, sperren, bauen und flashen** (`esppdh.edit`) und **… löschen** (`esppdh.admin`, Bereichsadministrator). Ansehen und pflegen dürfen automatisch alle Rollen mit dem Recht „Import-Verbindungen verwalten“, löschen nur Rollen, die zusätzlich die Server-Einstellungen ändern dürfen; die Rechte gelten je Abteilung.
- Jedes Gerät hat einen eigenen Zugang am MQTT-Broker, beschränkt auf sein eigenes Thema `esppdh/<kennung>/…`; ein gesperrtes Gerät kommt nicht mehr hinein.
- Die fertige Firmware und die Gerätekennwörter werden verschlüsselt abgelegt; Firmware laden darf nur, wer das Pflegerecht (`esppdh.edit`) für das Gerät hat.
- Die Seite eines ESPPDH-Geräts warnt, wenn sein Broker anonyme Verbindungen erlaubt, denn dann kann jeder im Netz unter dem Thema des Geräts senden.
- Der MQTT-Broker kennt von jedem Gerät nur einen Prüfwert des Kennworts, nie das Kennwort selbst, und die Geräte behalten ihren Zugang auch nach einem Wechsel des Schlüssels – vor dem nächsten Bau der Firmware braucht ein Gerät dann ein neues MQTT-Kennwort.
- ESPPDH ist im Docker-Testbetrieb nicht verfügbar, nur im Betrieb mit systemd.

## [0.81.6] – 2026-11-18

### Behoben

- Nummern von Rüstzeiten und KVP-Vorschlägen werden ab 10000 nicht mehr abgeschnitten („RZ-10105“ statt „RZ-1010“); Bestell- und Partnernummern bleiben auch bei langen Zählerständen eindeutig.

## [0.81.5] – 2026-11-18

### Behoben

- Efficio-Export, Karte „Anmeldung mit Schlüssel“: Der Fingerabdruck des Schlüssels bricht auf schmalen Bildschirmen jetzt um, statt über den Kartenrand hinauszulaufen.

## [0.81.4] – 2026-11-18

### Neu

- Neue Seite **Administration → Netzwerk**: die Netze (VLANs mit Nummer, Adressbereich und Zweck) und die WLANs des Betriebs an einer Stelle. Andere Bereiche wählen künftig daraus, statt WLAN-Namen und Kennwörter selbst zu führen.
- Handbuch: Kapitel „Netzwerk (VLANs und WLANs)“.

### Sicherheit

- Neue Rechte **Netzwerk: VLANs und WLANs pflegen** (`system.network`) und **… löschen** (`system.network_admin`, Bereichsadministrator). Pflegen dürfen automatisch alle Rollen, die die UniFi-Anbindung verwalten; löschen nur, wer zusätzlich Bereichsadministrator der Zertifikate ist.
- WLAN-Kennwörter werden verschlüsselt gespeichert und nie angezeigt.

## [0.81.3] – 2026-11-18

### Neu

- Efficio-Export per SFTP: Anmeldung jetzt auch mit **Schlüssel** statt Kennwort. Das PDH erzeugt dafür ein eigenes Schlüsselpaar; den öffentlichen Schlüssel zeigt die Seite **Dateien & Protokoll** zum Kopieren und Hinterlegen am Server. Kennwort und FTP gehen weiter wie bisher.
- Handbuch: Schritt „Schlüssel erzeugen“ im Kapitel zum Efficio-Export.

### Sicherheit

- Der private SFTP-Schlüssel wird verschlüsselt in der Verbindung gespeichert und nie angezeigt, auch nicht in Sicherungen im Klartext. Erzeugen und Ersetzen darf, wer die Verbindung bearbeiten darf (`export.write`).

## [0.81.2] – 2026-11-18

### Neu

- Anlagenterminal: Karte am Terminal anmelden. Die Terminalseite nimmt Karten vom lokalen Kartendienst des Raspberry Pi entgegen (nur gekoppelte Geräte, nicht im Bildschirmschoner) und zeigt die Leiste „Angemeldet: Name“ mit dem Knopf „Abmelden“; eine unbekannte Karte meldet „Karte nicht registriert“ für 4 Sekunden. Die Kartensitzung endet nach 2 Minuten, mit „Abmelden“ oder mit dem Bildschirmschoner. Handbuch-Abschnitt „Kartenleser und Kartensitzung“.
- Anlagenterminal: Anmeldung ohne Kartenleser. Meldet das Terminal keinen oder einen gestörten Kartenleser, gilt die Anmeldeart des Terminals (am Systembenutzer einstellbar): Kartennummer + PIN (Standard), Benutzer + Passwort, nur Kartennummer oder keine; mit Leser gibt es zusätzlich den Knopf „Mit Benutzer und Passwort“ (je Terminal abschaltbar). Die Terminal-PIN (4 bis 8 Ziffern) setzt jede Person unter „Mein Konto“; nach 5 falschen PINs ist sie 15 Minuten gesperrt (an allen Terminals). Geänderte Einstellungen übernimmt das Terminal sofort ohne Neuladen. Ältere Geräte und Terminals ohne Meldung des Lesers funktionieren unverändert.
- Anlagenterminal: Unter `/terminal` zeigt ein Wandbildschirm den Bildschirmschoner, die offenen Störungen und Tickets der Anlage samt Unteranlagen und führt über „Neu“ zur Meldeseite des Easy-Mode; Störung und Ticket öffnen vorerst beide diese Seite, Stillstand ist noch nicht verfügbar. Es läuft mit einem Systembenutzer, dessen Terminal-Standort die Anlage ist.
- Rückkehrzeit zum Bildschirmschoner: in den Core-Einstellungen für alle Terminals (Standard 120 Sekunden, erlaubt 30 bis 3600) und je Terminal am Systembenutzer; leer heißt „global“.
- HMI-Verbindungen haben die neuen Felder „Ansehen am Terminal“ (Jeder, Nur mit Karte, Niemand) und „Bedienen am Terminal“ (Niemand, Nur mit Karte, Jeder); bei ausgeschaltetem „Bedienen erlaubt“ gilt am Terminal immer „Niemand“, bei „Jeder“ warnt das Formular. Wirksam sind die Felder am Terminal erst, sobald dort HMIs angezeigt werden.
- Handbuch-Kapitel „Anlagenterminal“.
- Kiosk-Terminals: Im Reiter HMI einer Anlage öffnet „Am Terminal zeigen“ das HMI direkt auf dem Bildschirm des hmi-kiosk-Terminals am Bedienplatz, „Am Terminal bedienen“ mit Tastatur und Maus (nur wo Bedienen erlaubt ist); ist das Terminal belegt, nennt PDH das laufende HMI und seit wann.
- Neue Verwaltung „Kiosk-Terminals“ (Core-Einstellungen und Navigation): Terminals mit Adresse, Schlüssel und Fingerabdruck anlegen, ändern, löschen und mit „Verbindung prüfen“ testen; in den Stammdaten einer Anlage wählst du ihr Kiosk-Terminal.
- Befehle an ein Terminal stehen unter „Letzte Zugriffe“ des HMI mit dem Namen des Terminals.
- Ist das Terminal gerade nicht bereit, zeigt PDH den Grund, den das Terminal nennt (z. B. Wartung, Sicherung oder HMI vom Terminal aus nicht erreichbar).
- Handbuch-Kapitel „Kiosk-Terminals“, auch zum PDH-Terminalmodus (Web-Kachel mit angemeldetem Systembenutzer am Terminal).
- Terminal koppeln: Am Systembenutzer erzeugt „Gerät koppeln“ in der Karte „Gekoppelte Geräte“ einen Code (XXXX-XXXX, 10 Minuten gültig, nur einmal verwendbar). Am PDH-Terminal gibst du ihn auf der Admin-Seite unter „PDH“ ein; danach meldet sich das Terminal bei jedem Start selbst an.
- Gekoppelte Geräte: Die Karte am Systembenutzer zeigt jedes Gerät mit Status „aktiv“ oder „gesperrt“ und dem letzten Kontakt; „Sperren“ sperrt ein Gerät nach Rückfrage.
- Ein neuer Kopplungscode ersetzt den noch offenen Code des Benutzers.
- Die Wartungssperre meldet gekoppelte Terminals nicht ab; ein gesperrtes oder entkoppeltes Terminal sieht eine Fehlerseite statt einer Weiterleitung.

### Geändert

- Anlagenterminal: Störungen und Tickets zeigt das Terminal nur, wenn der Terminal-Benutzer „Störungen ansehen“ bzw. „Tickets ansehen“ für die Abteilung der Anlage hat; sonst erscheint an der Stelle ein Hinweis, ohne beide Rechte „Keine Berechtigung“.
- Anlagenterminal: Ist der Easy-Mode ausgeschaltet, sind „Störung“ und „Ticket“ unter „Neu“ abgeblendet und das Terminal erklärt es, statt zur Anmeldeseite zu führen.
- Die Karte „Anlagenterminal: Rückkehrzeit“ in den Core-Einstellungen sehen nur Personen, die sie auch speichern dürfen (Benutzer verwalten).
- Kartennummern (RFID) werden ohne Rücksicht auf Groß- und Kleinschreibung und Leerzeichen am Rand verglichen: Ein gespeichertes `deadbeef` passt zu einer gelesenen `DEADBEEF`.

### Behoben

- HMI speichern: Fehlen „Ansehen/Bedienen am Terminal“ in einer Anfrage (z. B. älteres Formular), bleiben die gespeicherten Werte erhalten, statt auf die Standardwerte zurückzufallen.
- Rückkehrzeit des Terminals: Datenbankfehler beim Speichern werden gemeldet, statt „gespeichert“ zu zeigen.

### Sicherheit

- Karte am Terminal: höchstens 10 unbekannte Karten je Minute und Terminal; danach antwortet PDH mit „Zu viele unbekannte Karten“ (429).
- Neues Recht „HMI am Kiosk-Terminal der Anlage zeigen“ (`hmi.kiosk`) für Administratoren, Manager, Techniker und Anlagenverantwortliche; es gilt je Anlage, bei Rechten auf Abteilungsebene also nur für die Anlagen dieser Abteilung.
- HMI-Verbindungen anlegen und ändern verlangt „HMI-Verbindungen einrichten“ jetzt für die Abteilung der Anlage (bisher genügte das Recht in irgendeiner Abteilung).
- Terminals einrichten, ändern, prüfen und löschen nur mit „HMI-Verbindungen einrichten“ ohne Abteilungsbegrenzung (standardmäßig Administratoren).
- Ändert sich Adresse oder Fingerabdruck eines Terminals, verwirft PDH den gespeicherten Schlüssel und verlangt einen neuen – Schlüssel und VNC-Passwörter lassen sich so nicht auf ein anderes Gerät umlenken.
- An ein Terminal geht immer nur ein Befehl gleichzeitig; Befehle für archivierte Anlagen werden abgewiesen.
- PDH vertraut einem Terminal nur, wenn der Fingerabdruck seines Zertifikatsschlüssels stimmt; Schlüssel und VNC-Passwort werden verschlüsselt gespeichert, nie angezeigt und nie protokolliert.
- Terminal-Kopplung: Kopplungscodes und Geräte-Geheimnisse werden nur als SHA-256-Hash gespeichert, nie im Klartext.
- Nach 5 Fehlversuchen innerhalb von 5 Minuten je Quelle sind Kopplung und Geräteanmeldung für 5 Minuten gesperrt.
- Ein gesperrtes Gerät wird innerhalb von 30 Sekunden abgemeldet.
- Geräte-Zugänge werden von der JSON-Schnittstelle `/api/v1` abgewiesen.
- Die Geräte-Anmeldung liegt als HttpOnly-Cookie vor; Seiten-Skripte können sie nicht lesen.
- Gesperrte Geräte können Stammdaten nicht mehr zur Löschung vormerken.

## [0.81.1] – 2026-11-18

### Behoben

- Wartung: Ein Auftrag oder Plan, der heute fällig ist, wird nicht mehr schon ab 01:00 Uhr (im Sommer ab 02:00 Uhr) als „überfällig“ angezeigt.
- Leitstand und Übersichten im Browser: „Überfällig“ und „heute“ richten sich nach dem Datum vor Ort – bisher galt ein gestern fälliger Vorgang den ganzen Tag noch nicht als überfällig.
- Neu anlegen: Die Schnellwahl der Fälligkeit („heute“, „morgen“ …) und der Beginn einer Aufgabe nehmen kurz nach Mitternacht nicht mehr den Vortag.
- Anlagen-Verlauf und Zeitstrahl: Zeitraum-Knöpfe und der Sammler „Erledigt“ nutzen das Datum vor Ort statt UTC.
- Outlook-Kalender: Schichten erscheinen zur richtigen Uhrzeit (bisher 1–2 Stunden zu spät), Nachtschichten auch über die Zeitumstellung; schon eingetragene Schichten korrigiert der nächste Abgleich.
- Microsoft-Konto: „Letzter Sync“ zeigt die Ortszeit statt UTC.
- KVP-Auswertung: Der Zwölf-Monats-Verlauf hängt nicht mehr davon ab, dass Programm und Datenbank dieselbe Zeitzone haben.
- Datensicherung: Bei der Umstellung auf Winterzeit läuft ein Zeitplan um 02:xx nicht mehr doppelt; zwei Sicherungen derselben Uhrzeit überschreiben sich nicht mehr.

### Geändert

- Datensicherung: Neue Zeitpläne starten mit 03:00 Uhr statt 02:00 Uhr; das Formular weist darauf hin, dass eine Sicherung zwischen 02:00 und 02:59 im März ausfällt.

## [0.81.0] – 2026-11-18

### Neu

- Neu anlegen: Mit dem Recht „Rüstzeiten: erfassen …“ gibt es im Assistenten die Kachel **Rüstzeit** – Grund, Anlage und Beginn Schritt für Schritt wählen; ohne Ende läuft die Rüstzeit, mit Ende oder Dauer wird sie nachgetragen.
- Neu anlegen: Auf der Seite ohne Assistent führt der Knopf **Rüstzeit** zur Seite „Rüstzeit anlegen“, mit der Anlage der QR-Infoseite vorbelegt.
- Handbuch: „So legst du eine Rüstzeit über „Neu anlegen“ an“.

## [0.80.25] – 2026-11-18

### Geändert

- Netz-Abgleich: Sehen UniFi und Omada denselben Client (gleiche MAC-Adresse), steht er nur noch einmal in der Liste der Vorschläge. **Ausblenden** und **Zurückholen** gelten für den Client, egal aus welcher Quelle er stammt.
- Netz-Abgleich: Vorschläge einer ausgeschalteten Quelle erscheinen nicht mehr in der Liste und zählen nicht mehr mit. Sie bleiben gespeichert und sind wieder da, sobald die Quelle eingeschaltet wird.

### Behoben

- Eingebauter MQTT-Broker: War der TLS-Port schon von einem anderen Programm belegt, startete der ganze Broker nicht. Jetzt laufen die übrigen Ports weiter; die Prüfliste nennt in der Zeile **TLS-Port (mqtts)** den Grund.
- Broker-Status: Nach einem Knopf der Prüfliste konnte die Meldung eines anderen Eintrags erscheinen (z. B. der alte Fehler eines anderen Zertifikats). Jetzt zeigt die Prüfliste nur das Ergebnis der eigenen Aktion.
- Omada: Sonderzeichen im Standortnamen konnten die Chat-Meldung „Omada-Anbindung geändert“ verunstalten. Die Meldung nennt den Namen jetzt ohne diese Zeichen.

## [0.80.24] – 2026-11-17

### Behoben

- Während eines Updates konnte eine Seite kurz „Template-Fehler … no such template“ zeigen (z. B. die Server-Einstellungen), weil die neue Seite schon da war, das Programm aber noch nicht neu gestartet. Jetzt steht dort „Das PDH wird gerade aktualisiert“, und die Seite lädt von selbst neu.

## [0.80.23] – 2026-11-17

### Neu

- Eingebauter MQTT-Broker: Das PDH öffnet den MQTT-Port und den TLS-Port (z. B. 1883 und 8883) bei jedem Start des Brokers selbst in der Firewall des Servers (ufw) – Geräte im Haus erreichen den Broker ohne Handarbeit am Server.
- Broker-Status: Die Prüfliste zeigt die neue Zeile **Firewall am Server** mit dem Knopf **Ports öffnen**. Kann das PDH die Firewall nicht ändern (kein Update-Agent, Docker-Betrieb, Fehler), steht dort der Befehl für den Server.
- Broker-Status: Die Prüfliste gibt es jetzt auch für einen Broker ohne eigenen Namen – dort mit der Zeile zur Firewall.
- Handbuch: „So geht's: Ports in der Firewall öffnen“.

### Behoben

- Broker-Status: „Name im Haus“ meldete „zeigt im Haus auf Cloudflare“, obwohl der Name an der UDM Pro richtig eingetragen war – wenn der Server selbst zusätzlich einen DNS außerhalb des Hauses fragt. Jetzt zählt der Eintrag an der UDM Pro.
- Broker-Status: Zeigt der Name über IPv6 weiter nach außen, steht das als Hinweis bei „Name im Haus“ – Geräte, die IPv6 bevorzugen, erreichen mqtt und mqtts sonst nicht.

### Sicherheit

- Wer eine Import-Verbindung bearbeiten darf (`import.write`), lässt damit die Ports des eingebauten Brokers in der Firewall des Servers öffnen. Geöffnet werden nur TCP-Ports ab 1024 und nie die von Datenbank, Ausfallschutz oder Update-Agent; ins Internet wird nichts freigegeben, und das PDH schließt oder ändert keine anderen Regeln.

## [0.80.22] – 2026-11-17

### Neu

- IT-Assets → Netz-Abgleich: **SNMP v3** ist die dritte Quelle neben UniFi und Omada – Geräte ohne Controller (Switches anderer Hersteller, Drucker, USV, NAS) werden einzeln gefragt und IT-Assets mit Modell, Firmware, MAC, Zustand und Anschlüssen.
- Server-Einstellungen: neuer Reiter **SNMP** für Zugangsprofile (Benutzer, Verfahren, zwei Kennwörter); ein Profil gilt für viele Geräte.
- Seite Netz-Abgleich: neue Karte **SNMP-Geräte** mit Adresse, Port, Profil, Zustand und letztem Fehler; **Prüfen** zeigt sofort, was ein Gerät antwortet, oder den Grund in Klartext.
- Schutz vor falschem Alarm: Lehnt ein Gerät den Zugang ab, gilt es als „unbekannt“ statt offline; antwortet keines von mindestens 3 Geräten, entsteht kein Ausfall-Ticket.
- SNMP: Ein Gerät gilt erst als „offline“, wenn es dem gerade fragenden Server schon einmal geantwortet hat – nach einer Umschaltung im Verbund, einem Neustart oder einer längeren Pause entstehen so keine Tickets für Geräte, die den Server nur nicht freigeschaltet haben.
- SNMP: Antwortet länger als die Karenz kein einziges Gerät, bekommen die Verantwortlichen des Netz-Abgleichs eine Nachricht statt vieler Tickets.
- SNMP: Kann der PDH-Server selbst nicht senden (z. B. kein Weg ins Netz), gelten die Geräte als „unbekannt“ statt offline.
- SNMP: Reicht die Zeit eines Laufs nicht für alle Geräte, behalten die nicht gefragten ihren Zustand, und der nächste Lauf beginnt bei ihnen.
- SNMP-Geräteliste: Hinweis mit Link, wenn das IT-Asset eines Geräts vom Abgleich getrennt ist; doppelte Einträge desselben Geräts ergeben kein zweites Asset; konnte gar kein Gerät gefragt werden, steht das an der Quelle.
- SNMP: Die MAC-Adresse eines Geräts ohne Bridge-Adresse stammt vom ersten echten Netzwerkanschluss (bevorzugt einem aktiven), nicht mehr von einer virtuellen Schnittstelle.
- Hinweis: SNMP v3 ist im Labor geprüft und noch nicht an echten Switches und Druckern erprobt.
- Handbuch: „So bindest du ein Gerät per SNMP v3 an“ und „So legst du ein SNMP-Zugangsprofil an“.

### Geändert

- Die Seite Netz-Abgleich zeigt den Stand für drei Quellen; Vorschläge kommen weiter nur aus UniFi und Omada, SNMP liefert keine.
- Am IT-Asset erscheint die Spalte PoE der Anschluss-Tabelle nur noch, wenn das Gerät PoE meldet.

### Sicherheit

- Neues Recht „SNMP-Zugangsprofile verwalten“ (`system.snmp`): Es bekommen alle Rollen und Benutzer, die „Namen im Haus eintragen (UniFi)“ (`system.unifi`) haben – niemand wird ausgesperrt, niemand bekommt mehr als bisher.
- Neues Recht „SNMP: Zugangsprofile löschen (Bereichsadministrator)“ (`system.snmp_admin`): Nur damit lässt sich ein Zugangsprofil endgültig löschen; es bekommen nur Rollen, die schon Bereichsadministrator der Zertifikate sind und die Profile verwalten dürfen.
- Das PDH fragt Geräte nur angemeldet und verschlüsselt (SNMP v3); SNMP v1/v2c, MD5 und DES gibt es nicht, und abgefragt werden nur Adressen im eigenen Netz.
- Die Kennwörter der Zugangsprofile liegen nur verschlüsselt in der Datenbank und werden nie wieder angezeigt oder protokolliert.
- Ein Gerät aus der SNMP-Geräteliste entfernen darf nur die IT-Administration (`it.admin`); hat das Gerät schon ein IT-Asset, zählt das Recht für dessen Abteilung. Das IT-Asset bleibt dabei bestehen.
- Handbuch: Für SNMP-Zugangsprofile werden lange, zufällige Kennwörter (mindestens 16 Zeichen) empfohlen.

## [0.80.21] – 2026-11-17

### Neu

- IT-Assets → Netz-Abgleich: **Omada (TP-Link)** ist die zweite Quelle neben UniFi – Switches, Access Points und Gateways aus dem Omada-Controller werden IT-Assets mit Hersteller TP-Link, verbundene Geräte erscheinen als Vorschläge.
- Server-Einstellungen: neuer Reiter **Omada** für Adresse, Omada-ID, Client-ID, Client-Secret und Standort des Controllers, mit Bestätigung des Fingerabdrucks und dem Knopf **Verbindung prüfen**.
- Hinweis: Die Omada-Anbindung ist nach der Dokumentation des Herstellers gebaut und noch nicht an einem echten Controller erprobt.
- Handbuch: „So bindest du einen Omada-Controller an“ und „Omada als zweite Quelle“ im Abschnitt Netz-Abgleich.

### Geändert

- Die Seite Netz-Abgleich zeigt den Stand je Quelle (UniFi, Omada) und hat in den Einstellungen einen Schalter je Quelle; „Jetzt abgleichen“ läuft über alle eingeschalteten Quellen.
- Die Liste der Vorschläge gilt für beide Quellen und hat die neue Spalte „Quelle“; ein Gerät, das schon als Asset aus der anderen Quelle bekannt ist, wird nicht noch einmal vorgeschlagen.
- Bei Omada-Geräten gibt es keine Anschluss-Tabelle und keinen Hinweis „Update verfügbar“; ein unbekannter Zustand löst nie ein Ausfall-Ticket aus.

### Sicherheit

- Neues Recht „Omada-Controller anbinden“ (`system.omada`): Es bekommen alle Rollen und Benutzer, die „Namen im Haus eintragen (UniFi)“ (`system.unifi`) haben – niemand wird ausgesperrt, niemand bekommt mehr als bisher.
- Das Omada-Client-Secret liegt verschlüsselt in der Datenbank und wird nie angezeigt, gemeldet oder protokolliert.
- Das PDH verbindet sich nur mit einem Omada-Controller im Hausnetz (https, private Adresse) und nur, wenn er den bestätigten Fingerabdruck zeigt; es liest nur und ändert am Controller nichts.
- Eine geänderte Controller-Adresse verlangt das Client-Secret neu und löscht den bestätigten Fingerabdruck.

## [0.80.20] – 2026-11-17

### Neu

- MQTT: Der integrierte Broker kann einen **eigenen Namen** bekommen (z. B. `mqtt.firma.de`, Feld „Name des Brokers“ an der Verbindung) – Geräte und Apps tragen den Namen ein statt einer IP-Adresse.
- MQTT: Mit Name gibt es einen zusätzlichen **TLS-Port (mqtts, z. B. 8883)**; das Zertifikat kommt aus der Zertifikatsliste und gilt nach einer Erneuerung ohne Neustart. Der bisherige Port ohne TLS läuft unverändert weiter.
- MQTT: Apps und Dienste erreichen den Broker unter `wss://name/` (Port 443) – im Haus und von außen mit derselben Adresse.
- Der Broker-Status zeigt eine **Prüfliste** (Zertifikat, Name im Haus, Tunnel, TLS-Port, WebSocket über 443) mit Knöpfen zum Einrichten.
- Server-Einstellungen → Cloudflare-Tunnel: Die Tabelle der Tunnel-Ziele hat eine Zeile **MQTT** für jeden Broker mit eigenem Namen.
- Handbuch: neues Kapitel „MQTT-Broker unter eigenem Namen“.

### Geändert

- Der Broker-Status nennt die fertigen Adressen (`mqtt://`, `mqtts://`, `wss://`) zum Kopieren, wenn der Broker einen Namen hat.
- TIA-Baustein: Hat der Broker einen Namen, ist er als Adresse vorbelegt; mit dem Haken „TLS“ setzt die Seite den Namen und den TLS-Port ein.

### Behoben

- IT-Assets: Auf schmalen Bildschirmen (Handy) sind die Knöpfe im Kopf der Asset-Seite wieder ganz sichtbar; sie rutschen in eine zweite Zeile.
- IT-Assets: Der Hinweis im Feld „Eigene Karenz“ ist ganz lesbar.

## [0.80.19] – 2026-11-16

### Neu

- IT-Assets: Die neue Seite **IT-Kategorien** (Knopf „Kategorien“ auf der IT-Seite) lässt dich Kategorien mit Name, Symbol und Reihenfolge anlegen, bearbeiten, aus- und einblenden und – wenn keine Assets darin liegen – löschen; die bisherigen sieben Arten sind jetzt Kategorien.
- IT-Assets: Jede Kategorie hat eine **Karenz** bis zum Ausfall-Ticket; leer gilt der allgemeine Wert aus dem Netz-Abgleich.
- IT-Assets: Auf der Karte „Netz“ lässt sich eine **eigene Karenz** je Gerät eintragen; es gilt erst der Wert am Gerät, dann der der Kategorie, dann der allgemeine.
- Das Ausfall-Ticket „Netzgerät offline“ nennt die wirksame Karenz („Karenz: N min“).

### Geändert

- Netz-Abgleich: Ob ein neu übernommenes Gerät überwacht wird, steht jetzt in der Kategorie („Neu übernommene Geräte überwachen“); die Vorgaben bleiben wie bisher (Workstation, Telefon und Tablet aus, alle anderen an).
- Netz-Abgleich: Beim Übernehmen stehen alle aktiven Kategorien außer Netzwerk zur Wahl.
- Listen, Filter und Formulare der IT-Assets nutzen die Kategorien; ausgeblendete Kategorien bleiben an Assets sichtbar, die sie schon haben.
- Lieferanten und Hersteller zeigen bei IT-Assets den Namen der Kategorie statt eines internen Schlüssels.

### Behoben

- Sicherung: Eine ältere Sicherung lässt sich auch dann wiederherstellen, wenn ein Update inzwischen eine Verknüpfung in der Datenbank angelegt hat; die Wiederherstellung bricht nicht mehr ab.
- IT-Assets: Verknüpfte IT-Assets zeigen ohne Hostnamen den Namen der Kategorie statt eines internen Schlüssels.

### Sicherheit

- Neues Recht „IT-Kategorien pflegen“ (`it.categories`); es haben automatisch alle Rollen mit „IT: Bereichsadministrator“ (`it.admin`), mit deren Abteilung. Die Seite ansehen darf, wer IT-Infrastruktur ansehen darf.
- Eine IT-Kategorie endgültig löschen darf nur, wer „IT: Bereichsadministrator“ (`it.admin`) hat – wie bei allen anderen Bereichen.

## [0.80.18] – 2026-11-16

### Behoben

- IT-Assets: Die Port-Tabelle der Karte „Netz“ zeigt Zustand und PoE jetzt auf Deutsch („aktiv“, „getrennt“, „an“, „aus“) statt „UP“/„DOWN“.

## [0.80.17] – 2026-11-16

### Neu

- IT-Assets: Die neue Seite **Netz-Abgleich** liest Switches, Access Points und Gateways aus UniFi und legt sie als IT-Assets an; IP, Modell, Firmware, Zustand, Uplink und Ports bleiben automatisch aktuell.
- Clients aus dem Netz erscheinen als Vorschläge, die du als Arbeitsplatz, Drucker, Telefon, Tablet, Server oder Sonstiges übernehmen oder ausblenden kannst.
- Ist ein überwachtes Netzgerät länger als die eingestellte **Karenz bis zum Alarm** offline, entsteht das Alarm-Ticket „Netzgerät offline“; beim Lösen ist die Ursache Pflicht.
- Die IT-Liste zeigt den Zustand als Marke und hat den Filter „Nur offline“; auf dem Gerät gibt es die Karte „Netz“ mit Port-Tabelle und die Knöpfe „Quelle trennen“ und „Wieder verbinden“.

### Sicherheit

- Neues Recht „Netz-Abgleich einrichten und Vorschläge übernehmen“ (`it.netsync`); es haben automatisch alle Rollen, die IT-Assets bearbeiten dürfen; die Bereichsadministratoren IT haben es über `it.admin`.

## [0.80.16] – 2026-11-15

### Behoben

- Startseite: Die Widgets werden beim Öffnen nur noch einmal geladen statt manchmal doppelt; das spart Anfragen an den Server und beseitigt eine Fehlermeldung in der Browser-Konsole.

## [0.80.15] – 2026-11-15

### Neu

- Energiedaten: An jeder Messstelle lassen sich **Schwellwerte** festlegen (über oder unter einem Grenzwert, mit Zeitraum, Rückschaltzeit, Tag, Priorität und Verantwortlichem). Das PDH wertet je Messart aus (Mittelwert, Menge, Summe) und prüft nur vollständige Zeiträume.
- Bei einer Überschreitung entsteht **ein Alarm-Ticket** mit Tag, Anlage und Priorität; die Verantwortlichen bekommen eine Nachricht im Chat und per Mail. Der Alarm endet erst nach der Rückschaltzeit am Stück. Wird die Grenze bei offenem Ticket erneut überschritten, kommt ein Eintrag „Erneut überschritten …“ statt eines neuen Tickets.
- Die Messstellen-Übersicht hat die Spalte **Schwellwerte** (Anzahl · Alarme) und den Filter „mit offenem Alarm“.

### Behoben

- Ticketlisten funktionieren auch bei Tickets ohne Beschreibung.

### Sicherheit

- Neues Recht **Schwellwerte pflegen** (`energy.thresholds`); alle Rollen mit „Messstellen pflegen“ haben es. Schwellwerte löschen darf nur der Bereichsadministrator.
- Alarm-Tickets lassen sich auf keinem Weg lösen, schließen, archivieren oder verwerfen, ohne dass die Ursache beschrieben ist.

## [0.80.14] – 2026-11-15

### Behoben

- TIA-Baustein: Der vorgeschlagene Topic schreibt Umlaute im Namen der Verbindung jetzt aus („Prüfung“ wird zu „pruefung“) statt sie durch Bindestriche zu ersetzen.

## [0.80.13] – 2026-11-15

### Neu

- Siemens-Steuerungen: An MQTT-Verbindungen gibt es unter „Werkzeuge“ den **TIA-Baustein zum Herunterladen**. Das PDH erzeugt daraus fertige Quellen für das TIA Portal (S7-1500, S7-1200 ab Firmware 4.2) in zwei Varianten: eigenständig ohne TLS oder mit der Siemens-Bibliothek LMQTT (TLS möglich).

- Die Werte kommen aus einem eigenen Datenbaustein der Steuerung. Große Datenmengen gehen automatisch in mehreren Nachrichten mit gleichem Zeitstempel. Vor dem Herunterladen zeigt „Prüfen“, was gesendet wird und was nicht (das geht auch auf dem Spiegelserver).

- Auf Wunsch legt das PDH beim Herunterladen die Zuordnungen gleich an, dazu eine für den Heartbeat. Ein erneutes Herunterladen erzeugt keine Doppelten; nur das Heartbeat-Intervall wird übernommen (Heartbeat 0 schaltet die Überwachung ab).

- Neue Anzeige „Steuerung meldet sich nicht“, wenn der Heartbeat länger als drei Intervalle ausbleibt: als Hinweis auf der Verbindungsseite, als Marke in der Zuordnung und in der Übersicht der Zuordnungen.

- Neues Handbuch-Kapitel „Steuerung per MQTT anbinden (TIA Portal)“ mit Schritt-für-Schritt-Anleitung und Statuscodes.

### Sicherheit

- Neues Recht „Steuerungsbaustein (TIA Portal) herunterladen“ (`import.tia`, Gruppe „Daten & Schnittstellen“). Es haben alle Rollen mit „Import ansehen“; niemand wird durch das Update ausgesperrt.

- Das Kennwort der Verbindung steht nur in der Datei, wenn es ausdrücklich angekreuzt wird – und nur mit dem Recht „Import verwalten“. Das Herunterladen mit Kennwort wird im Verlauf der Verbindung festgehalten. Sonst steht ein Platzhalter in der Datei.

## [0.80.12] – 2026-11-15

### Neu

- Energiedaten: Neue Seite „Messstellen“ (Navigation → Daten). Jeder importierte Wert lässt sich in einer Tabelle einer Anlage zuweisen – mit Messstellen-ID, Einheit, Art (Zählerstand, Verbrauch, Momentanwert) und Faktor.

- Energiedaten: Berechnete Messstellen rechnen mit einer Formel aus anderen Messstellen, zum Beispiel `[GESAMT] - [LINIE-A]`. Zirkelbezüge und unbekannte Messstellen weist das PDH mit einer verständlichen Meldung ab. Ändert sich Quelle, Faktor, Art oder Formel, zählt der Sprung nicht als Verbrauch.

- Energiedaten: Das PDH legt jede Minute den Wert jeder Messstelle ab und behält die Minutenwerte 400 Tage. Jede Lesung zählt genau einmal in der Minute, in der sie ankam, auch wenn die Quelle öfter als einmal je Minute liefert. Ist die Quelle zu alt oder kein Zahlenwert, oder kam bei Verbrauch keine Lesung, gibt es für diese Minute keinen Wert – nie eine 0.

- Energiedaten: Die Übersicht über alle Messstellen aller Anlagen lässt sich nach Anlage, echt oder berechnet, exportieren, aktiv, Einheit, Art, Verbindung und Suchtext filtern; „Nur ohne aktuellen Wert“ zeigt Messstellen, deren Quelle nicht mehr liefert.

- Energiedaten: Im Reiter „Einstellungen“ der Messstellen legt der Bereichsadministrator fest, nach wie vielen Minuten ein Quellwert als veraltet gilt, wie lange Minutenwerte bleiben und welche Vorgaben neue Efficio-Exporte bekommen.

- Export: Neue Art „Efficio-CSV (Energiedaten)“ liefert die Minutenwerte der Messstellen als CSV für Berg Efficio. Die Datei wird alle 15 Minuten ergänzt (einstellbar) und enthält Zeilen im Minutentakt (einstellbar); Zeitformat, Zeitbezug (Ortszeit, UTC oder Ortszeit mit Zeitzonenangabe), Dezimalkomma, Kopfzeile, Zeichensatz, Dateilänge und Aufbewahrung lassen sich einstellen.

- Export, Efficio: Die Dateien liegen zum Abholen im PDH, auf einem Netzlaufwerk oder auf einem FTP- bzw. SFTP-Server. Dateien werden erst vollständig geschrieben und dann umbenannt, damit nie eine halbe Datei gelesen wird; was nicht ankommt, wird beim nächsten Lauf nachgelegt. Ist ein Ziel nicht erreichbar, hält das die anderen Exporte nicht auf.

- Export, Efficio: Die Seite „Dateien & Protokoll“ zeigt die erzeugten Dateien zum Herunterladen und die letzten 30 Läufe samt Lücken; „Verbindung testen“ prüft die Ablage und „Jetzt exportieren“ startet einen Lauf von Hand.

- Export, Efficio: Läuft ein Lauf ohne Messwerte, entsteht keine Datei – auch keine nur mit Kopfzeile. Fehlende Werte werden nicht erfunden, sie stehen als Hinweis im Protokoll.

### Sicherheit

- Neue Rechte „Messstellen sehen“, „Messstellen pflegen“ und „Messstellen löschen (Bereichsadministrator)“; wer bisher Export ansehen oder verwalten durfte, bekommt die ersten beiden mit gleicher Abteilung. Die Einstellungen der Energiedaten ändert nur der Bereichsadministrator.

- Efficio-Export: Das FTP-/SFTP-Kennwort wird verschlüsselt gespeichert und nie angezeigt; SFTP verbindet nur mit einem Server, dessen Fingerabdruck du bestätigt hast.

## [0.80.11] – 2026-11-14

### Neu

- Verbindungen: Neuer Reiter „Explorer“ für MQTT-, OPC-UA- und Modbus-Verbindungen. Er zeigt Werte der Maschine live (höchstens 15 Minuten je Sitzung, drei Sitzungen je Benutzer, 20 am Server) und lässt einzelne Werte testweise schreiben – nach Rückfrage und mit Eintrag im Verlauf der Verbindung. Auf dem Spiegelserver ist er nur lesend.

- Explorer, MQTT: Er zeigt die Themen als Baum mit letztem Wert, Anzahl, Zeit und Retained-Kennzeichen, stellt JSON-Werte aufbereitet dar, zeichnet bei Zahlen eine kleine Kurve, filtert mit Platzhaltern und sendet Werte testweise. Der gesendete Inhalt steht im Verlauf der Verbindung.

- Explorer, OPC UA: Knoten lassen sich schrittweise aufklappen; der Explorer zeigt Wert, Datentyp, Zeitstempel, Qualität und Zugriffsrechte, verfolgt mehrere Knoten live und schreibt testweise (nur beschreibbare Knoten). Meldet sich die Verbindung mit Benutzer und Passwort an, weist der Explorer darauf hin, dass die Verbindung ohne Verschlüsselung (Sicherheitsrichtlinie None) läuft und das Passwort im Klartext übers Netz geht.

- Explorer, Modbus TCP: Er liest Coils, Discrete Inputs, Holding und Input Register (bis 125 Register bzw. 2000 Bits je Lesen), deutet jedes Register als 16-, 32- und 64-Bit-Ganzzahl oder Gleitkommazahl in den vier gängigen Byte- und Wortreihenfolgen (ABCD, CDAB, BADC, DCBA), verfolgt den Bereich live im Takt von 1 bis 60 Sekunden und schreibt Coils und Holding Register testweise; je Verbindung sind höchstens zwei Zugriffe gleichzeitig möglich. Beim Verfolgen kommen nur geänderte Werte der gewählten Reihenfolge; die Matrix aller Reihenfolgen lädt erst der Klick auf eine Zeile. Auch abgelehnte Schreibversuche stehen im Verlauf.

### Geändert

- Zuordnungen: Im Explorer wird ein Wert mit „Als Zuordnung übernehmen“ (im Export „Als Zielfeld übernehmen“) direkt zur Zuordnung, ohne die Seite neu zu laden. Bei MQTT kann eine Zuordnung ein einzelnes JSON-Feld eines Themas sein, bei Modbus enthält sie zusätzlich die Byte- und Wortreihenfolge (z. B. CDAB bei WAGO); der Export schreibt Modbus-Werte in dieser Reihenfolge, auch als 64-Bit-Wert. Bestehende Zuordnungen bleiben unverändert gültig. Zuordnungen mit Reihenfolge (Modbus) oder JSON-Pfad (MQTT, `thema#pfad`) versteht erst Version 0.80.11 und neuer – im Verbund beide Server gleich aktualisieren.

- Zuordnungen: Bei Modbus- und OPC-UA-Verbindungen holt der Knopf „Werte jetzt aktualisieren“ im Reiter „Zuordnungen“ alle Werte sofort.

- Explorer, Modbus: Testweise geschrieben wird nur noch mit der Einheit der Verbindung; Einheit 0 (Rundruf) ist gesperrt, das Schreibformular sperrt sich bei abweichender Einheit und die Rückfrage nennt die Einheit.

- Der MQTT-Sniffer, „Namespace durchsuchen“ und „Register lesen“ sind im Explorer aufgegangen; die Werkzeuge der Verbindung führen jetzt zum Reiter „Explorer“, alte Lesezeichen leiten dorthin um.

- Bibliotheken angehoben (MQTT paho 1.5.1, OPC UA gopcua 0.9.1). Die Modbus-Bibliothek wurde gewechselt (simonvetter/modbus 1.6.4 statt goburrow); Vorschau, Export, Abfragen und Verbindungsprüfung verhalten sich unverändert, Fehlermeldungen von Geräten können aber anders lauten.

### Sicherheit

- Der Explorer belastet echte Geräte und verlangt deshalb das Recht „Import ausführen“ bzw. „Export ausführen“; Schreiben zusätzlich „Import verwalten“ bzw. „Export verwalten“. Schreiben und Senden fragen vorher nach, stehen im Verlauf der Verbindung (auch abgelehnte Versuche) und sind auf dem Spiegelserver gesperrt; Sitzungen sind auf 15 Minuten, drei je Benutzer und 20 je Server begrenzt.

## [0.80.10] – 2026-11-14

### Neu

- Datennetz: Der neue Abschnitt „Datennetz prüfen“ zeigt mit ✓, ⚠ und ✗, ob dieser Server, die anderen PDH-Server und jeder Client im Datennetz sind, die richtigen Tags tragen und ihre Ports erreichbar sind – mit Klartext und Hinweis zur Abhilfe, z. B. „Gerät trägt tag:pdh-test1 statt tag:pdh – der Client lässt es nicht herein.“
- Datennetz: Optional lässt sich ein Tailscale-API-Token hinterlegen. Dann liest die Prüfung zusätzlich die Geräteliste und die Access controls, meldet fehlende tagOwners und Freigaben und zeigt die fehlenden Zeilen zum Kopieren. Die Access controls ändert das PDH nie selbst. Das Ergebnis bleibt 30 Sekunden gespeichert.
- Datennetz: Der Knopf „Tags korrigieren“ ergänzt bei PDH-Servern tag:pdh und bei Clients tag:pdh-data, ohne andere Tags zu entfernen, und ordnet Geräte nur über ihre Adresse im Datennetz zu (nach Rückfrage, nur für den Bereichsadministrator und nicht auf dem Spiegelserver); jede Änderung steht im Ereignisprotokoll.
- Das Handbuch erklärt im Kapitel „Datennetz“, wie man das Datennetz prüft und einen API-Token anlegt, und nennt „Anmeldung EOF“ als Zeichen für falsche Tags.

### Geändert

- Der Daten-Client schreibt ins Protokoll, wenn er eine Gegenstelle ohne tag:pdh abweist („Verbindung von … abgewiesen: Tags …, erwartet tag:pdh“), höchstens einmal je Minute und Adresse.

### Sicherheit

- Der Tailscale-API-Token wird verschlüsselt gespeichert, nie angezeigt und nie im Protokoll genannt; der Zugriff auf Tailscale geschieht nur auf Knopfdruck und mit Zeitgrenzen. Tags korrigieren braucht das Recht „Datennetz: Clients löschen (Bereichsadministrator)“.

## [0.80.9] – 2026-11-14

### Behoben

- Updates auf Servern scheiterten beim Nachladen der neuen Go-Version (fehlendes GOPATH); das Update setzt es jetzt selbst und zeigt Fehler verständlich an. Wer 0.80.8 einspielen will und hängen bleibt: einmalig `GOPATH=/var/cache/pdh-updater/go` in /opt/pdh-server/.env eintragen und den Update-Agenten neu starten.

## [0.80.8] – 2026-11-14

### Neu

- Datennetz: Der Daten-Client läuft am Standort neben der Fremddatenbank (z. B. SQL Server oder MySQL) und verbindet sie über ein privates Netz (Tailscale) mit den PDH-Servern, ohne dass dafür ein Port ins Internet geöffnet wird. Server und Client finden sich über das Netz von tailscale.com, die Daten selbst laufen direkt zwischen beiden; Betriebsprotokolle werden nicht an Tailscale gesendet.
- Neues Programm „Daten-Client“ für Windows und Linux: Es wird auf dem Rechner am Standort der Fremddatenbank mit einem Befehl eingerichtet und läuft danach im Hintergrund. Es kennt alle PDH-Server und meldet sich nach einem Rollentausch von selbst beim neuen aktiven Server.
- Neuer Verbindungsweg „Datennetz“: Eine Verbindung (SQL Server oder MySQL) läuft statt über einen Instanznamen über einen festen Port eines Daten-Clients im Datennetz; MySQL-Verbindungen haben jetzt auch die Wege VPN und Datennetz.
- Neue Seite „Datennetz“ mit den Clients, der Freigabe-Zeile für das Tailnet, dem Sperren und Entfernen von Clients und den Downloads des Daten-Clients.
- Das Handbuch erklärt im Kapitel „Datennetz“ Einschalten, Einrichten eines Clients, Umstellen einer Verbindung und Sperren.

### Geändert

- Das PDH baut jetzt mit Go 1.27 und aktuellen Bibliotheken. Beim nächsten Update lädt der Server dafür einmalig die neue Go-Version (Download unter 100 MB, entpackt etwa 250 MB) aus dem Internet nach; ohne Internetzugang bricht das Update ab und die bisherige Version bleibt in Betrieb.
- Die Programme des Daten-Clients stehen erst nach dem zweiten Update zum Download bereit: Das erste Update bringt den Bau mit, danach liegen die Dateien auf dem Server.

### Sicherheit

- Das Datennetz hat eigene Rechte; wer es bisher stillschweigend verwalten durfte (Administratoren), bekommt sie, alle anderen nicht.
- Der Zugangsschlüssel (Token) eines Clients wird nur einmal angezeigt und nur als Prüfwert (Hash) gespeichert.
- Über das Datennetz sind nur feste, freigegebene Ports erreichbar, keine beliebigen Ziele.
- Beim Sperren eines Clients wird seine Verbindung sofort getrennt.

## [0.80.7] – 2026-11-14

### Behoben

- Im Chat lassen sich Nachrichten jetzt auch am Handy (Nachricht antippen) und im Chat-Reiter der rechten Seitenleiste löschen, jeweils mit Rückfrage.

## [0.80.6] – 2026-11-14

### Neu

- Server-Einstellungen, Reiter Cloudflare-Tunnel: Die neue Karte „Client-Zertifikate bei Cloudflare (mTLS)“ zeigt je Name, ob Cloudflare dafür ein Client-Zertifikat verlangt, und kennzeichnet die Namen von PDH und Nextcloud.
- Mit „Abschalten“ nimmt das PDH nur diesen einen Namen aus der mTLS-Liste von Cloudflare; „Einschalten“ macht es rückgängig. Damit hört die Nextcloud-App auf dem Handy auf, über das Internet nach einem Zertifikat zu fragen.
- Die Karte zeigt auch die WAF-Regeln mit Zertifikatsprüfung und weist nach dem Abschalten auf aktive Regeln hin, die im Cloudflare-Dashboard zu prüfen sind; Regeln ändert das PDH nie.
- Das Handbuch erklärt unter „So behebst du: Nextcloud-App fragt über das Internet nach einem Zertifikat“ Ursache und Abhilfe.

### Sicherheit

- Das Abschalten von mTLS bei Cloudflare braucht das Recht „Cloudflare verwalten“ und geht als Nachricht an alle Administratoren; ohne mTLS schützt von außen die Anmeldung, im Firmennetz weiter die Zertifikatspflicht von nginx.

## [0.80.5] – 2026-11-14

### Sicherheit

- Easy-Modus: Die Bremse gegen zu viele Meldungen von derselben Adresse lässt sich nicht mehr mit einer erfundenen Absenderangabe (X-Forwarded-For) umgehen; weitergereichte Adressen zählen nur noch, wenn die Anfrage über den eigenen Server (Cloudflare-Tunnel) kommt.

## [0.80.4] – 2026-11-14

### Neu

- Mail-Brücke: Nextcloud verschickt seine Mails (neues Konto, Freigaben, „Passwort vergessen“, Benachrichtigungen) jetzt immer über das PDH – auch wenn das PDH über Microsoft 365 sendet, was Nextcloud selbst nicht kann.
- Ändert sich der Versandweg im PDH (SMTP oder Microsoft 365), gilt das sofort auch für Nextcloud, ohne Neustart.
- In den Server-Einstellungen unter Nextcloud zeigt die neue Karte „Mailversand über das PDH“, ob die Brücke läuft, wann Nextcloud angeschlossen wurde, wann die letzte Mail wegging und was zuletzt schiefging; ein Knopf schließt Nextcloud an, das PDH tut es nach dem Start auch von selbst.
- Der SMTP-Versand des PDH funktioniert jetzt mit jedem Anbieter: Verschlüsselung per STARTTLS, SSL/TLS oder gar nicht, mit und ohne Anmeldung, und der übliche Port richtet sich nach der Verschlüsselung (587, 465, 25).

### Geändert

- Der SMTP-Port hat keinen festen Standard mehr: Ist keiner eingetragen, gilt der übliche zur Verschlüsselung (STARTTLS 587, SSL/TLS 465, ohne 25); ein eingetragener Port bleibt, wie er ist.
- Beim SMTP-Versand ohne Verschlüsselungspflicht nutzt das PDH STARTTLS trotzdem, wenn der Server es anbietet, und meldet sich nur an, wenn der Server eine Anmeldung anbietet.

### Behoben

- Apps lassen sich in Nextcloud 35 über die Oberfläche installieren: Das Installationsskript liefert `.mjs`-Dateien als JavaScript aus und setzt den Sicherheitskopf `Strict-Transport-Security`.

### Sicherheit

- Die Mail-Brücke nimmt nur Verbindungen von diesem Server an (127.0.0.1), verlangt eine Anmeldung mit zufälligem, verschlüsselt gespeichertem Passwort und begrenzt Mails auf 25 MB, 50 Empfänger und 300 je Stunde.
- Das Passwort für Nextcloud geht nicht über Prozess-Argumente oder Protokolle an den Update-Agenten; Fehlertexte von Microsoft stehen nur im Server-Protokoll.
- Mail-Brücke: Verdeckte Empfänger (Bcc) gehen nie an die anderen Empfänger; es bleibt genau ein Absender (PDH), weitere From-, Sender- und Resent-Zeilen entfallen; über Microsoft 365 müssen alle Empfänger der Kopfzeilen auch im Umschlag stehen (Grenze von 50 Empfängern).
- Mail-Brücke: Nach einer falschen Anmeldung wartet das PDH eine Sekunde und trennt nach drei Fehlversuchen; das Protokoll warnt höchstens einmal je Minute. Ist der Port der Brücke von einem anderen Programm belegt, steht das im Fehlerprotokoll und auf der Karte.

## [0.80.3] – 2026-11-14

### Neu

- Rüstzeiten: Umrüsten, Reinigen und Formatwechsel sind jetzt ein eigener Vorgang (RZ-0001, …) mit Liste und Detailseite unter „Rüstzeiten“ und den Status Rüsten, Beendet und Störung durch Rüsten.
- Rüstzeiten: An der Anlage und im Easy-Modus starten Rüsttasten mit einem Tippen die Rüstzeit, „Fertig“ beendet sie.
- Rüstzeiten: Rüstgründe und Rüsttasten werden im Katalog unter Infrastruktur gepflegt und den Anlagen zugeordnet.
- Rüstzeiten: In den Core-Einstellungen gelten die maximale Rüstzeit (Standard 120 Minuten) und die Karenzzeit (Standard 15 Minuten); jede Anlage darf eine kürzere Höchstdauer haben, bei einem zu hohen Wert erscheint ein Hinweis.
- Rüstzeiten: Läuft das Rüsten länger als Höchstdauer plus Karenzzeit, entsteht genau eine Störmeldung „Störung durch Rüsten“, die Anlage gilt als stehend und die Störungsrichtlinie greift.
- Zertifikate: Die Zertifikatsseite und die Karte „Zertifikat für die Nextcloud-App“ zeigen jetzt, ob Nextcloud überhaupt Zertifikate abfragt („Zertifikatspflicht aktiv“, „nicht eingerichtet“ mit dem fertigen Befehl, „nicht prüfbar“); bei „Server (Nextcloud)“ erklärt ein Hinweis, wann das Let's-Encrypt-Zertifikat reicht.

### Sicherheit

- Neue Rechte „Rüstzeiten: ansehen“, „Rüstzeiten: erfassen, korrigieren, Katalog und Höchstdauer pflegen“ und „Rüstzeiten: Bereichsadministrator“; endgültig löschen darf nur der Bereichsadministrator, bisherige Rollen mit Anlagenrechten haben die passenden Rechte automatisch bekommen.

## [0.80.2] – 2026-11-13

### Neu

- Abgleich mit Fremdsystemen: Meldungen aus einer fremden SQL-Server-Datenbank werden im PDH zu Tickets oder Störmeldungen, Änderungen und Kommentare gehen zurück – ein Zusatz, ohne den alles unverändert läuft. Im Fremdsystem wird nie gelöscht und keine Meldung angelegt.
- Abgleich: Neuer Bereich „Abgleich“ in der Navigation mit einem Cockpit je Profil in fünf Schritten – Quelle, Ziel, Zuordnungen, Test, Betrieb.
- Abgleich: Vor dem Scharfschalten zeigt ein Trockenlauf, was entstünde, und ein Schreibversuch prüft das Zurückschreiben und nimmt es wieder zurück; hat die Tabelle Auslöser, fragt das PDH vorher nach.
- Abgleich: Meldungen von früher werden still als Historie übernommen, abgeschlossene gleich archiviert; neue Meldungen benachrichtigen wie von Hand angelegte.
- Abgleich: An übernommenen Tickets und Störmeldungen steht die Herkunft mit der Nummer im Fremdsystem und dem Stand des Abgleichs.
- Abgleich: Bei hoher Serverlast wartet der Abgleich und holt danach auf – die Benutzer haben Vorrang. Der Takt ist von 5 bis 30 Minuten einstellbar.
- Verbindungen (MSSQL): Der Weg ist „Direkt“ oder ein VPN-Tunnel mit eigenem Abfragetakt und eigener Zeitgrenze; das Serverzertifikat wird über einen einmal bestätigten Fingerabdruck geprüft.

### Geändert

- Verbindungen: Beim Bearbeiten bleibt das gespeicherte Kennwort erhalten, wenn das Kennwortfeld leer bleibt.
- Verbindungen: Die Prüfung nennt die Absenderadresse des Servers und zeigt Tunnel, Adresse, Port, Anmeldung, Lese- und Schreibrecht einzeln.

### Sicherheit

- Kennwörter und Schlüssel von Verbindungen werden verschlüsselt gespeichert und nicht mehr an den Browser gegeben; vorhandene werden beim Start einmalig verschlüsselt.
- In eine fremde Datenbank schreibt das PDH nur, wenn an der Verbindung „Schreiben erlaubt“ eingeschaltet ist.
- Neue Rechte für den Abgleich (sehen, einrichten und testen, scharf schalten, Bereichsadministrator); sie gelten überall oder nur in einer Abteilung. Rollen, die Import bearbeiten dürfen, bekommen sie automatisch; Profile löscht nur der Bereichsadministrator.

## [0.80.1] – 2026-11-12

### Neu

- Ausfallschutz: Der aktive Server kann zusätzlich eine IPv6-Adresse tragen, die beim Rollentausch mitwandert – Feld „IPv6-Ende“ bei der mitwandernden Adresse, ein Zusatz, ohne den alles unverändert läuft.
- Namen im Haus: Mit einer UniFi UDM Pro trägt das PDH für jeden Namen, der dort auf die mitwandernde Adresse zeigt, auch den IPv6-Eintrag ein – auch für von Hand eingetragene Namen – und hält ihn aktuell, wenn der Anbieter das Präfix wechselt. Geräte mit IPv6 gehen damit im Haus direkt zum Server.
- Namen im Haus: Ein IPv6-Eintrag, der woandershin zeigt, wird nie von selbst geändert – dafür gibt es einen eigenen Knopf „Überschreiben“ mit Rückfrage, auch für von Hand eingetragene Namen.

### Geändert

- Die nginx-Seite des PDH hört auch auf IPv6; nimmt nginx die IPv6-Zeilen nicht an, bleibt sie bei IPv4 und die Zertifikate-Karte sagt es.

## [0.80.0] – 2026-11-12

### Neu

- Zertifikate von Let's Encrypt: Im Reiter Cloudflare-Tunnel („Zertifikate laden“) holt das PDH je Name ein Zertifikat, erneuert es 30 Tage vor Ablauf und warnt rechtzeitig – ein Zusatz, ohne den alles unverändert läuft.
- Namen im Haus: Mit einer UniFi UDM Pro trägt das PDH die Namen im Firmennetz selbst ein – Adresse und API-Schlüssel stehen in den Server-Einstellungen im eigenen Reiter „UniFi“; ohne UniFi bleibt der Eintrag Typ A am DNS-Server von Hand.
- Full (strict): Ein Knopf stellt Tunnel und Domain auf geprüfte Verschlüsselung um, „Zurückstellen“ nimmt es wieder zurück.
- Im Ausfallschutz-Verbund werden die Zertifikate auf den zweiten Server gespiegelt und in die Sicherung aufgenommen; sobald Zertifikate vorhanden sind, richtet das Update nginx auf beiden Servern ein.
- Das Handbuch erklärt Zertifikate, Namen im Haus, Full (strict) und die möglichen Fehlerbilder.

### Geändert

- Ausfallschutz: Der Hinweis „Das Zertifikat von Nextcloud enthält die mitwandernde Adresse nicht“ erscheint nicht mehr, wenn Nextcloud ein vom PDH geholtes Zertifikat nutzt – dort zählt der Name, im Haus führt er über „Namen im Haus“ zum Server.

### Sicherheit

- Neue Rechte „Zertifikate verwalten (Let's Encrypt)“ (`system.certs`), „Zertifikate: entfernen und widerrufen (Bereichsadministrator)“ (`system.certs_admin`) und „Namen im Haus eintragen (UniFi)“ (`system.unifi`); Rollen mit dem Cloudflare-Recht bekommen sie mit dem Update.
- Entfernen und Widerrufen eines Zertifikats darf nur, wer das Recht `system.certs_admin` hat.
- Die privaten Schlüssel der Zertifikate und der UniFi-API-Schlüssel liegen nur verschlüsselt in der Datenbank.
- Die UniFi UDM Pro wird nur mit bestätigtem Fingerabdruck und nur über Adressen im eigenen Netz angesprochen.

## [0.77.10] – 2026-11-12

### Geändert

- Cloudflare: Meldungen, die die Karte „DNS, Proxy und Tunnel-Ziele“ anzeigt, stehen jetzt auch im Protokoll (Bereich cloudflare) – so lässt sich später nachsehen, was Cloudflare abgelehnt hat.

## [0.77.9] – 2026-11-09

### Neu
- Ausfallschutz mit zwei Servern: Die neue Datei-Spiegelung überträgt Anhänge, Chat-Dateien und die Einstellungen in `/etc/pdh` fast in Echtzeit auf den passiven Server – nach einem Tausch fehlt keine Datei mehr; Standard ist aus.
- Nextcloud im Verbund: Mit „Nextcloud mitnehmen“ werden Datenordner, Programm, nginx-Einstellungen und Zertifikate mitgespiegelt, Nextcloud ist auf beiden Servern startbereit und arbeitet immer nur auf dem aktiven – für Clients bleibt es derselbe Server.
- Das Nextcloud-Installationsskript kennt `--verbund aktiv-neu`, `--verbund aktiv-vorhanden` und `--verbund passiv` und nennt am Ende den fertigen Befehl für den anderen Server.
- Neue Karte „Datei-Spiegelung“ im Reiter Ausfallschutz mit Zustand, letztem Abgleich je Quelle, Fortschritt des Erstabgleichs, Papierkorb, Warnungen und Einstellungen; `pdh-ha status` hat eine Zeile „Spiegelung“, dazu `pdh-ha mirror status`, `pdh-ha mirror run` und `pdh-ha mirror trash-empty`.
- Beim Rollentausch läuft in der kurzen Sperre ein letzter Abgleich der Dateien (höchstens 120 Sekunden); klappt er nicht, bricht der Tausch ohne Änderung ab.
- Gelöschte und überschriebene Dateien landen am passiven Server 14 Tage lang in einem Papierkorb; „Papierkorb leeren“ leert ihn vorher.
- Am Partner wird erst gelöscht, wenn sein Stand belegt ist: Ein gefüllter Ordner wartet auf „Dieser Server ist vollständig“ (Knopf oder `pdh-ha mirror confirm`).
- Nach einem harten Ausfall nennt der neue aktive Server die Nextcloud-Dateien, die noch nicht übertragen waren, in der Karte und im Alarm.
- Jeder gespiegelte Ordner trägt eine Kennungsdatei `.pdh-mirror-id`; nach einem Plattentausch oder bei nicht eingehängtem Laufwerk löscht die Spiegelung nichts und meldet sich.
- Handbuch: neuer Abschnitt „Datei-Spiegelung und Nextcloud im Verbund“ mit Einschalten, Bestätigen, Nextcloud, Papierkorb und Fehlerbildern.
- Nextcloud verbindet sich von selbst: Liegt Nextcloud auf demselben Server, legt das PDH sein Konto „pdhNCadmin“ ohne Zutun an und trägt Adresse, Benutzer und Passwort in den Server-Einstellungen ein; alle Administratoren bekommen eine Nachricht.
- Das Nextcloud-Installationsskript richtet bei einer neuen Nextcloud gleich Kalender, Kontakte und Deck ein (`--apps` wählt andere oder keine).
- Abgleich mit Nextcloud, das PDH hat Vorrang: Jeder aktive Benutzer bekommt ein Nextcloud-Konto mit Namen, E-Mail und Sprache aus dem PDH; Rollen, Abteilungen und Gruppen werden zu Nextcloud-Gruppen, deaktivierte Personen dort gesperrt. Einschalten unter Server-Einstellungen → Nextcloud.
- Ist eine Person einem Microsoft-Konto zugeordnet und die Microsoft-Anmeldung für Nextcloud eingerichtet, bekommt ihr Nextcloud-Konto die Microsoft-E-Mail als Benutzer-ID – die Anmeldung klappt dann ohne eigenes Passwort.
- Der Abgleich richtet in Nextcloud die Apps ein, die das PDH braucht (Kalender, Kontakte, je nach Einstellung Deck, externer Speicher, Microsoft-Anmeldung); weitere Apps installieren Administratoren in Nextcloud wie gewohnt.
- Cloudflare im PDH: Mit einem API-Token lassen sich unter Server-Einstellungen → Cloudflare-Tunnel DNS-Einträge, der Proxy je Eintrag, die Verschlüsselung der Domain und die Ziele im Tunnel pflegen; für PDH und Nextcloud zeigt eine Tabelle, was fehlt, und richtet es auf Knopfdruck ein.
- Benutzerkarte: Haken „Administrator in Nextcloud“ und Knopf „Neues Startpasswort“ für das Nextcloud-Konto.
- Netzlaufwerke in Nextcloud: Mit dem Haken „in Nextcloud zeigen“ erscheint ein Laufwerk dort im Ordner „PDH-Laufwerke“; Laufwerke, die das PDH selbst benutzt, sind nur lesbar.
- Haben `pdh` (und mit „Nextcloud mitnehmen“ `www-data`) auf den beiden Servern verschiedene Benutzer- oder Gruppennummern, hält die Spiegelung die betroffenen Ordner mit einem Alarm an – auch schon beim ersten Abgleich nach dem Einschalten; Karte und `pdh-ha mirror status` nennen beide Nummern.

### Geändert
- Die Navigation zeigt Tickets, Störungen, Wartung, Aufgaben, Kalender, Ersatzteile, Lager, IT-Assets und Hersteller & Lieferanten nur noch, wenn die Person in dem Bereich wenigstens ein Recht hat.
- „Rollen tauschen“ und die Prüfung „alles grün“ vor einem Update warten, bis der Erstabgleich der Dateien fertig ist.
- Ein gescheiterter Tausch wird in zwei Schritten zurückgenommen: erst die Rolle, eine Sekunde später Datenbank, Dienste und mitwandernde Adresse.
- Scheitert die Rücknahme, bleibt die Automatik an; der Server versucht es jede Sekunde neu, bleibt solange nur lesend ohne Dienste und zeigt solange die Warnung „Rücknahme hängt“.
- Ohne Sicht auf den Partner (und im Trio auf den Zeugen) nimmt ein Server einen gescheiterten Tausch nicht zurück, sondern bleibt abgeschottet.
- Rollenskripte laufen nur noch bei einem echten Rollenwechsel, nicht beim Neustart von `pdh-ha`.
- Sicherung, Wiederaufbau und Rückspielen nehmen die Kennungsdatei `.pdh-mirror-id` nicht mit.
- Die Karte vergleicht neben der `.env` auch `/etc/pdh/pdh.env`; das Token des Update-Agenten gehört nur in die `.env`, `scripts/repair-update-agent-token.sh` nimmt es aus `pdh.env` heraus.
- Nextcloud-Daten- und Programmordner dürfen nicht unter `/etc/pdh/ha` liegen und müssen verschieden sein.

### Behoben
- Übernimmt der Partner mitten in einem Tausch, wird der alte Server automatisch wieder als passiver Server angebunden, statt ohne Datenbank stehen zu bleiben.
- Bricht das Zurückspulen der Datenbank beim Wiederanbinden ab, startet der halb zurückgespulte Stand nicht mehr unbemerkt; `pdh-ha rejoin` kopiert die Datenbank dann neu, `pdh-restore.sh` räumt die Markierung auf.
- Ein offener Update-Auftrag, den der aktive Partner nicht führt, meldet sich mit Alarm und lässt sich am passiven Server mit `pdh-ha update abort` beenden.
- „Papierkorb leeren“ meldet die Rückkehrer-Dateien nur noch als erledigt, wenn wirklich alles geleert ist, und hört nach einem Rollenwechsel auf.
- War der passive Server so lange weg, dass seine Datenbank neu kopiert werden muss, beginnt die Neukopie erst, wenn der aktive Server dafür bereit ist; sie scheitert nicht mehr gleich und wartet dann zehn Minuten.
- Der Eintrag „Nextcloud“ in der Navigation öffnet jetzt die Nextcloud-Adresse aus den Server-Einstellungen statt einer fest eingebauten Adresse; ohne eingetragene Adresse erscheint er nicht.
- Das Nextcloud-Installationsskript bricht nach der Installation nicht mehr mit „Memcache … not available“ ab; der Zwischenspeicher wird richtig eingetragen.
- Die Datei-Spiegelung erkennt den Datenordner neuerer Nextcloud-Fassungen (ab 30, Kennungsdatei `.ncdata`) und meldet ihn nicht mehr als leer.
- Die Karte „Datei-Spiegelung“ frischt sich während Erstabgleich und Abgleich selbst auf; neu laden ist nicht mehr nötig.
- Ist der zweite Server nicht erreichbar, zeigen Karte und `pdh-ha status` „wartet auf den zweiten Server“ statt „läuft“ mit einer rohen Fehlermeldung.
- Bei einem Rollentausch erscheint nicht mehr jedes Mal die Warnung „Dateien … abgelehnt: Dieser Server ist nicht passiv“; der kurze Moment der Übergabe ist nur noch ein Hinweis.
- Das Ereignisprotokoll des Verbunds verliert bei einem Stromausfall die letzten Zeilen nicht mehr, und Zeilen nach einem früheren Ausfall werden wieder gelesen.
- Kommt ein Ordner erst nach einer Übernahme neu dazu, nennt der Alarm das jetzt so („Der Ordner ist neu dazugekommen …“) statt „bevor der Erstabgleich fertig war“.
- Cloudflare: Ein API-Token, der in Cloudflare unter dem Konto angelegt ist, wird nicht mehr als „Invalid API Token (1000)“ abgelehnt.

### Sicherheit
- Vier neue Rechte (Kategorie „System & Sicherheit“): „Nextcloud-Abgleich einstellen und starten“, „Netzlaufwerke in Nextcloud nutzen“, „Netzlaufwerke in Nextcloud: auch PDH-Daten bearbeiten“ und „Cloudflare verwalten (DNS, Proxy, Tunnel)“. Mit dem Update bekommen sie die Rollen, die überall die Server-Einstellungen pflegen dürfen.
- Administrator in Nextcloud ist nur noch, wer im PDH Administrator ist und das Kennzeichen „Administrator in Nextcloud“ trägt; das Kennzeichen setzen nur Administratoren. Ohne eingeschalteten Abgleich ändert sich nichts.
- Beim Einschalten des Nextcloud-Abgleichs bekommen PDH-Administratoren, die in Nextcloud schon Administrator sind, das Kennzeichen von selbst – niemand sperrt sich aus; die Meldung nennt die Namen, die Historie der Person hält es fest.
- Neue Rechte „Datei-Spiegelung einstellen“ und „Datei-Spiegelung: Löschen am Partner freigeben und Papierkorb leeren“; Rollen, die „Server-Einstellungen (.env) bearbeiten & Server neu starten“ überall hatten, bekommen beide automatisch.
- Die Dateien laufen durch den verschlüsselten Kanal des Verbunds (Port 7480) mit den Zertifikaten der Server; der Zeuge bekommt keine Dateien.
- Der Empfänger nimmt nur Dateien an, wenn er passiv ist und sie vom aktuell aktiven Server kommen, setzt Zielpfad und Papierkorb selbst und lässt nur eine feste Liste von rsync-Optionen zu.
- `/etc/pdh/ha/`, die `.env` und `cloudflared` werden nie gespiegelt.
- „Dieser Server ist vollständig“ und „Papierkorb leeren“ lehnen Anfragen ab, die von einer fremden Seite kommen (auch von einer Nachbarseite wie Nextcloud unter derselben Domain).
- Texte vom Partner und Dateinamen erscheinen in Protokoll, Alarm, Chat und Mail ohne Steuerzeichen, Zeilentrenner und Schreibrichtungs-Zeichen.

## [0.77.8] – 2026-10-30

### Neu
- Update im Verbund: „Update installieren“ bringt beide Server und den Zeugen auf denselben Stand – wahlweise ohne Unterbrechung (mit Rollentausch) oder alle nacheinander; bei jedem Fehler setzt das System auf den bisherigen Stand zurück, auch nach Datenbank-Änderungen. Bei „alle nacheinander“ wird der Spiegel nur mit bestätigtem Gleichstand eingefroren, sonst bricht das Update ohne Änderung ab („Spiegel nicht auf Gleichstand“).
- Während eines Updates lehnt `pdh-ha` Rollentausch und Wartung von Hand ab („zuerst `pdh-ha update abort`“); nach einem Zurücksetzen macht die Ausfallschutz-Sicherung sofort eine neue Grundsicherung und legt den verworfenen Stand auf dem Laufwerk beiseite.
- Wartungsfenster im Reiter Ausfallschutz: täglich oder wöchentlich nach der Grundsicherung spielt das System automatisch die neue Version ein; fehlerhafte Versionen werden gemerkt und übersprungen, bis eine neuere kommt oder ein Administrator sie wieder zulässt.
- Wartungsbildschirm mit Fortschrittsbalken und Restzeit während der Sperre (hell oder dunkel wie das Gerät), vorher ein Band mit Countdown; Administratoren dürfen dort abbrechen und zurücksetzen, solange das Update weder zurückgesetzt wird noch schon freigegeben ist (Notbremse auf der Konsole: `pdh-ha update abort`).
- Der Zeuge zieht selbst nach, wenn beide Server auf demselben Stand sind (Container-Helfer `updater`, abschaltbar mit `PDH_HA_AUTOUPDATE=0`); der Reiter warnt „Zeuge veraltet“, falls er es nicht schafft.
- Geplanter Rollentausch im Wartungsfenster (Einstellung „Rollen im Fenster tauschen“).
- Bei „Automatik aus (Wartung)“ fragt das Wartungsfenster vor dem Update nach; ohne Bestätigung innerhalb von 3 Minuten entfällt das Update.
- Ein laufendes Update lässt sich vor der Sperre im Wartungs-Band und unter Ausfallschutz abbrechen.
- `pdh-ha update confirm` bestätigt die Nachfrage auf der Befehlszeile.

### Geändert
- Update auf dem Einzelserver läuft mit Countdown, Sperre und automatischem Rückweg auf den alten Stand, wenn die Prüfung scheitert; die Sicherung vor dem Update entsteht bei angehaltenem PDH.
- Ein Spiegel, dessen Datenbank neuer ist als sein Programm, meldet „Programm zu alt für diese Datenbank“ und sperrt sich.
- Der Befehl zum Nachziehen von Hand (`pdh-ha-join.sh --update`) ist jetzt nur noch der Notweg.
- „Update installieren“ funktioniert im Verbund auch bei „Automatik aus (Wartung)“.

### Behoben
- `pdh-ha update status` zeigt ein entfallenes oder abgesagtes Update als „nicht durchgeführt“ und nicht mehr als „abgebrochen (100 %)“, denn dabei wurde nichts geändert.
- Ein Server verliert den Kontakt zum Verbund nicht mehr, wenn das Netzwerk seine feste Adresse neu setzt: Die mitwandernde Adresse liegt jetzt als `/32` auf der Karte und wird nie Absender. Bestehende Server stellen sich im laufenden Betrieb um.
- `pdh-ha status` zeigt am Zeugen keine irreführende Zeile „Skripte: 0, anders als beim Partner“ mehr, denn der Zeuge führt keine Skripte aus.
- Das Band am Spiegel verweist auf den Reiter Ausfallschutz.
- Der Update-Zweig (`PDH_UPDATE_BRANCH`) gilt nur noch aus der `.env`: Ein in die Datenbank übernommener Wert wird nicht mehr beachtet, sonst prüfte das PDH einen anderen Zweig, als der Update-Agent einspielt.
- Core-Einstellungen zeigen bei „Version auf GitHub“ und „Branch“ den eingestellten Update-Zweig statt immer `main`.

### Sicherheit
- Neue Rechte „Wartungsfenster einstellen“ und „Automatische Wartung abbrechen“; Rollen mit den bisher zuständigen Rechten bekommen sie automatisch.
- Während der Wartung werden alle Benutzer abgemeldet, auch Systembenutzer (Terminals, Leitstand-Anzeigen); nur Inhaber des Abbruch-Rechts bleiben angemeldet.
- „Automatische Wartung abbrechen“ erlaubt jetzt auch Absagen und Abbrechen vor der Sperre; das Durchführen der Nachfrage braucht das Recht „Rollen & Berechtigungen verwalten“.

## [0.77.7] – 2026-10-29

### Behoben
- Ausfallschutz: Schaltet das Tunnel-Skript aus den Server-Einstellungen den Autostart von `cloudflared` wieder ein, schaltet `pdh-ha` ihn innerhalb von 30 Sekunden ab – sonst startete der Tunnel nach einem Neustart auch am passiven Server, und Cloudflare schickte Anfragen auch dorthin.

## [0.77.6] – 2026-10-29

### Neu
- Ausfallschutz mit zwei Servern: eine mitwandernde Adresse im LAN liegt immer auf dem aktiven Server und wechselt bei einer Umschaltung in wenigen Sekunden mit – Geräte und Lesezeichen müssen nicht mehr umgestellt werden.
- Der Cloudflare-Tunnel wird auf beiden Servern automatisch mit demselben Token eingerichtet und läuft immer nur auf dem aktiven Server.
- Eigene Skripte in `/etc/pdh/ha-hooks/` laufen bei jedem Rollenwechsel, z. B. um einen DNS-Eintrag umzustellen; testen mit `sudo pdh-ha hook run`.
- Das Skript `pdh-ha-adresse.sh` führt Schritt für Schritt durch die Umstellung einer bisherigen Server-Adresse zur mitwandernden Adresse, jeder Schritt zurücknehmbar.
- Neue Karte „Zugangsweg“ im Reiter Ausfallschutz: Zustand von Tunnel, mitwandernder Adresse und Skripten, Adresse testen und einschalten, Skripte ein- und ausschalten oder sofort ausführen.

### Geändert
- Das Update von `pdh-ha` installiert auf jedem Server – auch am ersten – die Pakete `iproute2` und `iputils-arping`; `pdh-ha-join.sh` legt zusätzlich den Ordner für die Skripte an und richtet mit `--voll` auch `cloudflared` ein.
- Am Spiegel führt der Link „Bitte den aktiven Server verwenden“ auf die mitwandernde Adresse, sobald sie eingeschaltet ist.
- Wird `pdh-ha` beendet, gibt der Server die mitwandernde Adresse bei automatischer Umschaltung „echt“ ab, damit der Partner sie bei einem Ausfall bekommt; im Beobachtungsmodus bleibt sie liegen.
- Auf einem Server ohne Datenbank-Spiegel bleibt der Autostart des Cloudflare-Tunnels, wie er ist – der Tunnel kommt nach einem Neustart weiter von selbst hoch.
- Die Karte „Zugangsweg“ meldet „Token fehlt“, wenn auf diesem oder dem anderen Server kein Tunnel-Token liegt, statt den Tunnel als bereit zu zeigen; ein Verbund ganz ohne Tunnel-Token gilt als „nicht eingerichtet“, nicht als Fehler.
- `pdh-ha-adresse.sh uebergabe` schaltet die Adresse zuerst mitwandernd ein und nimmt sie erst dann aus der festen Netzwerkkonfiguration – die Lücke dauert nur wenige Sekunden, egal wie lange die Bestätigung dauert.
- Ein Adresstest mit der eingeschalteten mitwandernden Adresse wird abgelehnt; dafür eine freie Testadresse nehmen.
- Meldungen zur mitwandernden Adresse kommen höchstens einmal je Stunde, auch wenn ein Netzwerkdienst die Adresse laufend entfernt.
- Die mitwandernde Adresse wird nach einem Wechsel auch als ARP-Antwort angekündigt: Windows-Rechner (z. B. im WLAN) erreichen den neuen Server nach etwa 10 Sekunden statt erst nach bis zu 30.
- Der Adresstest gibt jedem Teilnehmer bis zu 15 Sekunden, die Prüfadresse zu erreichen – so besteht er auch mit einem Zeugen auf einem Windows-PC, der den neuen Halter erst nach kurzer Rückfrage annimmt.

### Behoben
- Rollentausch, Update und Neustart: Das PDH beendet sich sofort, statt bis zu 30 Sekunden auf offene Browser-Seiten (Chat-Verbindung) zu warten – ein Rollentausch im Ausfallschutz dauert dadurch nur noch wenige Sekunden statt über eine halbe Minute.
- `pdh-ha-adresse.sh uebergabe`: Wird der Testlauf nicht bestätigt, wartet das Skript, bis die mitwandernde Adresse wirklich abgegeben ist, und prüft danach, dass die feste Adresse wieder anliegt; scheitert das Einschalten, bleibt auch die erzeugte Netzwerkeinstellung unverändert.

### Sicherheit
- Skripte beim Rollenwechsel laufen nur, wenn der Ordner `/etc/pdh/ha-hooks` root gehört und nicht für Gruppe oder andere schreibbar ist; Verknüpfungen im Ordner werden einmal gemeldet und nie ausgeführt.

## [0.77.5] – 2026-10-29

### Behoben
- Leitstand: eingeschaltetes Vollbild bleibt beim Weiterblättern und beim Wechsel zu einem anderen Leitstand an, bis es bewusst ausgeschaltet wird; nach einem Neustart bietet der Bildschirm „Tippen für Vollbild“ an.

## [0.77.4] – 2026-10-29

### Geändert
- Schichtplan-Widget und Leitstand-Baustein „Schichtplan“: die Namensfelder werden so breit, dass Name und Telefonnummer immer ganz zu sehen sind; reicht der Platz nicht, lässt sich der Plan seitlich schieben.

## [0.77.3] – 2026-10-29

### Neu
- Leitstand: beim Weiterblättern können Grafana-Dashboards aus der Statistik als zusätzliche Stationen gezeigt werden (Leitstand → Dashboards → nächster Leitstand), bildschirmfüllend und auch im Kioskmodus.
- In Grafana neu angelegte Dashboards stehen automatisch zur Wahl – im Leitstand-Editor und unter Core → Grafana & Statistik („Neu in Grafana“, mit einem Klick übernehmen).
- Widget und Leitstand-Baustein „Dokumente“: Bilder, PDF, Word- und Excel-Dateien direkt hochladen und anzeigen; mehrere wechseln sich ab.

### Sicherheit
- Neues Recht „Dokumente für Dashboard und Leitstand hochladen“ (für Administratoren, Manager und wer Leitstände verwaltet) und neuer Bereichsadministrator „Leitstand & Dashboards“.

## [0.77.2] – 2026-10-29

### Neu
- Leitstand-Baustein „Webseite“: eine externe Seite (oder eine PDH-Seite) direkt im Leitstand einbetten, mit einstellbarer Höhe und automatischem Neuladen.
- Direktlink zum Leitstand mit Vollbild und Kioskmodus: unter Leitstände → Bearbeiten → Direktlink den Link zusammenstellen und kopieren; der Kioskmodus zeigt nur den Inhalt ohne Kopfzeile und Knöpfe.

### Geändert
- Schichtplan-Widget und Leitstand-Baustein „Schichtplan“: die Namensfelder sind doppelt so groß, die Feldhöhen ebenfalls.

## [0.77.1] – 2026-10-29

### Neu
- Widget und Leitstand-Baustein „Schichtplan“: der Schichtplan so, wie er im Planer aussieht – Schichten × Tage mit Namen, darunter Sonderdienste und Bereitschaft; Woche, Schichtmodell und Feldhöhe oben wählen.
- Sonderdienste können die Telefonnummer der eingeteilten Person mit anzeigen (Haken „Telefon“ bei der Dienstart; Rufbereitschaft ist voreingestellt) – im Schichtplan, Kalender, Widget und Leitstand.
- Schichtplan: die Höhe der Namensfelder ist einstellbar (Klein, Normal, Groß, Sehr groß, Automatisch).
- Kalender: Kalenderwochen stehen oben, angezeigt werden immer ganze Wochen ab Montag (1 bis 6 Wochen).

### Geändert
- Kalender: Projekte stehen ganz oben – je Projekt eine Zeile mit durchgehendem Balken, ohne Zuordnung zu Personen.

## [0.77.0] – 2026-10-28

### Neu
- Das PDH läuft jetzt auch auf dem passiven Server („Spiegel“) im Lesemodus: anmelden und ansehen geht, jede Änderung wird mit einem Hinweis abgelehnt.
- Am Spiegel melden sich Administratoren an; fällt der aktive Server aus, dürfen sich alle anmelden (nur lesen), andere sehen „Bitte den aktiven Server verwenden“.
- Ein Band oben auf jeder Seite zeigt am Spiegel den Datenstand („aktuell“ oder „vor …“), den aktiven Server und – bei Ausfall – den Stand der letzten Daten.
- Übernimmt der Spiegel die Rolle, startet das PDH dort nach etwa 10 Sekunden von selbst neu und ist schreibbar; Anmeldungen bleiben gültig.
- `/health` zeigt die Betriebsart (`spiegel` oder `normal`).
- Wartet der Spiegel auf das Update des aktiven Servers, startet das PDH dort kurz nach diesem Update von selbst neu und zeigt wieder alle Seiten.

### Geändert
- `pdh-ha` startet das PDH jetzt auch auf dem passiven Server (als Spiegel), der Cloudflare-Tunnel läuft nur noch auf dem aktiven.
- Am Spiegel entfallen VPN-Schlüssel und Zertifizierungsstelle neu anlegen, Passwort vergessen oder ändern, Hintergrundaufgaben und Benachrichtigungen; reines Ansehen von Seiten schreibt dort nichts mehr.

### Behoben
- Docker-Testsystem: Der Build bricht nicht mehr wegen der eingebetteten Skripte ab.
- Der Reiter VPN zeigt Fehlermeldungen, statt einen leeren Schlüssel anzuzeigen.

## [0.76.6] – 2026-10-28

### Neu
- Schichtplan: Namen lassen sich auf einen anderen Tag oder in eine andere Schicht ziehen (Strg oder „Kopieren“ = kopieren); am rechten Rand gezogen trägt PDH die Person an jedem Tag ein – z. B. Montag bis Freitag. Urlaub und schon vergebene Schichten werden übersprungen und gemeldet.
- Sonderdienste: Bereitschaft, besondere Tätigkeiten und Zusatzaufgaben werden im Schichtplan unter den Schichten eingeteilt. Eine Dienstart kann eine Qualifikation aus dem Personalstamm verlangen – dann stehen nur Personen mit gültigem Nachweis zur Auswahl (Planer → Einstellungen → Sonderdienste, neues Recht „Sonderdienste einteilen“).
- Kalender: Schichten und Sonderdienste ziehen (auch auf eine andere Person), am Rand über weitere Tage verlängern, Urlaub und Abwesenheit am Rand verlängern oder verkürzen und die Fälligkeit von Störungen, Tickets, Aufgaben und Wartungen per Ziehen verschieben.
- Kalender zeigt die Sonderdienste als eigene Art.
- Navigation anpassen: ganze Gruppen lassen sich mit Pfeilen nach oben oder unten verschieben.
- Leitstand-Baustein „Kalender“: derselbe Kalender mit Schichten, Sonderdiensten, Abwesenheit und fälligen Vorgängen, Auswahl (Zeitraum, Abteilung, Gruppe, Person, Arten) oben im Baustein; voreingestellt auf die Abteilung bzw. Gruppe des Leitstands. Ohne Anmeldung nur lesbar, Abwesenheiten ohne Grund.

## [0.76.5] – 2026-10-28

### Neu
- Bestellvorgänge (Navigation → Bestellvorgänge): ein Vorgang je Lieferant mit mehreren Positionen verfolgt Angebot, Bestellung, Auftragsbestätigung, Lieferung und Rechnung.
- Die Auftragsbestätigung wird je Position mit der Bestellung verglichen (Menge, Preis) und ihr Liefertermin mit dem Wunschtermin; Abweichungen sind gelb markiert und stehen in der Historie.
- Lieferungen, auch Teillieferungen, werden mit der AB verglichen (Überlieferung, Rest offen, überfällig) und buchen beim „Wareneingang buchen“ direkt ins Lager; volle Kanban-Karten werden dabei zurückgesetzt.
- „Rechnung OK“ schließt den Vorgang – auch ohne hinterlegte Rechnung, weil die Rechnung in ELO bearbeitet wird; eine Rechnungs- oder ELO-Nummer kann optional eingetragen werden.
- Bestellvorgänge stehen beim Ersatzteil (Reiter Lieferanten & Einkauf, dort auch „+ Bestellvorgang“) und beim Lieferanten (Reiter Bestellungen); Angebot, AB und Lieferschein lassen sich als Dokument anhängen.
- Neue Rechte „Bestellvorgänge anlegen und bearbeiten“, „Wareneingang buchen“ und „Rechnung OK setzen“ (auch je Abteilung); wer bisher bestellen bzw. ins Lager buchen durfte, bekommt sie. Löschen darf nur der Bereichsadministrator Beschaffung.

## [0.76.4] – 2026-10-28

### Neu
- Standards für alle: Unter Core-Einstellungen übernimmt ein Administrator seine eigene Darstellung, sein Menü und sein Dashboard mit einem Klick als Standard – wer nichts Eigenes gespeichert hat, sieht diesen Stand; auf Wunsch werden auch die eigenen Einstellungen aller ersetzt, „Werksvorgabe“ stellt die Auslieferung wieder her.
- Neues Recht „Standards für alle festlegen“ (anfangs nur Administratoren).
- Neues Recht „Leitstand verwalten“ (Verwerfen, Aufgaben und Wartung im Leitstand anlegen) – bisher fest an Administrator und Manager gebunden, beide haben es weiterhin.
- Alles auf dem Dashboard ist jetzt ein Widget: „Tabelle offener Vorgänge“ und „Zeitstrahl“ sind neu im Katalog, Aktive Störungen, Schichten und fällige Wartung kommen aus den passenden Widgets – alles lässt sich verschieben, verbreitern oder herausnehmen. Wer schon ein eigenes Dashboard hatte, findet die bisher festen Teile unten angehängt.
- Neues Widget „Mein Kalender“: die eigenen nächsten 7 Tage mit Schicht, Urlaub, fälligen Vorgängen und Prüfpflichten (neues Recht „Widget Mein Kalender“, anfangs für alle).
- Neues Widget „Schichtplan Team“: wer aus der eigenen Abteilung in den nächsten 7 Tagen welche Schicht hat, mit Urlaub und Abwesenheit (Recht „Schichtplan sehen“).
- Rechte auf Abteilungsebene: In der Rollen-Matrix und bei den Einzelrechten lässt sich jedes Recht auf eine Abteilung samt Unterabteilungen begrenzen – z. B. Störungen bearbeiten nur in Halle 2.
- Bereichsadministrator: Für jeden Bereich (Störungen, Tickets, Wartung, Aufgaben, Projekte, KVP, Infrastruktur, IT, Lager, Beschaffung, Hersteller & Lieferanten, Schichten, Abwesenheit, Managementsysteme) gibt es ein Recht, das alle Rechte des Bereichs umfasst und endgültiges Löschen erlaubt.
- Stammdaten können vom Bereichsadministrator sofort „endgültig gelöscht“ werden; noch verwendete werden stattdessen ausgeblendet.
- Mehrere Leitstände: unter Navigation → Leitstände bekommt jede Abteilung, Gruppe oder jedes Team einen eigenen Leitstand aus Bausteinen (Neu anlegen, Vorgänge, Zeitstrahl, Kennzahlen, aktuelle Störungen, fällige Wartung, Hinweistext, Knöpfe zu anderen Leitständen), der nur ihre Vorgänge zeigt.
- Leitstände blättern auf Wunsch nach einer einstellbaren Zeit zum nächsten weiter; Bedienung am Bildschirm hält das Weiterblättern an.
- Terminals bekommen im Benutzerstamm einen festen Leitstand; sonst gilt der ihrer Gruppe, dann der ihrer Abteilung, dann der Standard-Leitstand.

- Das Logo dreht sich beim Laden einer Seite und solange gearbeitet wird – eine kleine Spielerei (aus bei „Bewegung reduzieren“).

### Geändert
- Der bisherige Leitstand ist jetzt der Standard-Leitstand und lässt sich wie jeder andere bearbeiten; Überschrift und Untertitel ändert, wer das Recht „Leitstand verwalten“ hat.
- Der Leitstand zeigt immer die Standard-Darstellung (Farbschema und Design-Stil) – unabhängig davon, wer gerade angemeldet ist.
- „Standard“ in Navigation anpassen und Dashboard anpassen holt jetzt den Firmenstandard zurück.
- Rollen & Rechte übersichtlicher: Rollen mit Rang, Abteilung, Pflichtschulungen und Benutzerzahl in eigener Tabelle; die Matrix hat ein Suchfeld, Gruppen zum Auf- und Zuklappen, eine feste Kopfzeile und Häkchen für ganze Gruppen; die Rechte sind neu gegliedert (z. B. „System & Sicherheit“, „Stammdaten“, „Daten & Schnittstellen“).
- „Zeiterfassung: Zeiten aller sehen, bearbeiten und löschen“ wird jetzt wirklich geprüft (bisher fest Administrator und Manager; Manager haben es weiterhin).
- Auto-Logout steht jetzt unter Core-Einstellungen statt unter Rollen & Rechte.
- Benutzerverwaltung: die Rollen-Knöpfe ergeben sich aus allen vorhandenen Rollen (auch eigenen), „Inaktive anzeigen“ blendet deaktivierte Benutzer ein, Gruppen stehen unter der Abteilung.
- Der Knopf „Zeitstrahl“ oben auf dem Dashboard entfällt – den Zeitstrahl nimmst du unter „Dashboard anpassen“ heraus oder fügst ihn hinzu.

### Behoben
- Inaktive Benutzer erschienen nie in der Benutzerverwaltung.
- Knöpfe mit Symbol und Text (z. B. „+ Neuer Benutzer“) waren im hellen Modus weiß auf weiß und abgeschnitten.
- Farbige Abzeichen (Rolle, Priorität, Status) sind im hellen Modus jetzt gut lesbar.

### Sicherheit
- Viele Aktionen prüften bisher nur die Anmeldung, nicht das Recht – z. B. Störungen und Tickets abschließen, Status ändern, Aufgaben, Projekte, Wartungen und IT-Geräte anlegen und bearbeiten, Ersatzteile und Lagerorte anlegen, Buchungen und Inventur über die Schnittstelle. Jetzt brauchen sie das passende Recht (in der Abteilung des Vorgangs); wer an einem Vorgang beteiligt ist, darf ihn weiter bearbeiten. Störungen und Tickets melden, kommentieren und Zeiten erfassen bleibt für alle offen.
- Damit niemand ausgesperrt wird, bekommen beim Update alle Rollen außer „Betrachter“ die Rechte zum Bearbeiten und Abschließen von Störungen, Tickets, Wartungen, Aufgaben, Projekten und IT; wer das Lager sieht, bekommt „Ersatzteile und Lagerorte anlegen“ (neu) und „Buchen“. Unter Rollen & Rechte lassen sie sich danach wegnehmen oder auf Abteilungen begrenzen.
- Wartungsaufträge, Aufgaben, Projekte und KVP-Vorschläge kann nur noch der jeweilige Bereichsadministrator endgültig löschen – bisher ging das bei Aufgaben, Projekten und Wartungsaufträgen für jeden.
- Stammdaten über die Schnittstelle zum Löschen vormerken braucht jetzt das Bearbeiten-Recht des Bereichs; Sperren, Deaktivieren und Feldsätze von Anlagen brauchen „Infrastruktur bearbeiten“.
- Rechte zum Bearbeiten und Abschließen von Vorgängen beachten jetzt die Abteilung des Vorgangs.
- Wer Rollen verwalten darf, aber kein Administrator ist, konnte anderen Rollen und Personen Rechte geben, die er selbst nicht hat – das geht jetzt nicht mehr.

## [0.76.3] – 2026-10-28

### Behoben
- Auf Smartphone und Tablet war der Knopf für das Benutzermenü rechts aus dem Bild gerutscht – Uhr und Benutzermenü bleiben jetzt immer ganz sichtbar.
- Mit vergrößerter Schrift war das geöffnete Benutzermenü auf dem Smartphone links abgeschnitten – es passt jetzt immer ins Bild.

### Geändert
- Ist die Kopfleiste zu schmal, kürzt sich zuerst der Seitentitel; danach werden Knöpfe ausgeblendet: Im Chat teilen, Copilot, Hell/Dunkel, Chat, rechte Seitenleiste, zuletzt die Sprache. „Im Chat teilen“, „Hell/Dunkel“ und „Suche, Hilfe & mehr“ stehen dann im Benutzermenü, Chat und Copilot als Reiter in der rechten Seitenleiste.
- Anmeldeseite, Easy-Mode und Leitstand folgen jetzt ebenfalls dem Design-Stil (Fiori oder Fluent).

## [0.76.2] – 2026-10-28

### Neu
- Design-Stil zum Umschalten: „Fiori“ (wie SAP, jetzt Standard) mit weißer Kopfleiste, Karten mit Schatten, Knöpfen mit blauer Schrift und durchgehend unterstrichenen Reitern, oder wie bisher „Fluent“ (wie Windows 11) – jeder wählt ihn im Benutzermenü unter „Darstellung“, der Firmenstandard steht unter Server-Einstellungen → Erscheinungsbild.

### Geändert
- Kopfleiste aufgeräumt: Chat teilen, Chat, Copilot, Hell/Dunkel und Sprache sind kleine Symbolknöpfe in Gruppen; was ein Knopf tut, zeigt der Hinweis beim Darüberfahren mit der Maus.

## [0.76.1] – 2026-10-28

### Behoben
- Die Netzlaufwerk-Sicherung verweigert einen Sicherungsordner, der schon einem anderen Server gehört (zwei Server mit gleichem Namen), statt die Sicherungen beider zu vermischen.
- Eine Änderungsdatei (WAL) mit gleichem Namen, aber fremdem Inhalt wird jetzt erkannt und gemeldet, statt verworfen.
- Verweise, die auf ihren eigenen Ordner zeigen (z. B. `uploads/uploads`), führen beim Dateiabgleich nicht mehr zu „rsync endete mit Fehler 23“ und endlosen Kopien.
- Bei Fehlern im Dateiabgleich zeigt der Reiter Ausfallschutz jetzt die betroffene Datei und den Grund.
- Nach einem Wechsel des Sicherungsordners (z. B. Server umbenannt) starten Grundsicherung und Dateiabgleich sofort statt erst in der Nacht.

## [0.76.0] – 2026-10-27

### Neu
- Datenbank-Spiegel im Ausfallschutz: Der aktive Server schreibt jede Speicherung synchron auch auf den zweiten Server (`pdh-ha-join.sh --voll`).
- „Rollen tauschen“ und „Übernahme erzwingen“ schalten jetzt wirklich um – Datenbank und PDH wechseln auf den anderen Server.
- Ein zurückkehrender Server gliedert sich selbst als Spiegel wieder ein; Daten, die nur auf ihm lagen, werden vorher gesichert und als „Abgezweigte Daten“ zur Prüfung angezeigt.
- Fällt der zweite Server aus, arbeitet der aktive nach kurzer Zeit asynchron weiter – im Trio erst nach Bestätigung des Zeugen.
- Einstellung „Automatische Umschaltung: Beobachtung oder echt“; „echt“ erst nach der Pflicht-Testumschaltung (hin und zurück).
- Neue Einstellungen „Asynchron nach“, „Verzögerungsgrenze“ und „Höchstmenge zurückgehaltener Änderungen“.
- Der Cloudflare-Tunnel läuft im Verbund nur auf dem aktiven Server und wechselt mit der Rolle – Anwender erreichen so immer den aktiven Server.
- Hat der aktive Server zu viele Änderungen für den ausgefallenen zweiten zurückgehalten, wird dieser bei seiner Rückkehr automatisch neu kopiert (mit Alarm).
- Neues Skript `scripts/pdh-static-ip.sh` stellt die jetzige IP-Adresse eines Servers fest ein (mit automatischem Zurücksetzen, falls die Verbindung abreißt).

### Geändert
- Die Netzlaufwerk-Sicherung läuft im Verbund nur noch auf dem aktiven Server; nach einer Übernahme schaltet der neue aktive Server sie mit den gespeicherten Einstellungen selbst ein und beginnt mit einer neuen Grundsicherung.
- Im Beobachtungsmodus ändern automatische Entscheidungen keine Rollen und Amtszeiten mehr, sie werden nur gemeldet.
- Auf dem passiven Server läuft das PDH nicht; Updates starten es dort nicht mehr.

### Behoben
- Kommentare zu einem Ticket werden nach dem Neuladen der Seite wieder angezeigt (vorher erschienen sie nur direkt nach dem Schreiben).

## [0.75.1] – 2026-10-25

### Behoben
- Der Zeugen-Container startet auch, wenn er auf einem Windows-Rechner gebaut wurde.
- Ein Zeuge hinter Docker-Portweiterleitung zeigt seinen Verbindungsweg richtig an und meldet keinen falschen Ausfall mehr.
- In der Knotenübersicht steht beim Zeugen kein irreführendes „Gateway: nein“ mehr.

## [0.75.0] – 2026-10-25

### Neu
- Ausfallschutz mit zwei Servern und optionalem Zeugen im Beobachtungsmodus: Server koppeln, die Server prüfen sich gegenseitig über LAN und VPN, Rollen und Ereignisse ansehen – es wird noch nichts umgeschaltet.
- Testlabor für den Ausfallschutz (Docker).
- Bei unterschiedlichen Versionen zeigt die Übersicht den Update-Befehl für den zweiten Server und den Zeugen.

### Geändert
- Der Reiter Ausfallschutz zeigt an, ob die Netzlaufwerk-Sicherung auf einem passiven Server künftig pausieren würde – in dieser Version läuft sie weiter.

### Sicherheit
- Die Schlüssel des Ausfallschutzes mit zwei Servern werden nicht aufs Netzlaufwerk gesichert und beim Wiederaufbau nicht zurückgespielt.

## [0.74.2] – 2026-10-24

### Behoben
- Nach der Ersteinrichtung ließ sich der im Einrichtungsassistenten angelegte Administrator nicht anmelden („Benutzer nicht gefunden“) – fehlende Angaben zu Abteilung und Telefon werden jetzt als leer behandelt; betroffene Konten gehen nach dem Update wieder.

## [0.74.1] – 2026-10-24

### Behoben
- Einrichtung des Update-Agenten und Wiederaufbau brachen mit „detected dubious ownership“ ab, wenn das PDH-Verzeichnis dem Benutzer pdh gehört (z. B. nach pdh-vm-prepare.sh --neu).

## [0.74.0] – 2026-10-24

### Neu
- Skript pdh-vm-prepare.sh (Server-Einstellungen → Ausfallschutz): bereitet eine frische Ubuntu-VM vor – Zeitzone, Firewall, Gast-Agent für Proxmox/VMware/Hyper-V – und richtet wahlweise eine neue PDH-Installation ein oder bindet die Sicherungsfreigabe für den Wiederaufbau dauerhaft ein.

## [0.73.0] – 2026-10-24

### Neu
- Ausfallschutz (Server-Einstellungen): Der Server sichert Datenbank, Dateien und Konfiguration von PDH, Nextcloud und n8n laufend auf ein Netzlaufwerk; mit pdh-restore.sh lässt sich daraus ein neuer Server oder eine VM aufbauen – auch auf einen bestimmten Zeitpunkt.
- Ausfallschutz: Status der VM-Sicherung (Datum oder Zeitstempel-Datei) mit Warnung an die Administratoren, wenn sie zu alt ist.
- Nextcloud-Installationsskript: Wiederherstellungsmodus `--restore` für den Wiederaufbau.

## [0.72.1] – 2026-10-24

### Behoben
- Docker: Das PDH-Netz liegt jetzt fest auf 172.31.254.0/24 (änderbar über `PDH_DOCKER_SUBNET` in `.env.docker`) statt auf einem von Docker gewählten Netz, das VPN-Netze überschneiden konnte – das Update stellt bestehende Installationen um und startet dabei alle Dienste einmal neu.
- Docker: Neuinstallationen legen auch die Docker-eigenen Netze in 172.31.0.0/16, damit sie nicht mit WireGuard-Netzen kollidieren.
- VPN: Tunnel mit Adressen in 172.17.x (z. B. 172.17.214.2/24) starten jetzt – das Installationsskript verlegt das Docker-Standardnetz docker0 von 172.17.0.0/16 nach 172.31.255.0/24 (danach einmal das Installationsskript erneut ausführen).
- VPN: Meldet der Dienst pdh-wireguard einen Fehler (z. B. ein Tunnel überschneidet ein Netz des Servers), zeigen die Tunnel und Geräte weiter ihren echten Zustand statt „Status unbekannt – VPN-Dienst läuft nicht“; die Fehlermeldung steht oben im VPN-Reiter.

## [0.72.0] – 2026-10-23

### Geändert
- Administratoren werden nicht mehr wegen Inaktivität abgemeldet, solange Code-Editor, n8n oder pgAdmin in einem anderen Tab offen sind – bisher beendete der PDH-Tab im Hintergrund die Sitzung, und das Werkzeug blieb hängen.

### Behoben
- VPN: Ein importierter Tunnel zum Maschinennetz verbindet sich jetzt wie im WireGuard-Client, auch wenn das VPN für Geräte und Gateways ausgeschaltet ist – bisher blieb er ohne eingeschaltetes VPN und öffentlichen Namen stumm (danach einmal das Installationsskript erneut ausführen).

## [0.71.0] – 2026-10-23

### Neu
- Lagerverwaltung: Unter „Bearbeiten“ lässt sich die Art eines Knotens ändern (Lagerort, Regal, Fach, Platz) und der Knoten samt Inhalt an eine andere Stelle im Baum verschieben; PDH erlaubt nur, was zu Lage und Unterknoten passt.

### Geändert
- Lagerverwaltung: Löschen wirkt sofort – ohne Bestand wird der Knoten mit allen Unterknoten gelöscht, mit alten Buchungen ausgeblendet; liegt noch Bestand dort, nennt PDH die Teile und löscht nichts. Gründe, warum etwas nicht geht, stehen direkt unter der Zeile.

## [0.70.0] – 2026-10-23

### Neu
- Die Statusleiste unten ist höher und zeigt im Balken, was PDH gerade macht und wie weit es ist (z. B. „Speichere: Anlegen … 62 %“), bei Uploads mit echtem Fortschritt; Hintergrund-Aktualisierungen lassen den Balken in Ruhe.

### Geändert
- Lager → Sammelanlegen: Der Dialog hat beschriftete Knöpfe „Abbrechen“ und „Anlegen“, bleibt nach dem Anlegen offen und zeigt das Ergebnis mit „Weitere anlegen“ und „Fertig“; das Ziel wird im Baum aufgeklappt und markiert.
- Knöpfe, die mit Beschriftung gedacht sind (z. B. Speichern, Abbrechen, Anlegen in Dialogen), zeigen jetzt neben dem Symbol auch ihren Text.

### Behoben
- Lager → Sammelanlegen: Der Knopf zum Anlegen war nur ein unbeschriftetes Symbol und leicht zu übersehen; Fehler erscheinen jetzt direkt im Dialog („Nichts angelegt: …“).

## [0.69.0] – 2026-10-23

### Neu
- VPN: Jede Verbindung zeigt deutlich, ob sie steht – „verbunden“, „getrennt – letzter Kontakt vor …“, „keine Antwort vom Server“, „noch nie verbunden“ –, mit Datenmenge; die Anzeige und die Zusammenfassung („Tunnel 1/1 verbunden“) aktualisieren sich alle 10 Sekunden.
- VPN: Geräte, Standort-Gateways und Tunnel lassen sich deaktivieren und wieder aktivieren, ohne sie zu sperren – die Konfiguration bleibt gültig.

### Behoben
- Das VPN-Installationsskript bricht nicht mehr mit „Syntax error: redirection unexpected“ ab, wenn es mit „sh“ statt „bash“ gestartet wird; das Dienst-Skript im Repository heißt jetzt pdh-wireguard-daemon.sh und erklärt beim versehentlichen Start, dass das Installationsskript aus Server-Einstellungen → VPN gemeint ist.

## [0.68.0] – 2026-10-23

### Neu
- VPN: Den Tunnel zum Maschinennetz legst du an, indem du die .conf-Datei des Routers hochlädst oder hineinziehst; „Prüfen“ zeigt vorab Endpunkt, Tunneladresse und Netze.
- VPN: Neue Konfiguration in einen bestehenden Tunnel einspielen, z. B. nach neuen Schlüsseln am Router – Name und Protokoll bleiben.
- VPN: Konfigurationen für mehrere Benutzer auf einmal erstellen und alle zusammen als ZIP herunterladen (je Person ein Ordner).

## [0.67.0] – 2026-10-23

### Geändert
- VPN: WireGuard läuft jetzt direkt auf dem Server als systemd-Dienst „pdh-wireguard“ statt in einem eigenen Docker-Container; ein Installationsskript richtet ihn mit einem Befehl ein.
- Die Einrichtung des VPN (Einstellungen, Standort-Gateways, ausgehende Tunnel, alle Geräte, Protokoll) liegt unter Server-Einstellungen → VPN und ist nur für Administratoren; die Seite „VPN Maschinennetz“ zeigt nur noch die eigenen Geräte.
- Das Recht „VPN verwalten“ entfällt – die Verwaltung gehört zu den Server-Einstellungen.

### Sicherheit
- Der VPN-Dienst prüft vor dem Start, ob sich ein Netz mit dem Netz des Servers (LAN, Docker) überschneidet, und setzt seine Firewall-Regeln nur für VPN-Verkehr.

## [0.66.0] – 2026-10-23

### Neu
- VPN ins Maschinennetz (WireGuard): Mit Laptop oder Handy über PDH in die Netze der Maschinen – Zugang je Gerät mit Konfiguration zum Herunterladen und QR-Code für die WireGuard-App.
- Standort-Gateways: Router im Maschinennetz wählen sich bei PDH ein und geben ihre Netze frei, ohne dass am Router ein Port offen sein muss.
- Ausgehende Tunnel: PDH verbindet sich als Client zu einem vorhandenen WireGuard-Server im Maschinennetz; die Client-Konfiguration lässt sich einfach einfügen.
- Zugänge lassen sich sofort sperren; ein Protokoll zeigt, wer wann Zugänge angelegt, geladen oder gesperrt hat (neue Rechte „VPN nutzen“ und „VPN verwalten“).

## [0.65.0] – 2026-10-23

### Neu
- In der Schnellübersicht schaltet ein Klick auf einen Motor, ein Ventil oder ein anderes Bauteil einer Trockenkammer zwischen grün (OK) und rot (NOK) um.
- Die Schnellübersicht zeigt zugeordnete Energiewerte (z. B. kWh, Leistung, Verbrauch) mit dem letzten Messwert.
- Dashboard-Widget „Anlagen-Schnellübersicht“: die Ampel einer gewählten Übersicht direkt auf der Startseite.
- Dashboard-Widget „Scannen“ im Schnellzugriff: QR-Codes und Barcodes mit der Handy-Kamera scannen – öffnet Anlage, Ersatzteil oder Kanban-Karte, auch für Handscanner.

## [0.64.0] – 2026-10-23

### Neu
- Anlagen-Schnellübersicht: Anlagen mit besonderem Augenmerk (einzeln gewählt oder ganze Anlagentypen, z. B. alle Trockenkammern) auf einer Seite mit Ampel für offene Störungen, überfällige Wartungen und Prüfungen sowie NOK-Bauteile.
- Trockenkammern zeigen in der Schnellübersicht Motoren, Klappenmotoren, Heizungsventile, Heizungspumpen, Torrollen und Tordichtungen als farbige Kästchen mit ihrem Status.
- Schnellübersichten lassen sich für Gruppen und einzelne Benutzer freigeben (neues Recht „Anlagen-Schnellübersichten einrichten“); jede Person kann sich zusätzlich eigene Übersichten anlegen.

## [0.63.1] – 2026-10-22

### Geändert
- Symbole erscheinen schneller: Die Symbol-Bibliothek wird komprimiert übertragen und bleibt im Browser gespeichert, statt täglich neu geladen zu werden.

## [0.63.0] – 2026-10-22

### Neu
- Bauteil per Foto erkennen: Beim Anlegen als neues Teil schlägt PDH ein passendes Teilebild aus dem Internet vor (Suche nach Hersteller und Teilenummer); wahlweise eigenes Foto oder kein Bild.

### Behoben
- Große Handyfotos brechen die Foto-Erkennung nicht mehr mit „Bild zu groß“ ab: PDH verkleinert sie im Browser und auf dem Server, ein vom Proxy abgewiesener Upload wird automatisch kleiner wiederholt.
- Teilebilder aus Fotos werden verkleinert gespeichert und erscheinen richtig gedreht.

## [0.62.0] – 2026-10-22

### Geändert
- Tickets, Störungen und Aufgaben sind auf der Detailseite aufgebaut wie der KVP: Zuständigkeit, Termin, Anlage und Kostenstelle ändert man im neuen Reiter „Bearbeiten“ mit einem gemeinsamen „Speichern“ statt in vielen Einzelfeldern rechts.
- Die Zeiterfassung hat auf Ticket, Störung und Aufgabe einen eigenen Reiter „Zeit“; die Karte „Details“ in der Übersicht zeigt alles auf einen Blick zum Nachlesen.
- Aufgaben haben jetzt wie Tickets und Störungen den Reiter „Maßnahmen & Ersatzteile“.
- Die Detailseiten sind im Windows-11-Stil gestaltet: Einstellungs-Zeilen im Reiter „Bearbeiten“, Meldungsleisten, Eigenschaftenliste in „Details“ und ein Windows-11-Dialog zum Reservieren von Ersatzteilen.
- Schaltflächen auf den Detailseiten tragen wie beim KVP Symbol und Beschriftung; der doppelte „Abschließen“-Knopf und das Menü oben rechts entfallen – abgeschlossen wird über die Karte in der Übersicht.
- Der Ticket-Status lässt sich im Reiter „Bearbeiten“ ändern; „Gelöst“ und „Geschlossen“ gehen nur noch über den Abschluss-Assistenten.

## [0.61.0] – 2026-10-22

### Neu
- Managementsysteme (eigene Gruppe in der Navigation): gelenkte Dokumente für Qualität (ISO 9001), Umwelt (14001), Arbeitsschutz (45001), Energie (50001), Informationssicherheit (27001) und das integrierte System – Handbücher, Prozessbeschreibungen, Verfahrens- und Arbeitsanweisungen, Formblätter & Vorlagen, Leitlinien
- Dokumentnummern automatisch (z. B. QM-PB-001), neue Dokumente starten mit passender Gliederung; Inhalt mit Überschriften, Listen, Tabellen und Live-Vorschau, Dateien als Anhang
- Fassungen mit Änderungsgrund: Entwurf → Prüfung → Freigabe nach dem Vier-Augen-Prinzip; ältere Fassungen bleiben einsehbar, Dokumente lassen sich zurückziehen
- Kenntnisnahme je Fassung (alle oder eine Abteilung) mit Übersicht, wer bestätigt hat; Überprüfungsfrist mit Markierung, wenn sie fällig ist; mitgeltende Dokumente verknüpfbar
- Eigene Rechte: lesen, anlegen/bearbeiten, prüfen/freigeben, Systeme verwalten; weitere Managementsysteme lassen sich ergänzen

## [0.60.0] – 2026-10-22

### Neu
- Fotos beim Melden: Im Erstellungs-Assistenten (auch im Leitstand) bis zu fünf und im Easy-Mode bis zu drei Fotos aufnehmen oder auswählen – sie werden auf dem Gerät verkleinert und stehen danach unter „Dokumente“ am Vorgang

### Behoben
- Kalender: Aufgaben fehlten ganz (Abfrage nutzte ein seit der Mehrfach-Zuweisung entfallenes Feld) – sie stehen jetzt bei allen Zuständigen, ohne Zuständige bei der verantwortlichen Person; Tickets, Störungen und Wartungen ohne Zuweisung ebenfalls bei der verantwortlichen Person
- Kalender: Kann eine Art nicht geladen werden, steht oben eine Meldung, statt dass die Einträge still fehlen

## [0.59.0] – 2026-10-22

### Neu
- Etiketten: Auswahl nach Lagerort – in der Etikettenansicht Lager, Regal, Fach oder Platz anhaken (mit allen Unterplätzen, Zahl der Etiketten je Ort, Suche, „nur mit Bestand“)
- Etiketten-Designer (Etikettenansicht → Designer): je Etikettengröße Felder frei platzieren und in der Größe ändern – QR-Code, Logo, Lagerplatz, Lagerpfad, Bezeichnung, Kategorie, Teilenummer, Hersteller, Hersteller-Teilenummer, Mindestmenge, EAN, Freitext; je Feld Schriftgröße, fett, Festbreite, Ausrichtung, maximale Zeichen und Zeilen, Präfix und Linie
- Das gestaltete Layout gilt für alle Druckwege: Browser, Netzwerkdrucker (PDF), Zebra und Dymo; „Standard“ stellt die bisherige Gestaltung wieder her

### Behoben
- Kalender: Angelegte Vorgänge ohne Fälligkeitsdatum fehlten – sie stehen jetzt am Tag ihrer Anlage; überfällige und ältere offene Vorgänge stehen (mit ⚠ bzw. Hinweis) in der Spalte „heute“

## [0.58.0] – 2026-10-22

### Neu
- Kalender (Instandhaltung → Kalender): je Mitarbeiter eine Zeile mit Urlaub, Schichten, fälligen Wartungen, Prüfpflichten, Tickets, Störungen, Aufgaben und laufenden Projekten – standardmäßig 14 Tage, wählbar was angezeigt wird sowie Abteilung, Gruppe und Person; Überschneidungen (abwesend, aber Schicht oder fälliger Auftrag) sind rot umrandet, die Auswahl merkt sich PDH

### Geändert
- Neues Erscheinungsbild im Stil von Windows 11: Navigation und Kopfzeile auf ruhigem Hintergrund, Inhalt als Fläche mit runder Ecke, Navigation mit Markierung der aktiven Seite, flachere Knöpfe und Eingabefelder, Reiter mit kurzem Unterstrich, schmale Scrollbalken – Firmenfarbe und Erscheinungsbild-Einstellungen gelten weiter
- Navigation links: Es ist immer nur die Gruppe der geöffneten Seite aufgeklappt, beim Aufklappen einer anderen Gruppe schließen sich die übrigen
- Leitstand im selben Design wie das übrige PDH: Farbschema und Firmenfarbe aus dem Erscheinungsbild, Windows-11-Grautöne, gleiche Schrift
- Einheitliche Farben für die Arten in Leitstand und Kalender: Störung orange, Ticket hellblau, Wartung gold, Aufgabe violett – Rot steht nur noch für „überfällig“ und Überschneidungen
- Handbuch im neuen Design (Inhaltsverzeichnis mit Markierung, Karten, Anleitungen mit Farbleiste); die Skizze zum Aufbau der Oberfläche zeigt die neue Navigation

## [0.57.0] – 2026-10-21

### Neu
- Planer (Instandhaltung → Planer) ersetzt den Schichtplan: Schichtplan, Urlaub & Abwesenheit, Wartungsplanung, Schichtmodelle & Vorlagen und Einstellungen an einer Stelle
- Schichtplan als Wochenkalender: oben die Tage, links die Schichten des rechts gewählten Schichtmodells, im Feld die eingeteilten Mitarbeiter – ein Klick öffnet die Auswahl mit Hinweisen auf Urlaub und andere Schichten
- Mindestbesetzung je Schicht (Feld rot, wenn zu wenige eingeteilt), Feiertage und Betriebsruhe im Kalender, Filter nach Abteilung und Gruppe, „Vorwoche übernehmen“
- Vorlagen für Schichtmodelle: Tagschicht, 2-Schicht, 3-Schicht, 4- und 5-Schicht-Kontischicht, 12-Stunden Tag/Nacht (2-2-3) und Rufbereitschaft – legen Schichten und Rotationsmuster in einem Schritt an
- Schichtmodelle, Schichten (Zeiten, Farbe, Pause, Mindestbesetzung) und Rotationsmuster lassen sich bearbeiten; „Plan erzeugen“ verteilt ein Muster auf Mannschaften mit automatischem Versatz, zuerst als Entwurf
- Urlaub & Abwesenheit: Antrag mit halben Tagen und Vertretung, Genehmigung mit Hinweis auf Schichten und gleichzeitig abwesende Kollegen, Urlaubsanspruch mit Übertrag und Resturlaub, Teamkalender je Monat, Benachrichtigung per Chat
- Arbeitstage werden ohne Wochenende, gesetzliche Feiertage (Bundesland wählbar) und Betriebsruhe gezählt
- Wartungsplanung: Monatskalender mit offenen Wartungen und Prüfpflichten, Warnung wenn die zugewiesene Person abwesend ist oder keine Schicht hat, Leute im Dienst je Tag, Verschieben und Zuweisen an verfügbare Mitarbeiter
- Neue Rechte: „Eigene Abwesenheit beantragen“, „Abwesenheiten genehmigen“, „Abwesenheitsgrund aller sehen“

### Geändert
- „Schichtplan ansehen/bearbeiten“ heißen jetzt „Planer ansehen“ und „Schichten planen“ und werden überall geprüft; ohne Recht sehen Kollegen bei fremden Abwesenheiten nur „abwesend“
- Der Wochenstreifen auf dem Dashboard zeigt die eigenen Schichten (ohne Entwürfe)

### Behoben
- Der Dashboard-Wochenstreifen zeigte die Schichten einer beliebigen Person und sonntags schon die nächste Woche

## [0.56.2] – 2026-10-21

### Behoben
- Der Schichtplan war nach 0.56.1 nicht erreichbar (Weiterleitung auf den noch nicht fertigen Planer) – die bisherige Seite ist wieder da

### Sicherheit
- Schichtplan-Schnittstelle prüft jetzt die Rechte: ansehen mit „Planer ansehen“, ändern mit „Schichten planen“; Abwesenheiten für andere anlegen oder genehmigen nur mit „Abwesenheiten genehmigen“, fremde Abwesenheiten sehen nur mit dem passenden Recht

## [0.56.1] – 2026-10-21

### Geändert
- Ersatzteil-Etiketten zeigen die Teilenummer vollständig über die ganze Breite (lange Nummern brechen um bzw. werden kleiner gedruckt), darunter den Hersteller – im Browser-Druck, auf Zebra und auf Dymo

### Behoben
- „Kanban aktivieren“ bzw. Speichern im Reiter Kanban scheiterte mit „inkonsistente Typen für Parameter $3“

## [0.56.0] – 2026-10-21

### Neu
- Beschaffung & Einkauf (Material & Anlagen, auch über Core-Einstellungen): Kanban-Tafel, Bestellvorschlag aus leeren Kanban-Karten, offene Bestellungen mit Liefertermin und Rahmenverträge
- Kanban je Ersatzteil zum Aktivieren (Reiter „Kanban“ im Ersatzteilstamm): Anzahl Karten, Menge je Karte, Lagerplatz, Lieferant und Rahmenvertrag; Karten werden bei sinkendem Bestand automatisch leer gemeldet und lassen sich mit QR-Code drucken
- Kanban-Karte scannen: Behälter leer melden oder Wareneingang buchen direkt am Lagerplatz
- Rahmenverträge mit Positionen, Vertragspreis und Rahmenmenge; Kanban-Bestellungen rufen die Menge ab, der Status warnt vor Ablauf und Ausschöpfung
- Buchungserfassung je Ersatzteil mit Bild und Beschreibung – der QR-Code auf dem Lagerplatz-Etikett führt jetzt direkt dorthin: Einbuchung, Ausbuchung, Reservierung, Sonstiges und Korrektur
- Ausbuchung und Reservierung fragen, welchem Vorgang (Störung, Ticket, Aufgabe, Wartung) oder welcher Anlage aus dem Infrastruktur-Baum die Teile zugeordnet werden
- Eigene Rechte in Rollen & Rechte: „Ersatzteile ein-/ausbuchen und reservieren“, „Bestandskorrekturen und sonstige Buchungen“ sowie vier Rechte der Gruppe „Beschaffung“

### Geändert
- Teile mit Kanban stehen nicht mehr im täglichen Bestellvorschlag nach Mindestbestand – sie bestellt der Kanban-Kreislauf
- Eine Ausbuchung über die Buchungserfassung oder den Teilestamm kann einen Lagerplatz nicht mehr ins Minus buchen

### Behoben
- Ersatzteile → Scannen: Fehlermeldungen (z. B. „Position 3: Menge fehlt“, Upload zu groß) wurden nicht angezeigt
- Ersatzteile → Scannen: Eine Erkennung, die das Zeitlimit überschritt oder durch einen Neustart abbrach, blieb für immer auf „läuft“ – sie wird jetzt mit Hinweis als Fehler beendet
- Ersatzteile → Scannen: Ein Lieferschein ließ sich durch doppeltes Absenden zweimal buchen, aus einem Bauteil-Foto ließen sich mehrere Teile anlegen
- Ersatzteile → Scannen: Ein abgelehnter Upload zeigte eine leere 404-Seite statt der Fehlermeldung; bestellte Mengen und Rückstände stehen mit Komma
- Barcode-Suche erkennt die QR-Codes von PDH-Etiketten und Kanban-Karten
- Zwei gleichzeitige Buchungen am selben Lagerplatz konnten sich gegenseitig überschreiben
- Die Schnellbuchung im Lager prüfte keine Rechte
- Formulare mit mehreren Aktionsknöpfen (z. B. „In Nextcloud einrichten“) schickten an eine falsche Adresse („/admin/[object HTMLButtonElement]“, 404) und taten scheinbar nichts – der PDH-Administrator in Nextcloud ließ sich so nicht anlegen
- „scripts/repair-update-agent-token.sh“ trägt auch die fehlende Adresse des Update-Agenten wieder ein – ohne sie war das PDH „ohne Update-Agent“ und z. B. „In Nextcloud einrichten“ gesperrt
- Der Hinweis „In der Einstellungsdatei stehen Werte, die noch nicht in der Datenbank sind“ nennt das Update-Agent-Token nicht mehr – es bleibt absichtlich in der Datei
- Server-Einstellungen → Nextcloud: Schlägt „In Nextcloud einrichten“ fehl, steht der Grund jetzt rot direkt in der Karte „PDH-Administrator in Nextcloud“ statt nur oben auf der Seite
- Gesperrte Knöpfe sind jetzt blasser dargestellt und sehen nicht mehr wie klickbare aus; die Karte „PDH-Administrator in Nextcloud“ sagt, wenn noch die Nextcloud-Adresse fehlt
- Fehlermeldungen von occ-Befehlen nennen die eigentliche Fehlerzeile statt einer Zwischenüberschrift
- Ein abgebrochenes Nextcloud-System-Update lässt Nextcloud nicht mehr im Wartungsmodus zurück (Nextcloud antwortete dann mit „503 Service Unavailable“)

## [0.55.2] – 2026-10-21

### Behoben
- Update-Agent startete nach einem Neustart nicht mehr („PDH_UPDATE_AGENT_TOKEN must be at least 32 characters“): Beim Übernehmen der Einstellungsdatei in die Datenbank wurde auch das Agent-Token übernommen und in der Datei auskommentiert – Token und Adresse des Agenten bleiben jetzt immer in der Datei
- Server-Protokoll: „copilot bereit“ nennt bei Claude das tatsächlich genutzte Modell statt des Ollama-Modells
- nginx-Einrichtungsskripte legen ihre Sicherungen jetzt unter `/etc/nginx/pdh-backups/` ab statt in `sites-enabled` – dort wurden sie mitgeladen und erzeugten „conflicting server name“

## [0.55.1] – 2026-10-21

### Behoben
- „Update-Agent antwortet mit 401 Unauthorized“: Token und Adresse des Update-Agenten lassen sich nicht mehr über die Server-Einstellungen in die Datenbank speichern, dort vorhandene Werte werden beim Start entfernt; „scripts/repair-update-agent-token.sh“ gleicht PDH und Agent wieder an

## [0.55.0] – 2026-10-21

### Neu
- Nextcloud: eigenes Admin-Konto für das PDH (Vorgabe „pdhNCadmin“) – Server-Einstellungen → Nextcloud → „In Nextcloud einrichten“ legt es an, erzeugt ein zufälliges Passwort und trägt es im PDH ein
- Das PDH hat beim Passwort Vorrang: Alle 6 Stunden prüft es die Anmeldung und setzt sein Passwort und die Admin-Rechte in Nextcloud wieder, wenn sie dort geändert wurden; „Neues Passwort“ tauscht es jederzeit aus

### Geändert
- Handbuch „Versionen & Updates“: Hinweis, dass ein Update am Server „install_update.sh“ braucht – „git pull“ allein baut das Programm nicht neu

## [0.54.0] – 2026-10-21

### Neu
- Ersatzteile: Feld „EAN / GTIN“ in den Stammdaten (mit Barcode-Knopf und Prüfziffern-Kontrolle); die Ersatzteil-Suche findet Teile auch über die EAN
- Ersatzteile → Barcode: Strichcode fotografieren öffnet das passende Teil direkt zum Buchen (EAN, Teile-, Hersteller- oder Lieferanten-Nummer); ohne Barcode-Unterstützung im Browser lässt sich die Nummer eintippen
- Lieferschein-Scan: bestellte Menge und Rückstand werden mitgelesen, Positionen mit Rückstand und Teile aus dem Bestellvorschlag sind markiert; nach dem Buchen zeigt PDH die Teile des Lieferanten, die noch unter Mindestbestand liegen
- Lieferschein- und Bauteil-Scan übernehmen eine erkannte EAN in neue Teile und ergänzen sie bei vorhandenen Teilen ohne EAN
- n8n und pgAdmin unter der PDH-Adresse (/n8n/, /pgadmin/), nur für Administratoren und in den Core-Einstellungen verlinkt; n8n-Webhooks bleiben ohne Anmeldung erreichbar – Einrichtung mit „scripts/configure-pdh-admin-tools.sh“

### Sicherheit
- „scripts/configure-pdh-admin-tools.sh --disable-old-code-site“ schaltet die alte nginx-Seite des Code-Editors ab, die ihn ohne PDH-Anmeldung erreichbar machte

### Geändert
- „scripts/configure-pdh-nginx-websockets.sh“ richtet die WebSockets jetzt für /code/, /n8n/ und /pgadmin/ ein und übernimmt von Hand eingetragene Blöcke

## [0.53.0] – 2026-10-20

### Neu
- Ersatzteile → Scannen: Lieferschein mit dem Smartphone fotografieren (oder als PDF wählen) – PDH erkennt Lieferant, Lieferschein-Nr. und Positionen und schlägt je Position einen Zugang zu einem vorhandenen Teil oder eine Neuanlage vor; nach Prüfung werden alle Positionen zusammen gebucht
- Beim Buchen eines Lieferscheins merkt sich PDH die Artikelnummern des Lieferanten am Teil, damit der nächste Lieferschein die Teile direkt findet
- Bauteil per Foto erkennen: Bezeichnung, Hersteller, Typ und Merkmale vom Foto bzw. Typenschild, passende Teile im Lager mit Bestand, sonst vorausgefüllte Neuanlage mit dem Foto als Teilebild
- Infrastruktur: Feld „Verantwortlich“ in den Stammdaten; ohne eigene Angabe gilt die Person des übergeordneten Elements, die Stammkarte zeigt „geerbt von …“
- Neue Tickets, Störungen, Wartungsaufträge und Aufgaben ohne Verantwortliche/n übernehmen automatisch die Person der Anlage – abschaltbar unter Core-Einstellungen → „Verantwortliche aus der Infrastruktur“

### Geändert
- Infrastruktur: Der Servicepartner wird aus den Lieferanten gewählt; ein bisher eingetragener Servicepartner, der kein Lieferant ist, bleibt sichtbar und erhalten

### Behoben
- Code-Editor hinter nginx: „WebSocket close with status code 1006“ – „sudo bash scripts/configure-pdh-nginx-websockets.sh“ reicht die WebSockets für /code/ durch
- Infrastruktur → Prüfungen & GBU: Mit dem Recht „Prüfpflichten“ allein war die Auswahl der Verantwortlichen leer

## [0.52.1] – 2026-10-19

### Behoben
- Code-Editor ohne Docker: „scripts/install-code-server.sh“ sucht selbst einen freien Port, zeigt an, welches Programm einen Port belegt, und übernimmt mit „--existing PORT“ einen schon laufenden code-server

## [0.52.0] – 2026-10-19

### Neu
- Nextcloud mit Microsoft 365: Unter Core-Einstellungen → Microsoft 365 legt der Abgleich auf Wunsch fehlende Nextcloud-Konten an, mit der Microsoft-E-Mail als Benutzer-ID
- Nextcloud-Anmeldung mit Microsoft: Core-Einstellungen → Microsoft 365 → „In Nextcloud einrichten“ richtet den Knopf „Mit Microsoft anmelden“ ein, mit den Zugangsdaten aus den Server-Einstellungen; nur Personen mit Nextcloud-Konto aus dem PDH kommen hinein (ohne Update-Agent: Skript „scripts/configure-nextcloud-microsoft-login.sh“)
- Benutzerstamm → Zuständigkeiten: Bei Brokern zeigt „Wartet auf Zuweisung (als Broker)“ die offenen Vorgänge ihrer Arten, die noch niemandem gehören
- Neues Modul „Vorlagen & Signaturen“: Firmenvorlagen für Word, Excel und PowerPoint zentral hochladen, versionieren und herunterladen, auf Wunsch in eine SharePoint-Bibliothek gespiegelt, damit sie in Office unter „Neu“ erscheinen
- E-Mail-Signaturen mit Platzhaltern wie {Name}, {Position}, {Telefon} oder {Mobil}, je Abteilung oder für alle, mit Vorschau je Person; das Exchange-Skript rollt sie als Transportregel an jede ausgehende Mail aus
- „Werte nach Microsoft 365 übertragen“ schreibt Position, Abteilung, Telefon, Mobil, Standort und Firma aus dem Benutzerstamm nach Microsoft 365; leere Felder übernimmt PDH von dort
- Benutzerstamm → Übersicht → E-Mail: Knopf „Testmail senden“ prüft den Versand an die Adresse der Person
- Schalter „Statusmeldungen auch per E-Mail“ je Person: Die Hinweise von PDH-System kommen dann zusätzlich zum Chat per E-Mail, mit Link zum Vorgang
- Infrastruktur-Auswahl umschaltbar „Nach Standort“ oder „Nach Kostenstelle“: Die Kostenstellen lassen sich aufklappen, darunter steht ihre Infrastruktur im gewohnten Baum
- Infrastruktur-Rechte wie bei den übrigen Modulen: „Infrastruktur ansehen“ und „Prüfpflichten & Gefährdungsbeurteilungen pflegen und erledigen“ zusätzlich zu „bearbeiten“; alle Rollen behalten das Ansehen
- Neue Rolle „Anlagenverantwortliche/r“: Infrastruktur und Prüfpflichten verwalten, dazu Tickets, Störungen und Wartung bearbeiten und abschließen
- Zertifikate für Nextcloud: eigene Zertifizierungsstelle im PDH stellt persönliche Zertifikate für die Nextcloud-App, Geräte- und Server-Zertifikate als passwortgeschützte .p12 zum Herunterladen aus (Core-Einstellungen → Zertifikate, selbst unter Mein Konto); gesperrte Zertifikate lehnt nginx über die Sperrliste ab („scripts/configure-nextcloud-client-certs.sh“)
- Code-Editor für Administratoren: VS Code im Browser (code-server) mit dem PDH-Quellcode unter /code/, Anmeldung über das PDH, verlinkt in den Core-Einstellungen; Docker-Installation automatisch, ohne Docker einmalig „sudo bash scripts/install-code-server.sh“

### Behoben
- Benutzerstamm → Zuständigkeiten zeigt jetzt auch Vorgänge, die einer Gruppe der Person zugewiesen sind, sowie Projekte, denen sie zugewiesen ist; erledigte Aufgaben erscheinen nicht mehr als offen

## [0.51.0] – 2026-10-18

### Neu
- E-Mail-Versand über Microsoft 365 ohne SMTP: Unter Server-Einstellungen → E-Mail den Versandweg „graph“ wählen – der PDH sendet dann über die App-Registrierung aus dem Reiter Microsoft 365 (Berechtigung Mail.Send), auch wenn SMTP AUTH im Tenant abgeschaltet ist

### Behoben
- E-Mail-Versand über Microsoft 365 (smtp.office365.com): Die Anmeldung scheiterte mit „504 5.7.4 Unrecognized authentication type“, jetzt meldet sich der PDH per AUTH LOGIN an, wenn der Server kein AUTH PLAIN anbietet

## [0.50.1] – 2026-10-18

### Neu
- Installation ohne Docker (systemd): Grafana einmalig mit „sudo bash scripts/install-grafana.sh“ einrichten – lokaler Container, Nur-Lese-Benutzer und Verbindung zum PDH in einem Schritt

### Behoben
- Statistik bei Installationen ohne Docker: Die Grafana-Datenquelle fand die PDH-Datenbank nicht, und der Nur-Lese-Benutzer ließ sich ohne Rollenrechte des PDH-Datenbankbenutzers nicht einrichten

## [0.50.0] – 2026-10-18

### Neu
- Server-Einstellungen, Reiter „Störungen“: Schalter „Ticket zu jeder Störung anlegen“ – ausgeschaltet bekommen Störungen kein automatisches Ticket mehr (Standard: aus, wirkt sofort ohne Neustart)
- Benutzerstamm: Knopf „Nextcloud-Konto anlegen“ beim Feld Nextcloud-Benutzer – legt das Konto in Nextcloud an, verknüpft es und zeigt das Startpasswort einmalig an
- Lagerverwaltung: „Sammelanlegen“ – Ziel wählen, dann Fächer und Plätze per „von–bis“ (z. B. A01–A12 mit je Platz 1–10) in einem Schritt anlegen; vorhandene werden übernommen, so lassen sich auch Plätze in bestehende Fächer ergänzen
- Lagerverwaltung: Lagerorte, Regale und Fächer kopieren – samt Unterknoten und auf Wunsch mehrfach mit Namensreihe (ohne Bestand)
- Infrastruktur: „Sammelanlegen“ (z. B. Presse 1–6 mit je Motor 1–3) und Kopieren einer Anlage samt Unteranlagen, Stammdaten, Partnern und Kostenstelle
- Neues Kostenstellen-Dashboard (Navigation → Kostenstellen bzw. Core-Einstellungen): Stunden je Kostenstelle und Tätigkeit, offene Vorgänge je Kostenstelle, Pflege von Kostenstellen mit Unterstellen und Verantwortlichen
- Tätigkeiten (z. B. Rüsten, Reinigung) – für alle oder je Kostenstelle; in der Zeiterfassung beim Starten, manuellen Erfassen und Bearbeiten wählbar, die Zeit zählt dann auf die Kostenstelle der Tätigkeit
- Neue Seite „Statistik“ mit großen Touch-Kacheln, die Grafana-Dashboards im PDH oder in einem neuen Tab öffnen; Kacheln mit Symbol, Farbe, Kiosk-Ansicht und Aktualisierung unter Core-Einstellungen → Grafana & Statistik
- Docker-Installation: Grafana läuft mit (Port 3000) – mit fertiger Datenquelle „PDH“ (nur lesen, ohne Passwörter und Schlüssel) und dem Dashboard „PDH Übersicht“
- Server-Einstellungen: Zugangsdaten aus der .env.docker (Microsoft 365, Nextcloud, Copilot, DeepL, E-Mail …) mit einem Klick verschlüsselt in die Datenbank übernehmen – danach gelten sie aus der Datenbank und sind in den Reitern bearbeitbar
- Server-Einstellungen: „Verbindung testen“ jetzt auch für Microsoft 365, Nextcloud und DeepL; das Ergebnis bleibt gespeichert und zeigt sich als grüner oder roter Punkt am Reiter
- Server-Einstellungen: Sind Werte übernommen und der Test grün, entfernt „Aus .env.docker entfernen“ sie über den Update-Agenten aus der .env.docker (ohne Klartext-Kopie)
- Server-Einstellungen → Nextcloud: „Nextcloud im PDH-Look“ überträgt Name, Firmenfarbe, Logo und Adresse des PDH in die Theming-App von Nextcloud – direkt oder per herunterladbarem occ-Skript
- Doppel-Absende-Sperre: Ein Doppelklick oder erneutes Absenden legt nichts mehr doppelt an – jedes Absenden trägt einen einmaligen Schlüssel, der nur einmal ausgeführt wird
- Core-Einstellungen: neue Kachel „Nextcloud-Updates“ neben den PDH-Updates – prüft System- und App-Updates von Nextcloud und spielt sie mit einem Klick ein („Alle Apps aktualisieren“, „System-Update“)
- Grafana läuft durch das PDH unter /grafana/ – gleiche Adresse und HTTPS wie das PDH (auch über Cloudflare), Anmeldung automatisch mit dem PDH-Konto

### Geändert
- Neue Datensätze bekommen zeitlich sortierte IDs (UUIDv7) – schnellere Datenbank-Indizes, bestehende IDs bleiben unverändert

### Behoben
- Statistik: Grafana-Kacheln brachen über HTTPS/Cloudflare mit „ERR_SSL_PROTOCOL_ERROR“ ab (Aufruf über Port 3000) – Grafana läuft jetzt unter /grafana/ durch das PDH
- Mein Konto: „Geschäftskonto verbinden“, „Privates Konto verbinden“ und „Teams freigeben“ brachen mit „login.microsoftonline.com hat die Verbindung verweigert“ ab, wenn das PDH eingebettet lief (z. B. in Nextcloud) – die Microsoft-Anmeldung öffnet sich dann in einem neuen Tab

## [0.49.0] – 2026-10-10

### Neu
- Infrastruktur: neuer Anlagentyp „Trockenkammer“ mit Reiter „Bauteile“ – je Gruppe (Motoren, Klappenmotoren, Heizungsstellventile, Heizungspumpen, Torrollen, Tordichtungen) die Anzahl eintragen, dann entsteht je Stück eine Kachel mit Typ aus den Ersatzteilen, NOK, letztem Wechsel und Ursache

### Behoben
- Leitstand im Kachelmodus: Die Auswahl „Wer führt aus?“ schloss sich am Tablet sofort wieder (beim Scrollen oder mit Bildschirmtastatur) – jetzt lässt sich die Person wählen

## [0.48.2] – 2026-10-09

### Behoben
- Push-Benachrichtigungen: Die Karte zum Einschalten auf dem Handy steht jetzt sichtbar unter Mein Konto → Reiter „Konto & Dienste“ und erscheint auch, solange Push noch ausgeschaltet ist – mit Hinweis, wo man es einschaltet

## [0.48.1] – 2026-10-09

### Behoben
- Doppelt eingetragene Seitenadressen (Meldetexte, Leitstand-Einstellung, Schichtplan, Zeiterfassung an Störungen) bereinigt

### Geändert
- Schnellere Seiten: Die Angaben zur angemeldeten Person werden je Seitenaufruf mit einer statt sieben Datenbankabfragen gelesen
- Die Suche nach neuen Störungen und Tickets für Push-Nachrichten ist entlastet, und doppelte Datenbank-Indizes im Schichtplan sind entfernt
- Anwesenheit, Aktivität und Alarm-Prüfung werden nur noch abgefragt, solange der Browser-Tab sichtbar ist – beim Zurückkehren sofort

## [0.48.0] – 2026-10-09

### Neu
- Push-Benachrichtigungen aufs Handy: Neue Störungen und Tickets gehen an die in den Core-Einstellungen gewählten Benutzergruppen – mit Inhalt und „Annehmen“; wer zuerst annimmt, wird zuständig und bei den anderen verschwindet die Nachricht (einschalten unter Mein Konto)
- „Anlage steht“ beim Anlegen von Störungen und Tickets (Assistent, Leitstand, Easy-Mode): Der Push-Alarm wiederholt sich, bis jemand annimmt, und erscheint in der PDH-App bildschirmfüllend

## [0.47.0] – 2026-10-09

### Neu
- Easy-Mode-Meldetexte: Vorformulierte Meldungen im Infrastruktur-Stamm pflegen (Infrastruktur → Easy-Mode: Meldetexte) und je Anlage im neuen Reiter „Meldetexte“ zuordnen – an einer Halle oder Linie zugeordnet gelten sie für alle Anlagen darunter
- Easy-Mode: Die Meldetexte der Anlage erscheinen als große Tasten in der Sprache des Handys; antippen genügt, eigener Text ist dann freiwillig, und ein Text kann „elektrisch/mechanisch“ vorgeben
- Die Reihenfolge der Meldetexte lässt sich im Katalog mit ▲/▼ festlegen; so erscheinen sie auch im Easy-Mode und an der Anlage

### Geändert
- Leitstand: Statt die RFID-Karte zu scannen, wählt man bei Annehmen, Verwerfen und Fertig aus der Liste der Mitarbeitenden aus Instandhaltung und IT, wer ausführt; unter Core-Einstellungen lässt sich die Liste auf angemeldete Terminals beschränken – an anderen Geräten gilt dann weiter die Karte
- Leitstand: Die Knöpfe „Info“ und „Warten“ und das Kommentarfeld im Bearbeitungsfenster entfallen; ein Kommentar ist dort nicht mehr nötig

## [0.46.2] – 2026-10-09

### Geändert
- Easy-Mode: Der Schritt „Wer meldet?“ entfällt – eine Meldung hat nur noch drei Schritte (Was ist zu melden, Art, Zustand der Anlage), ein Name wird nicht mehr abgefragt oder auf dem Handy gespeichert

## [0.46.1] – 2026-10-09

### Geändert
- Leitstand: Ist der Zeitstrahl ausgeblendet, füllen die Kacheln (bzw. die Tabelle) den ganzen restlichen Bildschirm

## [0.46.0] – 2026-10-09

### Neu
- Benutzermenü → Darstellung: neue Einstellung „Zeilenhöhe“ macht die Zeilen in Tabellen und Listen niedriger oder höher (70–150 %), die Schrift passt sich dabei automatisch an; mit „Speichern“ gilt sie auf allen Geräten
- Leitstand: Die offenen Vorgänge erscheinen als große, antippbare Kacheln; in der Kopfzeile lässt sich auf die Tabelle umschalten
- Leitstand: Größe der Kacheln bzw. Tabellenzeilen direkt in der Kopfzeile einstellen (− / +), die Schrift passt sich an
- Leitstand und Dashboard: Der Zeitstrahl lässt sich per Knopf aus- und wieder einblenden; Ansicht, Größe und Zeitstrahl merkt sich das Gerät

## [0.45.0] – 2026-10-08

### Neu
- Server-Einstellungen → Cloudflare-Tunnel: Tunnel-Token und Hostname dort pflegen, mit Schritt-für-Schritt-Anleitung, Installationsskript für Linux und Windows zum Herunterladen und einer Prüfung, ob das PDH über den Tunnel erreichbar ist
- Live-Übersetzung: Im Benutzerstamm einschaltbar (auch im Benutzermenü) – übersetzt Oberfläche und Inhalte jeder Seite live in die Sprache der Oberfläche oder eine frei gewählte Sprache, mit DeepL oder dem Copilot; einzurichten unter Server-Einstellungen → Übersetzung

### Geändert
- Der Copilot weiß, dass eine Störung und das daraus erstellte Ticket derselbe Vorgang sind: Er zählt sie nicht doppelt und nennt beide Links
- Die automatischen Copilot-Vorschläge zeigen das aus einer Störung erstellte Ticket (und umgekehrt) nicht mehr als mögliche Dopplung, und gelöste Fälle erscheinen nicht doppelt als Störung und Ticket
- Der Infrastruktur-Baum und die Anlagenauswahl starten zugeklappt; offen ist nur der Ast, den du gerade geöffnet hast – die übrigen klappen dabei zu

## [0.44.1] – 2026-10-08

### Geändert
- Die Seite „Zuweisung“ zeigt alle offenen Vorgänge ohne Zuweisung – Störungen, Tickets, Aufgaben und Wartungen; zuweisen dürfen die Broker der jeweiligen Art und Administratoren, sonst steht dort, wer zuweist

### Behoben
- Termin im Zeitstrahl verschieben meldete „kein token“, wenn die Anmeldung abgelaufen war – jetzt geht es zur Anmeldung und danach zurück auf die Seite
- Auf der öffentlichen Leitstand-Tafel (ohne Anmeldung) lassen sich Termine im Zeitstrahl nicht mehr verschieben; ein Hinweis erklärt das, Antippen und Doppelklick funktionieren weiter

## [0.44.0] – 2026-10-08

### Neu
- Profilbild: auf „Mein Konto“ (oder im eigenen Benutzerstamm) ein Foto wählen; es erscheint im Chat, in der Kopfleiste, in der Benutzerliste, im Organigramm und bei „Aktivität“ – mit „Benutzer verwalten“ auch für andere

### Geändert
- Systemmeldungen im Chat kommen jetzt von „Service“ mit dem PDH-Logo (bzw. dem Firmenlogo aus dem Branding) statt von „PDH System“; „Service“ taucht nicht in Personen-Auswahllisten auf
- Das automatische Ticket aus einer Störung entsteht erst nach der Zuweisung an eine Person oder Gruppe (bis dahin Hinweis auf der Störung) und übernimmt auch die zugewiesene Gruppe

### Behoben
- Symbole erschienen hinter Cloudflare als leere Kästchen: Die Symbol-Schrift steckt jetzt direkt in der Symbol-Datei, und deren Adresse ändert sich mit jeder Version

## [0.43.0] – 2026-10-08

### Neu
- Prüfpflichten und Gefährdungsbeurteilungen je Anlage (Reiter „Prüfungen & GBU“): Grundlage, Prüfer, Intervall, nächste Fälligkeit, Verantwortliche; „Erledigt erfassen“ mit Ergebnis setzt die nächste Fälligkeit und führt einen Nachweis
- Erinnerung an Prüfpflichten und Gefährdungsbeurteilungen per Chat (PDH System) an die verantwortliche Person bzw. die Admins – zu Beginn des Vorlaufs und noch einmal bei Überfälligkeit
- Überblick „Fällige Prüfungen & GBU“ über alle Anlagen oben auf der Infrastruktur-Seite
- Symbole anpassen (Verwaltung → Symbole): für Navigation, Vorgangsarten, Anlagen-, Lager- und IT-Typen sowie Knöpfe das Symbol aus der Bibliothek (rund 5.900 Symbole, mit Suche) wählen

### Geändert
- Die Infrastruktur-Auswahl zeigt Gebäude, Linie, Anlage und Gerät nur noch als Symbol (Name im Tooltip) statt als englischen Text
- Alle Symbole kommen jetzt vom eigenen Server statt aus dem Internet; PDH sieht auch ohne Internetzugang vollständig aus
- Anlagen-, Lager- und IT-Typen haben einheitliche Symbole statt Emoji

### Behoben
- Die Easy-Mode-Seite zeigte statt des Anlagensymbols dessen technischen Namen

## [0.42.0] – 2026-10-08

### Neu
- Knopf „Easy-Mode testen“ an jeder Anlage (nur Admins): öffnet die Meldeseite wie nach einem QR-Scan, auch wenn der Easy-Mode ausgeschaltet ist; Testmeldungen beginnen mit „TEST:“

## [0.41.0] – 2026-10-08

### Neu
- Der Copilot schlägt bei offenen Störungen und Tickets automatisch Lösungen aus euren Daten vor: bewährte Lösungen aus gelösten Störungen und Tickets (mit Ursache, Maßnahmen und verbauten Teilen) sowie oft benötigte Ersatzteile mit aktuellem Bestand
- Mögliche Dopplungen: Der Copilot zeigt offene Störungen und Tickets, die vermutlich dasselbe beschreiben; gleiche Lösungen erscheinen nur einmal mit Anzahl („2× so gelöst“)
- Gibt es Vorschläge, blinkt der Copilot-Reiter in der Seitenleiste gelb – auf der Störungs- und Ticketseite und während der Behebung; im Assistenten öffnet ein gelber Knopf die Vorschläge, ohne ihn zu schließen
- Easy-Mode: Wer ohne Anmeldung den QR-Code einer Anlage scannt, kann in der Sprache seines Handys eine Störung oder ein Ticket melden – Name, Text, elektrisch/mechanisch, Anlage läuft/steht, fertig; die letzten 5 Meldungen der Anlage stehen darüber, oben führt „Anmelden“ in den Normalmodus
- Meldungen aus dem Easy-Mode werden automatisch ins Deutsche übersetzt; der deutsche Text steht immer über dem Original
- Copilot-Knopf in der Kopfleiste: klappt die Copilot-Hilfen auf und zu (wie der Chat) und blinkt gelb, wenn es Vorschläge gibt

### Geändert
- Auf der QR-Infoseite einer Anlage klappen offene Vorgänge beim Antippen auf; „Beheben“ bzw. „Abschließen“ startet direkt den Abschluss-Assistenten
- Der Easy-Mode lässt sich unter Core-Einstellungen abschalten; dann führen QR-Codes wie bisher zur Anmeldung

## [0.40.0] – 2026-10-08

### Neu
- Alle Listen und Tabellen bedienen sich gleich: Zeile anklicken klappt sie auf, darunter erscheinen „Ansehen“ (Schnellansicht), „Bearbeiten“ und – bei Tickets, Störungen, Aufgaben und Wartungen – „Fertigstellen“; ein Doppelklick öffnet den Datensatz direkt

### Geändert
- Die Knopfleiste „Auswahl öffnen / kopieren / erledigen“ über den Listen und die Knöpfe am Zeilenende entfallen; IT-Status, Zeiteinträge bestätigen/bearbeiten/löschen und „Unteranlage hinzufügen“ stehen jetzt in der aufgeklappten Zeile
- Im Anlagenbaum klappt der Pfeil die Unteranlagen auf, ein Klick auf die Zeile zeigt die Aktionen
- Der Abschluss-Knopf für Tickets heißt in Listen und auf dem Board jetzt „Abschließen“ statt „Bearbeiten“

### Behoben
- Im Anlagenbaum verlor eine Zeile nach dem Anspringen aus dem Schnellzugriff ihren Hintergrund
- Die Auswahl „Zugewiesene Gruppe“ bei Aufgaben, Tickets, Störungen, Wartungen und Projekten blieb bei „Lädt…“ stehen; eine Gruppe ließ sich nicht auswählen

## [0.39.1] – 2026-10-08

### Behoben
- Auf der Detailseite einer Störung war die linke Navigation leer; außerdem fehlten dort Sprache, Branding und berechtigungsabhängige Knöpfe

## [0.39.0] – 2026-10-08

### Neu
- Infrastruktur: neuer Reiter „Stammdaten“ auf jeder Anlage mit allen Angaben in einem Formular – auch Typ, übergeordnetes Element (Umhängen samt Unteranlagen), Beschreibung und „In Betrieb seit“
- Änderungen an den Stammdaten einer Anlage stehen jetzt im Reiter „Änderungen“

### Geändert
- Die Infrastruktur-Detailseite hat den Aufbau der übrigen Stammdaten (Kopfzeile, Übersicht mit allen Angaben, einheitliche Reiter)
- Neue Anlagen und Unteranlagen werden mit Name, Typ und Standort angelegt und öffnen sich danach direkt im Reiter „Stammdaten“ zum Vervollständigen
- Der Anlagenbaum ist stabil sortiert (Gebäude, Linien, Anlagen, Geräte, jeweils nach Name)

### Behoben
- Beim Anlegen einer Anlage in der Infrastruktur erschien die komplette Seite noch einmal verschachtelt im Anlagenbaum
- Speichern einer Anlage löschte Modell und Beschreibung

## [0.38.2] – 2026-10-08

### Behoben
- Tickets, Störungen, Aufgaben und Wartungen ohne verantwortliche Person (oder Wartungen ohne Termin) fehlten in der Liste, im Board und im Dashboard des Moduls, obwohl der Leitstand sie zeigte

## [0.38.1] – 2026-10-08

### Behoben
- Wieder geöffnete Tickets, Störungen und Aufgaben blieben als „archiviert“ markiert: Sie fehlten in der Liste und in den Dashboard-Zählern, erschienen aber im Leitstand und bei der Zuweisung – jetzt hebt das Wiederöffnen die Archivierung auf, bestehende Fälle werden beim Update bereinigt
- Dashboard-Kacheln führen direkt in die passende Liste („Alle offenen“, „Fällig“, „Meine“) statt auf die Startseite des Moduls
- „Wartung fällig“ zählte Wartungen nicht mit, die heute erst später fällig sind; der neue Chip „Fällig“ unter Aufträge zeigt genau diese
- „Meine Aufgaben“ zählt jetzt wie der Chip „Meine“ auch Aufgaben, für die man verantwortlich ist
- Die Tabelle der offenen Vorgänge im Dashboard zeigt keine Wartungen mehr, deren Vorlauf noch nicht begonnen hat (wie Leitstand und „Alle offenen“); im Zeitstrahl stehen sie weiter

## [0.38.0] – 2026-10-07

### Sicherheit
- Passwortwechsel-Zwang: Wer sich mit einem vom Administrator vergebenen Passwort zum ersten Mal anmeldet oder dessen Passwort im Benutzerstamm geändert wurde, muss zuerst ein eigenes Passwort vergeben – vorher gibt es keine Anmeldung, auch nicht über Schnittstelle oder Override
- Passwort-Richtlinie in den Server-Einstellungen (Reiter „Passwörter“): Mindestlänge, Groß-/Kleinbuchstaben, Ziffer, Sonderzeichen, Höchstalter und Sperre für die letzten Passwörter; gilt sofort für jedes neue Passwort
- Benutzerstamm: „Passwortwechsel verlangen“ bzw. „Erlassen“ und Anzeige, ob ein Wechsel aussteht oder das Passwort abgelaufen ist; alle Passwortfelder zeigen die geltenden Regeln

## [0.37.0] – 2026-10-07

### Neu
- Neue Seite „Zuweisung“ nur für Broker: Alle offenen Vorgänge ohne Zuweisung ihrer Arten laufen hier auf und lassen sich direkt einer Person, einer Gruppe oder sich selbst zuweisen
- Wer etwas anlegt (Ticket, Störung, Aufgabe, Projekt, Wartung, KVP-Vorschlag), bekommt von PDH-System eine Bestätigung im Chat – mit dem Zuständigen oder den Brokern, an die der Vorgang ging
- Wer beim Anlegen als Zuständige/r, Verantwortliche/r oder über eine Gruppe eingetragen wird, bekommt den Hinweis „Neu für dich“
- KVP: Ideen reicht man jetzt Schritt für Schritt im Einreich-Assistenten ein (Thema → Was stört? → Vorschlag → Wo? → Team → Einreichen), auch über „Neu anlegen“ → „KVP-Idee“
- KVP: „Nächster Schritt“ öffnet einen Assistenten, der durch Bewertung, Entscheidung, Plan, Maßnahmen, Wirksamkeit und Standardisierung führt und weiterschaltet – oder nur speichert („geht noch weiter“)
- Tickets, Störungen, Aufgaben und Wartungen haben einen Reiter „Regeln & Einstellungen“ mit den Regeln des Moduls, der Standard-Frist und den Brokern

### Geändert
- Tickets, Störungen, Aufgaben und Wartungen sind aufgebaut wie der KVP: Dashboard mit Kennzahlen des Jahres, Ablauf, 12-Monats-Verlauf und „Braucht Aufmerksamkeit“, Liste mit Filter-Chips und Anzahl, Board nach Status
- Die Ansichten „Anstehend“ und „Liste“ sind in der neuen Liste mit Filter-Chips aufgegangen; das Archiv ist dort der Chip „Archiv“ – alte Links führen in den passenden Chip
- Wartung: „Anstehend“ heißt jetzt „Aufträge“; Aufträge vor dem Vorlauf stehen unter dem Chip „Geplant“, Jahresplan, Pläne, Rundgänge und Checklisten bleiben eigene Reiter

### Behoben
- Ohne Zuweisung angelegte Tickets, Störungen, Aufgaben und Wartungen gingen nur aus dem Leitstand an die Broker – jetzt auch über „Neu anlegen“, die Modulseiten und die Schnittstelle
- Aufgaben: Die Ansichten „Anstehend“ und „Archiv“ blieben leer, weil sie nach einer nicht mehr vorhandenen Einzelzuweisung fragten – jetzt mit allen Zuständigen und der Gruppe
- Tickets und Störungen: „Ohne Zuweisung“ zeigte auch Vorgänge, die einer Gruppe zugewiesen sind
- Leitstand: Vorgänge, die einer Gruppe zugewiesen sind, zeigen die Gruppe als zuständig und gelten bei „Nur ohne Zuständigen“ nicht mehr als frei

## [0.36.0] – 2026-10-07

### Neu
- KVP (Kontinuierliche Verbesserung): Jede/r reicht Verbesserungsideen mit Ist-Zustand, Vorschlag, Nutzen, Anlage und Team ein; alle sehen alle Vorschläge
- KVP-Dashboard mit Kennzahlen des Jahres (Vorschläge je Mitarbeitende/n, Annahmequote, Zeit bis zur Entscheidung, Beteiligung, Einsparungen, Prämien), Ablauf, 12-Monats-Verlauf, Themen, Nutzen-Aufwand-Matrix und allem Überfälligen
- KVP-Ablauf nach PDCA mit Bewertungsbogen (Nutzen/Aufwand, Einsparung, Amortisation), Maßnahmenplan (wer · was · bis wann), Wirksamkeitsprüfung und Standardisierung; PDCA-Board mit allen Vorschlägen in Umsetzung
- KVP-Regeln werden eingehalten: Rückmeldung binnen Frist (einstellbar, mit täglicher Erinnerung), jede Entscheidung mit Begründung, nächster Schritt erst, wenn alles Nötige erfasst ist; Einreichende bekommen jede Entscheidung im Chat
- KVP-Vorschlag als A3-Report drucken; Vorher/Nachher-Fotos, Kommentare, Kategorien, Feldsätze, Verknüpfungen und Historie wie bei Tickets
- Dashboard-Kachel „KVP-Vorschläge“ und Schnellaktion „Idee einreichen“; neues Recht „KVP steuern“
- „Neue Zuweisung“ heißt jetzt „Neu anlegen“; im Auswahlschritt gibt es zusätzlich die Kachel „KVP-Idee“ zum schnellen Einreichen (für alle)
- Chat steht jetzt in der Gruppe „Instandhaltung“ und als Knopf im Leitstand (neben dem Chat-Schnellzugriff im persönlichen Dashboard)
- Persönliches Dashboard: neue Tabelle der offenen Vorgänge oberhalb des Zeitstrahls; Tabelle und Zeitstrahl haben wie im Leitstand eine eigene Auswahl (Vorgangsarten, nur überfällig, nur ohne Zuständigen), die der Browser sich merkt
- Netzlaufwerke (Core-Einstellungen): SMB/CIFS- und NFS-Freigaben oder bereits eingebundene Pfade einbinden, testen und durchsuchen; Passwörter verschlüsselt, automatisches Wiederverbinden mit Hinweis an die Administratoren
- Netzlaufwerk als globaler Datenspeicher, für Import & Export (Pfad-Vorschläge), für die Datensicherung und als Dokumentenablage, die alle Anhänge nach Vorgang sortiert ablegt

### Geändert
- Tickets, Störungen und Aufgaben sehen aus und arbeiten wie die Wartung: neue Startansicht „Anstehend“ mit Karten nach Dringlichkeit (Überfällig, Heute, Nächste 7 Tage, Später, Ohne Termin), Filtern „Nur meine“, „Ohne Zuweisung“, Priorität, Anlage und Projekt und einem Knopf direkt zum Abschluss-Assistenten
- Neues Archiv für Tickets, Störungen und Aufgaben mit Suche (auch in der Lösung), Anlage, Zeitraum und aufklappbarer Lösung & Ursache; die bisherige Tabelle bleibt als Ansicht „Liste“
- Detailseiten von Tickets, Störungen und Aufgaben zeigen oben eine „Bearbeiten/Beheben/Erledigen“-Karte wie der Wartungsauftrag
- Projekte als übergeordnetes Aufgabenmanagement: Projektkarten mit Fortschrittsbalken, überfälligen Aufgaben und nächstem Termin; im Projekt stehen die Aufgaben gruppiert als Karten mit „Erledigen“

## [0.35.1] – 2026-10-06

### Behoben
- Checklisten: „+ Punkt“ lud die Seite neu und sprang zurück auf „Anstehend“ – jetzt bleibt die Checkliste offen und man legt Punkt für Punkt weiter an
- Checklisten: „Anlegen“ einer neuen Checkliste brach mit „Bitte einen Namen angeben“ ab, obwohl ein Name eingetragen war
- Wartungsplan-Editor, HMI-Zugang an der Anlage und „Neues Projekt“: Speichern lud die Seite nicht mehr zusätzlich neu (konnte Eingaben verwerfen)

## [0.35.0] – 2026-10-06

### Neu
- Wartung: neuer Reiter „Archiv“ (ersetzt „Erledigt“) mit Suche, Filter nach Anlage (inkl. Unteranlagen und Rundgang-Stationen), Plan, Zeitraum, Status und „nur mit Abweichungen“, seitenweise
- Im Archiv klappt die erledigte Checkliste direkt auf – jeder Punkt mit Wert, Vorgabe, wer ihn wann erfasst hat und den Fotos; dazu Ausführende, Fotozahl und Protokoll-PDF je Wartung
- Seitenleiste „Aktivität“: wer gerade online ist und wer nicht – mit „zuletzt gesehen“
- Neue Zuweisung: Broker sehen je Art die offenen, noch nicht zugewiesenen Vorgänge als Schnelleinstieg

### Geändert
- Checklisten: Nach „+ Punkt“ bleibt das Anlegen offen – Art, Pflicht und Einheit bleiben stehen, der Cursor steht im Namensfeld; Messwert-Vorgaben (Einheit, Soll, Min, Max) gibt man gleich beim Anlegen ein
- „Dashboard anpassen“ und „Navigation anpassen“ stehen jetzt im Benutzermenü
- Neue Zuweisung: Aufgaben und Wartungen legen dort nur Administratoren und Manager an

### Behoben
- Übersprungene Wartungen erscheinen im Archiv mit dem Datum des Überspringens statt mit dem Fälligkeitsdatum

## [0.34.0] – 2026-10-06

### Neu
- Kontrollrundgang: ein Wartungsplan, der nacheinander mehrere Anlagen (Stationen) mit je einer Checkliste abfragt – neuer Reiter „Rundgänge“ in der Wartung
- Rundgang anlegen mit Bereich, Stationen in fester Reihenfolge und „Alle Unteranlagen“ zum schnellen Übernehmen; jede Station kann ihren eigenen Takt haben
- Im Rundgang zeigt der Assistent „Station 2 von 5“ mit Anlagenname; „Mangel melden“ legt direkt eine Störung zu dieser Anlage an und kehrt in den Rundgang zurück
- Auftrag und Wartungsprotokoll (PDF) gliedern die Ergebnisse eines Rundgangs nach Stationen
- Einführungs-Rundgang durch PDH: Sprechblasen erklären Navigation, Dashboard, Melden, Seitenleiste und Konto – Start im Benutzermenü, im Handbuch und beim ersten Besuch auf dem Dashboard
- Eigene Rundgänge für Dashboard, Störungen und Wartung über „Rundgang für diese Seite“ im Hilfe-Reiter

## [0.33.0] – 2026-10-06

### Neu
- Wartung neu aufgebaut mit den Reitern Anstehend, Jahresplan, Pläne, Erledigt und Checklisten
- Anstehend zeigt die Wartungen nach Überfällig, Heute, Nächste 7 Tage und Demnächst, jeweils mit Knopf „Durchführen“ und dem Stand der Checklisten
- Jahresplan: je Plan eine Zeile mit allen erledigten, offenen und geplanten Terminen des Jahres
- Ein Wartungsplan kann mehrere Checklisten mit eigenem Takt haben (z. B. „bei jedem Termin“, „alle 2 Wochen“, „monatlich“); fällige Checklisten werden bei der Durchführung zu einer Liste zusammengeführt
- Neuer Plan-Editor auf eigener Seite: Intervall frei wählbar („alle N Tage/Wochen/Monate/Jahre“), Vorlauf in Tagen, Vorschau der nächsten Termine und „zuletzt abgefragt“ je Checkliste
- Foto je Checklistenpunkt direkt im Abschluss-Assistenten – in der App, am Handy und am Leitstand
- Das Wartungsprotokoll (PDF) liegt jetzt auch am Auftrag und in der Liste „Erledigt“

### Geändert
- Der Wartungsauftrag ist schlanker: ein Knopf „Wartung durchführen“ öffnet den Assistenten, darunter das Ergebnis je Checkliste mit Werten, Fotos und Protokoll
- Bestehende Pläne, Checklisten und offene Aufträge wurden übernommen; Checklistenpunkte mit eigenem Intervall wurden zu eigenen Checklisten mit diesem Takt
- Leitstand: Wartungen erscheinen ab „Fälligkeit minus Vorlauf“ des Plans
- Ruhende Pläne haben keinen offenen Auftrag mehr; beim Aktivieren wird er wieder angelegt

### Behoben
- Ein beschädigtes Foto lässt das Fertigmelden nicht mehr scheitern – es fehlt dann nur im Protokoll
- Verlaufseinträge aus der Wartungsausführung erscheinen jetzt im Verlauf des Auftrags

## [0.32.1] – 2026-10-06

### Behoben
- Fertigmelden ging in der App nicht für Mitarbeitende ohne die Rollenrechte „Wartung bearbeiten/abschließen“ (der Assistent brach mit „Keine Berechtigung“ ab): Wer zugewiesen, verantwortlich oder in der zugewiesenen Gruppe ist, darf jetzt fertig melden; Aufträge ohne Zuweisung darf jeder übernehmen (außer Betrachtern). Gilt auch für Tickets, Störungen und Aufgaben
- Leitstand: Bei täglichen und wöchentlichen Plänen erschien der Folgeauftrag sofort wieder als „Offen“, als wäre nicht abgeschlossen worden – die Tabelle blendet Wartungen jetzt aus, bis sie fällig sind (Knopf „Geplante Wartungen“ zeigt sie)

### Geändert
- Wartungsauftrag: Die eigene Zeiterfassung im Auftrag entfällt – die Arbeitszeit fragt der Abschluss-Assistent ab
- Abschluss-Assistent: Die Meldung „… abgeschlossen. Nächster Termin: …“ bleibt länger stehen

## [0.32.0] – 2026-10-06

### Neu
- Wartungspläne legen ihren Auftrag selbst an: Zu jedem aktiven Plan gibt es immer genau einen offenen Auftrag – nach dem Anlegen des Plans, nach jedem Abschluss oder Überspringen und stündlich für Pläne, denen einer fehlt
- Wartungsplan: „Nächster Termin nach Durchführung“ wählbar – ab Durchführung (Erledigt-Datum + Intervall) oder fester Rhythmus (Fälligkeit + Intervall); Monate, Quartale und Jahre werden kalendergenau gerechnet
- Automatische Rückmeldung nach jeder erledigten Wartung: Verantwortliche, Zugewiesene und Ersteller bekommen eine Chat-Nachricht mit Ergebnis, Abweichungen der Checkliste, Bemerkung, nächstem Termin und Link zum Protokoll
- Nach „Fertig“ zeigt der Abschluss-Assistent den nächsten Termin; im erledigten Auftrag führt ein Link zum Folgeauftrag
- Der Abschluss-Assistent schlägt bei Wartungen die geplante Dauer des Plans als Arbeitszeit vor
- Zeitstrahlen (Dashboard, Leitstand, Projekte): Erledigte erscheinen als ein Sammler „✓ Erledigt (Anzahl)“, offene und laufende als einzelne Balken; Klick auf den Sammler zeigt die Erledigten einzeln

### Geändert
- Wartungsauftrag: Die doppelte Erfassung von Maßnahmen und Ersatzteilen im Ausführungsformular entfällt – Material und Kommentar (als Maßnahme) fragt der Abschluss-Assistent ab
- Wartung: Der Knopf „Erzeugen“ heißt „Fehlende Aufträge anlegen“ und legt keine doppelten Aufträge mehr an
- Ändert man die nächste Fälligkeit eines Plans, zieht der noch nicht begonnene Auftrag mit

### Behoben
- „Aufträge erzeugen“ legte bei jedem Klick erneut Aufträge für alle fälligen Pläne an (Duplikate), und nach dem Abschluss konnte ein zweiter Auftrag für denselben Termin entstehen
- Am Leitstand verworfene Wartungen ließen den Plan ohne nächsten Termin
- Wartungsliste: Der Zähler „Aktuell“ zählt jetzt offene, laufende und wartende Aufträge

## [0.31.2] – 2026-10-06

### Behoben
- Leitstand: Nach dem Fertigmelden einer Wartung erschien sofort der nächste Auftrag des Wartungsplans mit gleichem Namen als „Offen“ – es sah aus, als wäre nicht abgeschlossen worden. Geplante Wartungen, die erst in mehr als 7 Tagen fällig sind, blendet die Tabelle jetzt aus (Knopf „Geplante Wartungen“ zeigt sie)
- Wartung: Der nächste Auftrag eines Plans fragt die Checkliste wieder ab – fällig ist ein Punkt, wenn sein Intervall bis zum Fälligkeitstag des Auftrags abgelaufen ist, nicht erst ab heute gerechnet

## [0.31.1] – 2026-10-06

### Behoben
- Leitstand: Wartungen (und andere Vorgänge) lassen sich per Karte wieder fertig melden, auch wenn die Rolle der Person kein Bearbeiten-Recht für diese Vorgangsart hat – bisher brach der Abschluss-Assistent mit „Keine Berechtigung“ ab

## [0.31.0] – 2026-10-06

### Neu
- Anlagen haben einen Reiter „HMI“: Bediengeräte per VNC direkt im Browser ansehen oder bedienen – ohne eigenes VNC-Programm, auch ohne Internet
- Drei Stufen über Berechtigungen: HMI ansehen, HMI bedienen und HMI-Verbindungen einrichten; je HMI lässt sich das Bedienen ganz sperren
- Jeder HMI-Zugriff wird mit Name, Zeit, Modus und Dauer protokolliert und ist unter „Letzte Zugriffe“ sichtbar
- Wartung fertig melden: Im Abschluss-Assistenten ist die Checkliste jetzt der erste Schritt – Punkt für Punkt, auch beim Fertigmelden per Karte am Leitstand; ohne ausgefüllte Pflichtpunkte kein Abschluss
- Beim Abschluss einer Wartung legt PDH das Wartungsprotokoll als PDF an der Anlage ab (Dokumente) – mit Firmenlogo, Datum, Ausführenden, Checkliste samt Bewertung und Fotos, Maßnahmen und Ersatzteilen
- Neuer Status „Wartet“ auch für Störungen, Wartungen und Aufgaben (bei Tickets bisher „Ausstehend“); wartende Vorgänge bleiben in allen Listen offen

### Geändert
- Leitstand: „Warten“ per Karte setzt den Vorgang jetzt wirklich auf „Wartet“ – sichtbar in allen Listen, nicht nur am Leitstand; bestehende Wartestellungen werden beim Update übernommen
- Leitstand: Nach „Geht noch weiter“ steht der Vorgang wieder auf „In Arbeit“ statt weiter „Wartet“ anzuzeigen
- Leitstand: Wer per Karte fertig meldet (Instandhaltung/IT), kann im Abschluss-Assistenten auch abschließen – der Knopf „Fertig“ ist nicht mehr gesperrt, wenn der Rolle das Abschluss-Recht fehlt

### Sicherheit
- Beim HMI-Fernzugriff bleibt das VNC-Passwort auf dem Server; im Modus „ansehen“ verwirft der Server Tastatur, Maus und Zwischenablage

## [0.30.0] – 2026-10-06

### Neu
- Leitstand: Wer ohne Anmeldung einen Vorgang oder eine Anlage öffnet (Info/Öffnen, Doppelklick im Zeitstrahl), scannt seine RFID-Karte und wird nur kurz angemeldet
- Der Kurzzugang endet bei Inaktivität nach der Override-Zeit, mit „Zurück zum Leitstand“, beim Schließen des Browsers oder sobald der Leitstand wieder aufgerufen wird

## [0.29.0] – 2026-10-06

### Neu
- Leitstand: Die Tabelle zeigt bei allen Vorgängen den Ersteller bzw. Melder (zwischen „Fällig“ und „Zuständig“)

### Geändert
- Leitstand: Jede zweite Tabellenzeile ist farblich abgesetzt, damit sich die Zeilen leichter lesen lassen
- Leitstand: Ein Klick auf eine Tabellenzeile klappt die Werkzeuge (Annehmen, Fertig, Verwerfen, Warten, Info) direkt unter der Zeile auf statt unterhalb des Zeitstrahls; erneuter Klick klappt sie zu

## [0.28.0] – 2026-10-06

### Neu
- Leitstand: Tabelle und Zeitstrahl haben je eine eigene Auswahl, was angezeigt wird – Störungen, Tickets, Wartung, Aufgaben, nur überfällige oder nur Vorgänge ohne Zuständigen

### Geändert
- Leitstand: Die Tabelle der offenen Vorgänge steht jetzt über dem Zeitstrahl

## [0.27.0] – 2026-10-06

### Neu
- Wartung abschließen: Die Checklistenpunkte werden Schritt für Schritt abgearbeitet – ein Punkt auf einmal mit Fortschritt, Zurück/Weiter und Übersicht am Ende
- Jeder Checklisten-Schritt wird beim „Weiter“ gespeichert; nach einer Unterbrechung geht es am offenen Punkt weiter
- Zu einem Wartungsauftrag lässt sich eine weitere Checklisten-Vorlage auswählen – auch bei Aufträgen ohne Wartungsplan

## [0.26.0] – 2026-10-05

### Neu
- Copilot in der Seitenleiste beantwortet Fragen zu euren Daten: Er sucht selbst in Störungen, Tickets, Aufgaben, Wartungen, Projekten, Anlagen, Ersatzteilen und Messwerten – nur mit den Rechten der fragenden Person, ohne etwas zu ändern und ohne Internet
- Copilot-Antworten enthalten anklickbare Links zu den gefundenen Vorgängen, Anlagen und Teilen; darunter steht, welche PDH-Daten benutzt wurden
- Drucker (Navigation → Drucker): Zebra ZT4xx, Dymo LabelWriter (über DYMO Connect am PC) und Netzwerkdrucker (IPP oder RAW 9100) einrichten, prüfen – mit Zustand, Papier, Farbband bzw. Toner – und einen Testdruck senden
- Etiketten-Druckansicht: „Direkt drucken“ schickt die Etiketten ohne Druckdialog an einen eingerichteten Drucker
- Jede Import- und Export-Verbindung hat eine eigene Seite mit Übersicht, Prüfungen, Zuordnungen, Vorlagen und Werkzeugen – erreichbar per Klick auf den Namen in der Liste
- „Jetzt prüfen“ auf der Verbindungsseite testet Einstellungen, Erreichbarkeit, TLS, Anmeldung, Abfragen, Zuordnungen und Hintergrund-Abrufe und zeigt jedes Ergebnis mit Grund
- Import-Verbindungen können mehrere benannte Abfragen haben (SQL, Web-Endpunkt, Modbus-Register, OPC-UA-Knoten, Excel-/CSV-Zeile), jede mit eigenem Intervall und Ergebnistabelle; Spalten lassen sich direkt einer Anlage zuordnen
- Eingebaute Abfrage-Vorlagen je Verbindungstyp sowie eigene Vorlagen, die Abfragen und Zuordnungen auf eine gleichartige Verbindung übertragen
- Anmeldeseite: „Passwort vergessen?“ schickt einen Link an die hinterlegte E-Mail-Adresse, mit dem man ein neues Passwort vergibt – er gilt eine Stunde und nur einmal
- Mein Konto → Konto & Dienste: eigenes Passwort ändern (mit dem aktuellen Passwort) bzw. ein erstes Passwort festlegen, wenn man sich bisher nur per Karte oder Microsoft anmeldet
- QR-Codes für jede Anlage und jedes IT-Asset: Scannen öffnet eine Infoseite fürs Handy mit allen offenen Störungen, Tickets, Aufträgen und Wartungen – antippen zum Bearbeiten – und Knöpfen zum Neuanlegen mit vorbelegter Anlage bzw. vorbelegtem Gerät
- QR-Etiketten für Anlagen und IT-Assets drucken – einzeln, für eine Anlage samt Unteranlagen oder für den ganzen Bestand, in allen Etikettenformaten und direkt auf eingerichtete Drucker
- Vorgänge können einem IT-Asset zugeordnet werden; das Gerät steht im Reiter Verknüpfungen
- Wer ohne Anmeldung einen QR-Code scannt, kommt nach der Anmeldung direkt auf die gescannte Seite
- Leitstand: „Fertig“ startet nach dem Scannen der RFID-Karte den geführten Abschluss-Assistenten (Material, Zeit, wer war dabei, Kommentar) im Namen der Person mit der Karte – statt nur eines Kommentarfelds

### Geändert
- Handbuch: neue Kapitel „Drucker“ und „Import, Export & Verbindungsseite“; die Kontexthilfe auf Import und Export zeigt jetzt das neue Kapitel

### Behoben
- Die Anmeldung mit RFID-Karte auf der Anmeldeseite leitete ohne bestehende Sitzung zum Leitstand um, statt anzumelden
- Kostenstelle: Auf der Anlagen-Detailseite, beim Anlegen von Anlagen und im Benutzerstamm war die Auswahl leer – Speichern löschte dort die hinterlegte Kostenstelle. Die Liste wird jetzt überall geladen
- Ticket-Liste: Den Status über die Auswahl in der Zeile zu ändern schlug fehl und ersetzte danach die ganze Zeile; jetzt wird der Status gespeichert und nur die Statusanzeige aktualisiert
- Wartungsauftrag bearbeiten: Speichern auf der Detailseite schlug fehl
- Anlage, Beteiligte und Gruppe an Tickets, Störungen, Wartungen, Aufgaben und Projekten ließen sich hinter manchen Proxys (Cloudflare, Nginx) nicht speichern
- Neu angelegte Wartungspläne trugen als Ersteller den ältesten Benutzer statt der Person, die sie angelegt hat

### Sicherheit
- Wartungspläne anlegen, bearbeiten, vormerken und „alle wiederherstellen“ war ohne Anmeldung möglich; diese Aktionen erfordern jetzt eine Anmeldung und beachten die Abteilungs-Sicht
- Die Karte für die Fertigmeldung am Leitstand gilt nur für den gewählten Vorgang und höchstens 20 Minuten; sie ist keine Anmeldung am Terminal
- Links zum Zurücksetzen des Passworts werden nur aus der eingestellten öffentlichen Adresse gebaut, sind nur als Prüfsumme gespeichert und pro Konto und Adresse begrenzt; die Seite verrät nicht, ob ein Konto existiert

## [0.25.0] – 2026-10-02

### Neu
- Navigation und rechte Seitenleiste lassen sich am Innenrand per Ziehen in der Breite ändern; der Browser merkt sich die Breite, Doppelklick stellt den Standard wieder her, mit der Tastatur über die Pfeiltasten

## [0.24.0] – 2026-10-02

### Neu
- Ersatzteile: Datenblatt und Benutzerhandbuch aus dem Internet holen – im Reiter Dokumente auf „Datenblatt & Handbuch suchen“. Gesucht wird zuerst beim Hersteller (mit Claude-Websuche, wenn ein Anthropic-Schlüssel hinterlegt ist, sonst über DuckDuckGo); angezeigt werden nur Adressen, hinter denen wirklich ein PDF liegt
- Übernommene Dokumente werden im PDH gespeichert und tragen das Etikett „Datenblatt“ oder „Benutzerhandbuch“, den Stand und einen Link zur Quelle; eine PDF-Adresse lässt sich auch direkt einfügen
- „Aktualisieren“ (je Dokument oder „Alle aktualisieren“) lädt die Dokumente erneut von der Quelle und ersetzt sie nur, wenn der Anbieter eine neue Fassung hat – vermerkt im Verlauf des Teils
- Server-Einstellungen → Copilot: „Verbindung testen“ zeigt, mit welchem Anthropic-Schlüssel der Server gerade läuft und welcher eingestellt ist (maskiert, mit Quelle), ob ein Neustart fehlt, ob eine Umgebungsvariable den Schlüssel aus der Datenbank verdeckt – und prüft kostenlos, ob Anthropic den Schlüssel annimmt und das Modell verfügbar ist

### Behoben
- Server-Einstellungen: Leere Einträge von außen (z. B. `PDH_COPILOT_ANTHROPICKEY=` in `.env.docker` aus dem Installationsskript) verdeckten den in der Datenbank gespeicherten Wert; sie zählen jetzt nicht mehr als gesetzt
- Copilot: Ein Anthropic-Schlüssel aus einer Umgebungs- bzw. .env-Datei mit Leerzeichen, Windows-Zeilenende oder Anführungszeichen wurde unverändert gesendet und von Anthropic mit „HTTP 401 – API key is invalid“ abgelehnt; diese Zeichen werden jetzt entfernt. Die Fehlermeldung bei 401 sagt genauer, wo der Schlüssel zu prüfen und einzutragen ist

## [0.23.0] – 2026-10-02

### Neu
- Mehrsprachigkeit, Stufe 3: Erstellungs-Assistent und Abschluss-Assistent sind übersetzt – alle Schritte, Hinweise, Schnellwahlen, Zusammenfassung und Prüfmeldungen, auch im Leitstand-Modus
- Rückmeldungen des Servers beim Abschließen (z. B. „Bitte die Arbeitszeit erfassen.“, „Ticket abgeschlossen.“) und beim Anlegen im Leitstand (Prüffehler, Verteilung an die Broker) kommen in der eingestellten Sprache

### Geändert
- Der Leitstand übernimmt die Sprache des Browsers bzw. der zuletzt gewählten Flagge und setzt sie auch für Datumsangaben im Assistenten

## [0.22.0] – 2026-10-02

### Neu
- Ersatzteil-Reservierung: Teile für Störungen, Tickets, Aufgaben und Wartungen werden reserviert und dabei sofort vom gewählten Lagerplatz abgebucht. Liegt dort nicht genug, wird abgelehnt und die freie Menge genannt
- Reservierte Mengen stehen in Klammern neben dem Bestand: in der Ersatzteilliste, beim Teil (Kopf, Kennzahl, je Lagerplatz), in der Seitenleiste und bei der Teilesuche im Abschluss-Assistenten
- Nicht benötigte Mengen lassen sich zurückbuchen, ganz oder teilweise, an der Position im Vorgang, im Abschluss-Assistenten oder beim Teil in der neuen Karte „Reservierungen“ (Reiter Lager & Bewegungen)
- Beim Abschluss wird die Reservierung zum Verbrauch des Vorgangs, ohne doppelte Abbuchung. Wird ein Vorgang verworfen, archiviert oder gelöscht, gehen offene Reservierungen automatisch an ihren Lagerplatz zurück
- Neue Bewegungsarten „Reservierung“ und „Rückbuchung Reservierung“ in der Bewegungsliste. Bei den Verknüpfungen eines Vorgangs stehen reservierte Teile als „reserviert“
- Zeitstrahlen (Dashboard, Leitstand, Projekte) zeigen den Zustand am Rand der Balken: laufend gelb, fällig rot, überfällig rot blinkend, nicht zugewiesen lila, beendet grün umrandet und ausgegraut; Legende unter jedem Zeitstrahl, Zustand auch im Tooltip
- Theming: neue Karte „Zeitstrahl“ unter Server-Einstellungen → Erscheinungsbild – Farbe je Zustand, Ausgrau-Farbe, Randstärke, Randhelligkeit, Balkenhöhe und ab wann „fällig“ gilt, mit Live-Vorschau
- Broker auch für Aufgaben und Wartungen: im Benutzerstamm „Broker für Aufgaben“ und „Broker für Wartungen“; im Leitstand angelegte Aufgaben und Wartungen gehen wie Tickets und Störungen an diese Broker
- Mehrsprachigkeit, Stufe 2: Dashboard und alle Dashboard-Widgets sind übersetzt (Begrüßung, Kennzahlen, Listen, Status, Schweregrad, Wochentage, Schnellaktionen, Widget-Katalog)

### Geändert
- Alle Wege zum Anlegen von Tickets, Störungen, Aufgaben und Wartungsaufträgen öffnen jetzt den Erstellungs-Assistenten: auch die Dashboard-Schnellaktionen „Störung melden“ und „Ticket anlegen“, das + bei den Aufgaben eines Projekts (Aufgabe gehört gleich zum Projekt) und Links mit `?create=`. Die alten Eingabeformulare der Listen sind entfernt; die Kostenstelle kommt wie bisher aus der Anlage
- Leitstand: Anlegen über denselben Assistenten im Leitstand-Modus – Art → Was? → Wo? → Wer meldet? → Wann? → Anlegen. Es wird dort nichts zugewiesen; der Vorgang geht an die Broker, und der Assistent zeigt, an wen
- „Ersatzteil vormerken“ heißt jetzt „Ersatzteil reservieren“. Bereits vorgemerkte Teile sind noch nicht abgebucht und werden wie bisher erst beim Abschluss gebucht
- Manuelle Buchungen können keine Reservierungsarten buchen; Reservierungen entstehen nur über Vorgänge

### Behoben
- Leitstand: In der Wartungsauswahl fehlte bei offenen Wartungen der Knopf „Start“ – alle wurden als „In Arbeit“ angezeigt, weil der angezeigte Status („Offen“) statt des Statuscodes verglichen wurde
- Copilot mit Anthropic-Schlüsseln ohne Workspace-Bindung brach mit „HTTP 400 – … must include the anthropic-workspace-id header“ ab. Neue Einstellung „Anthropic-Workspace-ID“ (Server-Einstellungen → Copilot, `PDH_COPILOT_ANTHROPICWORKSPACE`) sendet die ID mit; die Fehlermeldung verweist jetzt direkt darauf
- Dashboard-Kopfzeile: Das Datum zeigte immer „Mo“ und den englischen Monatsnamen; jetzt mit richtigem Wochentag in der eingestellten Sprache

## [0.21.0] – 2026-10-01

### Neu
- Erstellungs-Assistent im Stil des Abschluss-Assistenten: Tickets, Störungen, Aufgaben und Wartungsaufträge Schritt für Schritt anlegen (Art → Was? → Wo? → Wer? → Wann? → Anlegen), mit Gruppen-Zuweisung, Schnellwahl der Fälligkeit, ähnlichen gelösten Störungen schon beim Tippen und „Anlegen & nächster“; öffnet über Neue Zuweisung, Neu/Neue Aufgabe und in der Wartung über „Neuer Auftrag“
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
