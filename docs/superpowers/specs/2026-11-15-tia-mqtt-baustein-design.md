# TIA-Portal-Baustein „MQTT senden“ – Entwurf

Stand 2026-11-15 · Angaben des Anwenders im Chat (Steuerung S7-1500 und S7-1200, beide Wege, Download im PDH). Vom Anwender freigegeben („Passt soweit“).

## Absicht
Siemens-Steuerungen sollen ihre Daten ohne Zusatzgerät per MQTT an das PDH schicken. Dafür liefert das PDH einen fertigen Funktionsbaustein für das TIA Portal. Er sendet bei einem Trigger-Eingang und schickt regelmäßig einen Heartbeat, damit das PDH erkennt, ob die Steuerung lebt.

## Ziele
- **Steuerungen:** S7-1500 und S7-1200 (ab Firmware 4.x), SCL, optimierter Zugriff, als externe Quelle (`.scl`) zum Import in TIA Portal (ab V17).
- **Zwei Varianten (Anwender: „Beides“):**
  1. **Eigenständig:** MQTT 3.1.1 selbst in SCL über `TCON`/`TSEND`/`TRCV` (`TCON_IP_v4`): CONNECT (mit Benutzer/Kennwort), PUBLISH QoS 0 (wahlweise Retain), PINGREQ für Keep-Alive, DISCONNECT, Wiederverbinden nach Fehler. Ohne TLS (Port 1883), keine fremde Bibliothek.
  2. **Mit Siemens-Bibliothek LMQTT:** dieselbe Schnittstelle als Hülle um `LMQTT_Client`; erlaubt TLS (8883). Die Bibliothek muss der Anwender vorher ins Projekt holen (Hinweis und Link in Handbuch und Download).
- **Schnittstelle des Bausteins (beide Varianten gleich):**
  - Eingänge: `enable`, `trigger` (steigende Flanke = senden), `heartbeatInterval` (Vorgabe `T#60s`, 0 = aus), `hwId`/`connId`.
  - **Werte aus einem selbst erstellten Datenbaustein** (Anwender): Der Anwender legt in TIA einen eigenen DB an, mit beliebig vielen Variablen und Strukturen. Er exportiert dessen Quelle (`.db`, „Quelle aus Baustein generieren“) und lädt sie im PDH hoch. Das PDH liest die Variablen mit Namen und Typ (Bool, Int, DInt, UInt, UDInt, Real, LReal, String, Date_And_Time/DTL, Strukturen und Felder, ausgegeben mit Punkt- bzw. Index-Pfad) und erzeugt dazu den passenden Sende-Code. Dieser schreibt das JSON mit genau diesen Namen. Der Baustein bekommt den DB als `InOut`-Parameter. Je Variable entsteht wahlweise gleich eine PDH-Zuordnung. Nicht unterstützte Typen werden beim Hochladen genannt und übersprungen. Die Länge einer Nachricht ist begrenzt (Vorgabe 4096 Byte). **Wird sie zu groß, entsteht eine zweite (dritte …) Nachricht** (Anwender). Das PDH teilt beim Erzeugen die Variablen fest auf Teile auf, die sicher in die Grenze passen. Strings werden dabei mit ihrer Höchstlänge gerechnet. Jeder Teil geht beim selben Trigger nacheinander raus, mit gleichem `ts`, und trägt `"part":1,"parts":2` usw. Die Zuordnungen im PDH lesen die Werte aus jedem Teil gleich, denn die JSON-Pfade bleiben `values.<Name>`.
  - Ausgänge: `connected`, `busy`, `done` (ein Zyklus), `error`, `status` (Klartext-Code), `sentCount`, `heartbeatCount`.
  - Konfiguration in einem Datenbaustein: Broker-Adresse und Port, Client-ID, Benutzer/Kennwort, Topic, Heartbeat-Topic, Keep-Alive.
- **Inhalt der Nachrichten:** JSON, passend zu den PDH-Zuordnungen, z. B. `{"ts":"2026-11-15T10:01:00Z","values":{"Zaehler":123.4,"Takt":12.5}}`. Heartbeat auf eigenem Topic (`…/heartbeat`) mit Zähler und Zeitstempel. Der Zeitstempel kommt aus der Steuerungsuhr (UTC).
- **Im PDH zum Herunterladen:** An einer MQTT-Import-Verbindung erzeugt der Knopf „TIA-Baustein herunterladen“ die `.scl`-Datei. Man wählt Variante, CPU-Familie und Topic und lädt die Quelle des eigenen Datenbausteins hoch. Vorbelegt sind Broker (integrierter PDH-Broker oder Broker der Verbindung), Client-ID und Topic.
  - Das Kennwort steht nur in der Datei, wenn man das ausdrücklich ankreuzt, sonst ein Platzhalter.
  - Wahlweise legt das PDH die passenden Import-Zuordnungen je Wert gleich an (JSON-Pfad `values.<Name>`), dazu eine Zuordnung für den Heartbeat. Bleibt der Heartbeat länger als 3 Intervalle aus, zeigt die Verbindung „Steuerung meldet sich nicht“.
- **Rechte:** Herunterladen ohne Kennwort mit dem Leserecht der Verbindung, mit Kennwort und Zuordnungen anlegen nur mit Bearbeiten-Recht.
- Handbuch-Kapitel mit „So geht's“: Datei herunterladen, in TIA als externe Quelle hinzufügen, Bausteine erzeugen, in OB1 aufrufen, Hardware-Kennung eintragen, laden, im PDH prüfen. Dazu CHANGELOG und VERSION.

## Nicht-Ziele
S7-300/400, Empfangen/Abonnieren in der Steuerung, QoS 1/2 in der eigenständigen Variante, TLS in der eigenständigen Variante.

## Dem Umsetzer überlassen
Generierung über Go-Vorlage mit sicherem Einsetzen der Zeichenketten (keine Anführungszeichen- oder Steuerzeichen-Einschleusung in SCL); Prüfung des erzeugten SCL über Golden-Dateien; Prüfung des MQTT-Paketaufbaus über einen gleichwertigen Go-Kodierer in Tests. Die Abnahme macht der Anwender mit **PLCSIM** (Anwender). Hinweis: Das einfache PLCSIM lässt je nach Version keine echte TCP-Verbindung aus der simulierten CPU ins Netz zu. Ob die eigene PLCSIM-Version das kann, prüft der Anwender zuerst mit einem einfachen TCON-Test. Kann sie es nicht, ist für die Netzprüfung PLCSIM Advanced oder eine echte CPU nötig. Übersetzen, Bausteinaufruf und JSON-Aufbau lassen sich in jedem Fall in PLCSIM prüfen.

## Version
Eigene Korrekturversion **0.80.13**. Schwellwerte, Alarme und Tickets rücken auf **0.80.14**.
