# Explorer für MQTT, OPC UA und Modbus TCP – Entwurf (freigegeben)

Stand 2026-11-14 · vom Anwender freigegeben („Der Entwurf passt also Ja“)

## Absicht
Import- und Export-Verbindungen sollen sich bequem erkunden lassen, um Datenpunkte zu finden, zu verstehen und mit einem Klick als Zuordnung zu übernehmen – Vorbereitung für das Energiedaten-Paket (WAGO PFC200) und für weitere Anlagenanbindungen. Heute gibt es nur einen MQTT-Sniffer (Liste, 15 min), eine OPC-UA-Baumvorschau (begrenzt) und einen Modbus-Einzel-Lesedialog.

## Gemeinsam
- Reiter **„Explorer“** an der jeweiligen Import- bzw. Export-Verbindung (MQTT, OPC UA, Modbus TCP).
- Aus jedem gefundenen Wert mit einem Klick eine **Zuordnung** bauen (vorhandener Zuordnungsweg; Quellwert-Formate bleiben: MQTT-Topic/JSON-Pfad, OPC-UA-NodeID, Modbus `registertyp:adresse:datentyp` – ggf. um Byte-/Wortreihenfolge erweitert, rückwärtsverträglich).
- Lesen: wer die Verbindung sehen darf. **Schreiben/Senden** zum Testen: nur mit Bearbeiten-Recht der Verbindung, mit Rückfrage und Eintrag im Verlauf/Protokoll. Auf dem Spiegelserver kein Schreiben/Senden.
- Sitzungen höchstens 15 Minuten, dann Trennung (wie heute beim Sniffer); Grenzen für Datenmenge, Knoten, Register.
- Farben nur über CSS-Variablen, Fiori/Fluent, hell/dunkel; Handbuch, CHANGELOG, VERSION wie üblich.

## MQTT-Explorer (Ausbau Sniffer)
- Themenbaum mit letztem Wert je Thema, Nachrichtenzahl, Zeitpunkt, Retained-Kennzeichen.
- JSON aufbereitet (aufklappbar); aus einem JSON-Feld direkt eine Zuordnung.
- Kurzer Verlauf je Thema, bei Zahlen als kleine Kurve.
- Filter nach Thema (Wildcards).
- Testsenden: Thema, Inhalt, QoS, Retain.

## OPC-UA-Explorer (Ausbau Vorschau)
- Knotenbaum schrittweise aufklappen (statt alles auf einmal).
- Je Knoten: Wert, Datentyp, Zeitstempel, Qualität (StatusCode), Zugriffsrechte.
- Werte live verfolgen (Subscription), mehrere Knoten gleichzeitig.
- Testweise schreiben.

## Modbus-TCP-Explorer
- Registerbereiche lesen (Holding, Input, Coils, Discrete), z. B. 0–99, Einheit-ID wählbar.
- Jedes Register gleichzeitig als INT16, UINT16, INT32, UINT32, FLOAT32 (und 64-Bit, wo sinnvoll) in allen Byte- und Wortreihenfolgen (bei WAGO oft Wort getauscht).
- Passende Deutung anklicken → Zuordnung; Werte live verfolgen.
- Testweise ein Register/Coil schreiben.

## Nicht-Ziele
Integrierter MQTT-Broker, OPC-UA-Server, Modbus-Server; Energiedaten-Import selbst (eigenes Paket); IT-Assets (eigenes Paket).

## Dem Umsetzer überlassen
Live-Übertragung (SSE wie beim Sniffer oder htmx-Polling), genaue Grenzen, Aufteilung in Aufgaben, Speicherung von Explorer-Zustand nur im Arbeitsspeicher.
