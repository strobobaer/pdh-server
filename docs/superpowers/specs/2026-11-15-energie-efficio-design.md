# Energiedaten-Export für Berg Efficio – Entwurf (freigegeben)

Stand 2026-11-15 · vom Anwender freigegeben (Reihenfolge und Angaben im Chat, „Passt“)

## Absicht
Energiedaten der Anlagen (zuerst von WAGO PFC200 mit EDM-Firmware, grundsätzlich aus jedem Import) sollen minütlich ins PDH kommen, den Anlagen zugeordnet sein und alle 15 Minuten als CSV an das externe Energiemanagement **Berg Efficio** gehen.

## Ziele
- **Lesen:** über die vorhandenen Import-Verbindungen (MQTT, OPC UA, Modbus TCP – mit dem Explorer zuzuordnen; später ggf. SQLite über den Daten-Client) im Minutentakt. Der Lese-Weg vom PFC200 ist noch offen; der Export funktioniert unabhängig davon mit jedem Import, der Messwerte einer Anlage liefert.
- **Messstellen:** Eine Messstelle ist ein Messwert einer Anlage (z. B. „Zählerstand Linie 1“) mit **Messstellen-ID** (frei, so wie Efficio sie kennt), Einheit und Quelle (Zuordnung eines Imports). Messstellen werden im PDH gepflegt (Liste, anlegen, Anlage, Quelle).
- **Zuweisungstabelle** (Anwender: „Welche Daten importiert werden, soll in einer Zuweisungstabelle pro Messwert/Messstelle einzeln zugeordnet werden, es kann alles sein“): Jede Messstelle bekommt in einer Tabelle ihre Quelle einzeln zugewiesen. Die Quelle kann **jeder beliebige importierte Wert** sein: jede Import-Verbindung und jeder Datenpunkt bzw. jede Zuordnung, ob MQTT, OPC UA, Modbus TCP, Datenbank, Datei oder künftige Wege. Es gibt keine feste Liste von Energiegrößen; Strom, Gas, Wasser, Druckluft, Temperatur oder Stückzahl sind alle gleich möglich. Die Tabelle zeigt je Zeile Messstellen-ID, Bezeichnung, Anlage, Quelle (Verbindung und Datenpunkt, auswählbar), Einheit, Art (Zählerstand/Verbrauch/Momentanwert), optional Faktor, letzten Wert mit Zeitpunkt und aktiv/inaktiv. Zeilen lassen sich direkt in der Tabelle bearbeiten. Eine Quelle darf mehreren Messstellen zugewiesen werden.
  - **Anlage wählen wie überall** (Anwender): Beim Bearbeiten erscheint für die Anlage der vorhandene Infra-Picker (`web/templates/widgets/infra_tree_picker.gohtml`, wie in `import_mapping.gohtml`). Gibt es zur gewählten Anlage noch keine passende Messstelle, wird sie beim Speichern angelegt. Eine neue Zeile in der Tabelle ist damit auch eine neue Messstelle.
- **Virtuelle (berechnete) Messstellen** (Anwender): Eine Messstelle kann statt einer Import-Quelle eine **Formel aus anderen Messstellen** haben. Beispiel: Gesamtanlage − Unteranlage A − Unteranlage B = Unteranlage C. Die Formel kennt `+ − × ÷`, Klammern und Zahlen, Bezüge auf andere Messstellen werden über ihre ID gemacht.
  - Berechnet wird je Intervall aus den Intervallwerten der Bezugsmessstellen. Bei Zählerständen wird der Verbrauch im Intervall verrechnet. Für die virtuelle Messstelle wird ein eigener Zählerstand fortgeschrieben, wenn sie als Zählerstand exportiert wird.
  - Fehlt ein Bezugswert, fällt die virtuelle Zeile für dieses Intervall weg. Sie wird nicht mit 0 gerechnet, und das Protokoll vermerkt es.
  - Zirkelbezüge und unbekannte IDs werden beim Speichern abgewiesen. Eine virtuelle Messstelle darf auf andere virtuelle Messstellen verweisen.
  - Virtuelle Messstellen werden wie normale exportiert und sind in der Tabelle als „berechnet“ gekennzeichnet. Ihre Formel steht in der Spalte „Quelle“.
  - **Berechnung schon beim Import** (Anwender): Virtuelle Messstellen werden nicht erst beim Export berechnet, sondern bei jedem Import-Takt, also minütlich. Ihre Werte werden wie echte Messwerte abgelegt. So stehen sie für Export, Anzeige und Schwellwerte gleich zur Verfügung.
