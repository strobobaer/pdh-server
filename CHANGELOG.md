# Änderungsprotokoll

Alle nennenswerten Änderungen am PDH-Server. Die Versionsnummer steht in der
Datei `VERSION` und folgt [Semantic Versioning](https://semver.org/lang/de/)
(Haupt.Neben.Fehlerkorrektur; vor 1.0 steigt die Nebenversion mit jedem
Funktionspaket). Versionen bis 0.9.0 stammen aus den Commit-Nachrichten, 0.10.0
bis 0.14.0 wurden nachträglich aus der Git-Historie zusammengefasst.

Aufbau je Version: `## [x.y.z] – JJJJ-MM-TT`, darunter `### Neu`, `### Geändert`,
`### Behoben`, `### Sicherheit` mit Stichpunkten. Das Handbuch zeigt diese Datei
im Kapitel „Versionen & Updates“ an.

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
