# Änderungsprotokoll

Alle nennenswerten Änderungen am PDH-Server. Die Versionsnummer steht in der
Datei `VERSION` und folgt [Semantic Versioning](https://semver.org/lang/de/)
(Haupt.Neben.Fehlerkorrektur; vor 1.0 steigt die Nebenversion mit jedem
Funktionspaket). Versionen bis 0.9.0 stammen aus den Commit-Nachrichten, 0.10.0
bis 0.14.0 wurden nachträglich aus der Git-Historie zusammengefasst.

Aufbau je Version: `## [x.y.z] – JJJJ-MM-TT`, darunter `### Neu`, `### Geändert`,
`### Behoben`, `### Sicherheit` mit Stichpunkten. Das Handbuch zeigt diese Datei
im Kapitel „Versionen & Updates“ an.

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
- Die Karte „Zugangsweg“ meldet „Token fehlt“, wenn auf diesem oder dem anderen Server kein Tunnel-Token liegt, statt den Tunnel als bereit zu zeigen.
- `pdh-ha-adresse.sh uebergabe` schaltet die Adresse zuerst mitwandernd ein und nimmt sie erst dann aus der festen Netzwerkkonfiguration – die Lücke dauert nur wenige Sekunden, egal wie lange die Bestätigung dauert.
- Ein Adresstest mit der eingeschalteten mitwandernden Adresse wird abgelehnt; dafür eine freie Testadresse nehmen.
- Meldungen zur mitwandernden Adresse kommen höchstens einmal je Stunde, auch wenn ein Netzwerkdienst die Adresse laufend entfernt.

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