- **Schwellwerte und Alarme** (Anwender: Stromspitzenerkennung, Werkzeugwechsel):
  - Je Messstelle, echt oder virtuell, lassen sich ein oder mehrere Schwellwerte festlegen. Dazu gehören Bedingung (über/unter), Grenzwert mit Einheit und Bezeichnung.
  - **Überwachungszeitraum** je Schwellwert einstellbar, zum Beispiel 1 Minute (Einzelwert) oder 15 Minuten (15-Minuten-Wert). Ausgewertet wird der Wert über diesen Zeitraum: bei Momentanwerten der Mittelwert, bei Zählerständen und Verbrauch die Menge bzw. Leistung im Zeitraum.
  - Bei Überschreitung legt das PDH ein **Ticket** an (vorhandenes Ticket-Modul) und schickt eine **Nachricht an den Verantwortlichen**. Verantwortlich ist je Schwellwert ein Benutzer oder eine Gruppe, als Vorgabe die Verantwortlichen der Anlage. Die Nachricht läuft über den vorhandenen Benachrichtigungsweg (Mail/Chat/Push).
  - Das Ticket bekommt einen **Tag**, der in der Einstellung des Schwellwerts gewählt wird (vorhandene `tickets.tags`, z. B. „Stromspitze“, „Werkzeugwechsel“).
  - **Kommentarpflicht:** Ein solches Ticket lässt sich nur mit Kommentar erledigen oder schließen.
  - Je Überschreitung entsteht ein Ticket. Solange der Wert über der Grenze bleibt, kommen keine weiteren Tickets dazu. Ein neues Ticket gibt es erst, wenn der Wert wieder im Normalbereich war.
  - Nur der aktive Server überwacht, der Spiegel legt keine Tickets an (wie die Rüst-Überwachung `internal/modules/ruesten/monitor.go`).
  - Rechte: Schwellwerte pflegen mit eigenem Recht, Tickets mit den vorhandenen Ticket-Rechten.
- **Export „Efficio-CSV“** (neuer Export-Typ bzw. Exportprofil):
  - 3 Spalten `Zeitstempel;Messstellen-ID;Messwert`, Trennzeichen Semikolon. **Mehrere Messstellen in einer Datei, eine Zeile je Messstelle und Zeitpunkt** (Anwender).
  - **Dateilänge in Tagen** (Anwender: „max Länge der Datei in gespeicherten Tagen“): Eine Datei sammelt die Läufe fortlaufend (jeder Lauf hängt seine Zeilen an), bis sie N Tage Daten umfasst; dann beginnt eine neue Datei. N einstellbar (Vorgabe 1 Tag); N = 0 heißt „eine Datei je Lauf“. Beim Ablageziel wird die laufende Datei jeweils vollständig ersetzt (Netzlaufwerk/FTP: atomar über temporären Namen und Umbenennen), damit Efficio nie eine halbe Datei liest.
  - Aufbewahrung der Dateien zum Abholen im PDH in Tagen einstellbar (Vorgabe 30).
  - Takt einstellbar, Vorgabe **15 Minuten**; je Messstelle der Wert zum Intervallende (Vorgabe Zählerstand), wahlweise Verbrauch im Intervall.
  - Einstellbar mit Vorgaben: Zeitformat `TT.MM.JJJJ hh:mm`, Dezimalkomma, Kopfzeile ja/nein, Dateiname mit Zeitstempel, Zeichensatz UTF-8 (wahlweise Windows-1252).
  - **Ablage je Export wählbar:** zum **Abholen** im PDH (Download-Liste/Link, Aufbewahrung einstellbar), auf ein **Netzlaufwerk** (vorhandene Laufwerke des PDH), per **FTP oder SFTP** (neu: Zugangsdaten verschlüsselt, Zielordner, Prüfung „Verbindung testen“).
  - Fehlende Werte (Gerät offline) werden nicht erfunden: Zeile fehlt, Hinweis im Exportprotokoll; nach Wiederkehr kein Nachfüllen alter Intervalle (Vorgabe), optional nachträglich.
  - Ein Fehlschlag beim Ablegen wird wiederholt (nächster Lauf legt verpasste Dateien nach); Protokoll je Lauf (Zeit, Zeilen, Ziel, Ergebnis).
- **Rechte:** vorhandene Export-Rechte (`export.*`); Messstellen pflegen mit dem Recht, das Export-Zuordnungen heute brauchen; FTP-Zugangsdaten nur mit Bearbeiten-Recht; Löschen nur Bereichsadministrator.
- **Verbund:** Export läuft nur auf dem aktiven Server; Spiegel legt nichts ab.
- Handbuch, CHANGELOG, VERSION, Fiori/Fluent wie üblich.

## Nicht-Ziele
Lese-Weg vom PFC200 per SQLite/Daten-Client (eigenes Folgepaket, falls MQTT/OPC UA/Modbus nicht reichen); Auswertungen/Diagramme der Energiedaten; Efficio-API (nur CSV).

## Dem Umsetzer überlassen
Ob „Efficio-CSV“ als eigener Export-Typ oder als Profil des vorhandenen CSV-Exports gebaut wird (vorhandene Export-Struktur prüfen und nutzen), Speicherung der Messwert-Historie (vorhandene Ablage der Import-Werte nutzen, sonst schlanke Tabelle), Bibliothek für FTP/SFTP (gepflegt und aktuell).
