# Energiedaten-Export für Berg Efficio – Entwurf (freigegeben)

Stand 2026-11-15 · vom Anwender freigegeben (Reihenfolge und Angaben im Chat, „Passt“)

## Absicht
Energiedaten der Anlagen (zuerst von WAGO PFC200 mit EDM-Firmware, grundsätzlich aus jedem Import) sollen minütlich ins PDH kommen, den Anlagen zugeordnet sein und alle 15 Minuten als CSV an das externe Energiemanagement **Berg Efficio** gehen.

## Ziele
- **Lesen:** über die vorhandenen Import-Verbindungen (MQTT, OPC UA, Modbus TCP – mit dem Explorer zuzuordnen; später ggf. SQLite über den Daten-Client) im Minutentakt. Der Lese-Weg vom PFC200 ist noch offen; der Export funktioniert unabhängig davon mit jedem Import, der Messwerte einer Anlage liefert.
- **Messstellen:** Eine Messstelle ist ein Messwert einer Anlage (z. B. „Zählerstand Linie 1“) mit **Messstellen-ID** (frei, so wie Efficio sie kennt), Einheit und Quelle (Zuordnung eines Imports). Messstellen werden im PDH gepflegt (Liste, anlegen, Anlage, Quelle).
- **Export „Efficio-CSV“** (neuer Export-Typ bzw. Exportprofil):
  - 3 Spalten `Zeitstempel;Messstellen-ID;Messwert`, Trennzeichen Semikolon, eine Datei je Lauf.
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
