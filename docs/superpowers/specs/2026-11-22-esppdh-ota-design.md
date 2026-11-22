# ESPPDH: Firmware übers Netz aufspielen – Entwurf

Stand 22.11.2026, zur Bestätigung durch den Anwender. Version: nächste freie
Korrekturversion (bei Beginn 0.82.23).

## Anlass und Ziel

Bisher kommt neue Firmware nur per USB auf ein ESPPDH-Gerät. Wunsch des
Anwenders: Updates übers Netz, **das PDH schiebt sie** (21./22.11.2026).
Entschieden: **a + b** – je Gerät ein Knopf „Firmware aufspielen“ und
zusätzlich „Alle veralteten Geräte aktualisieren“ (nacheinander bauen und
aufspielen). Kein selbsttätiges Aufspielen.

## Ziele

- Gerät mit Update-Baustein: Das PDH spielt die zuletzt gebaute Firmware per
  Knopf übers Netz auf.
- Sammelaktion: alle Geräte mit veralteter Firmware nacheinander bauen und
  aufspielen; scheitert eines, geht es mit dem nächsten weiter; Ergebnis je
  Gerät sichtbar.
- Das PDH weiß, welche Firmware auf dem Gerät läuft (meldet das Gerät selbst).

## Nicht-Ziele

- Kein selbsttätiges Aufspielen (z. B. nach einem ESPHome-Update).
- Kein Aufspielen über die Webseite des Geräts (`web_server` bleibt `ota: false`).
- Kein Ersatz für das erste Flashen per USB.

## Anti-Ziele

- Ein Gerät darf durch ein Update nicht unbrauchbar werden: Es bleibt der
  ESPHome-Sicherheitsmodus (`safe_mode`) aktiv, und die Firmware wird nur
  aufgespielt, wenn sie für genau dieses Gerät gebaut wurde.
- Kein Aufspielen vom Spiegel und nicht während der Wartung.

## Ansätze

- **A – ESPHome-Update-Baustein (`ota: platform: esphome`) mit Kennwort je
  Gerät, Aufspielen mit der ESPHome-Umgebung des Servers** (gewählt):
  `esphome upload esppdh.yaml --device <Adresse> --file firmware.ota.bin` in
  einem Arbeitsordner wie beim Bau; Protokoll und Firmware passen immer zur
  selben ESPHome-Version.
- B – Hochladen über die Webseite des Geräts: verworfen (Webseite
  abschaltbar; jeder mit dem Web-Kennwort könnte Firmware hochladen).
- C – eigenes OTA-Protokoll in Go: verworfen (muss jeder ESPHome-Version
  folgen).

## Firmware

- Neuer Baustein in der erzeugten YAML: `ota: - platform: esphome,
  password: !secret ota_password` (Port: ESPHome-Standard, ESP32 3232,
  ESP8266 8266) und `safe_mode:`.
- Kennwort je Gerät, zufällig, verschlüsselt gespeichert
  (`esppdh_devices.ota_password_enc`, wie das Web-Kennwort; für vorhandene
  Geräte beim nächsten Bau angelegt); im Bau-Protokoll geschwärzt.
- Neuer Info-Wert: Das Gerät meldet die Kennung seiner Firmware (Kennung des
  Baus) auf `esppdh/<slug>/info/firmware` (text_sensor mit festem Wert,
  `retain: true`). So sieht das PDH, was installiert ist – egal ob per USB
  oder übers Netz aufgespielt.

## Daten

- Migration: `esppdh_devices.ota_password_enc text NOT NULL DEFAULT ''`;
  Tabelle `esppdh_ota_runs` (id, device_id, build_id, status queued|running|
  ok|failed|canceled, log, requested_by, batch_id NULL, created_at,
  started_at, finished_at).
- „Veraltet“ heißt: Die vom Gerät gemeldete Firmware-Kennung ist nicht die
  des letzten erfolgreichen Baus, oder der letzte Bau passt nicht mehr zur
  Gerätekonfiguration bzw. zur aktuellen ESPHome-Version (wie der bestehende
  Hinweis „neu bauen“). Geräte ohne Update-Baustein (Kennung leer) gelten
  als „erst per USB aktualisieren“.

## Ablauf „Firmware aufspielen“ (ein Gerät)

1. Voraussetzungen: aktiver Server, keine Wartung, letzter Bau erfolgreich,
   Gerät meldet eine Adresse, Firmware enthält den Update-Baustein (Kennung
   gemeldet).
2. Auftrag in `esppdh_ota_runs`, im Hintergrund: Arbeitsordner mit YAML,
   Secrets (0600) und der entschlüsselten `firmware.ota.bin`;
   `esphome upload … --device <Adresse> --file …` mit Zeitlimit (5 min) und
   eigener Prozessgruppe (wie der Bau); Ordner danach immer löschen.
3. Erfolg erst, wenn das Gerät nach dem Neustart die neue Firmware-Kennung
   meldet (bis 3 min warten), sonst „aufgespielt, meldet sich nicht“.
4. Geräteseite zeigt Fortschritt und Protokoll (Kennwörter geschwärzt).

## Sammelaktion „Alle veralteten Geräte aktualisieren“

- Auf der ESPPDH-Übersicht. Bestätigung mit Liste der Geräte.
- Nacheinander: neu bauen (falls nötig), dann aufspielen; ein Gerät nach
  dem anderen; Fehler überspringen. Übersicht zeigt je Gerät den Stand.
- Geräte ohne Update-Baustein oder ohne Adresse werden übersprungen
  (Hinweis „erst per USB“ bzw. „keine Adresse“).
- Abbrechen möglich (laufendes Gerät wird fertig, Rest abgebrochen).

## Rechte

- Neues Recht `esppdh.ota` „ESPPDH: Firmware übers Netz aufspielen“,
  Kategorie „Daten & Schnittstellen“, per Migration an alle, die
  `esppdh.edit` haben (samt Abteilung). Einzelgerät: `hasPermRec` auf die
  Anlage des Geräts; Sammelaktion nur, wer das Recht überall hat.

## Netz

- Geräteseite: Hinweis, dass der Server das Gerät auf dem Update-Port
  erreichen muss (Freigabe vom Server zum Netz des Geräts, Port 3232/8266),
  ähnlich dem bestehenden Hinweis zur Broker-Freigabe.

## Handbuch und Protokoll

- Kapitel ESPPDH: „So geht's: Firmware übers Netz aufspielen“, einmal per
  USB nötig, Netzfreigabe, Sammelaktion; CHANGELOG `### Neu`,
  `### Sicherheit` (neues Recht).

## Festlegungen nach der Prüfung (22.11.2026)

- Aufruf mit festem Weg: `esphome upload esppdh.yaml --device <Adresse>
  --ota-platform esphome --file firmware.ota.bin` (in ESPHome 2026.10.2
  geprüft: kein Bauordner nötig, drei Versuche mit 5 s Pause). ESPHome prüft
  nicht, ob die Firmware zum Gerät passt – das sichert das PDH über die
  Zuordnung Bau ↔ Gerät und das eigene Kennwort je Gerät.
- Die Bau-Kennung im Info-Wert darf nicht in `ConfigHash` eingehen (sonst
  gälte jede Firmware sofort als veraltet und Flashen wäre gesperrt): eigenes
  Feld im Bauauftrag, Wert = `esppdh_builds.id`. Thema über `infoTopic`
  (`esppdh/<slug>/info/firmware`).
- Durch die neuen Bausteine gelten nach dem Update alle Geräte einmal als
  „neu bauen“, ältere Firmware lässt sich auch per USB nicht mehr flashen –
  gewollt, weil ohnehin einmal USB nötig ist; steht im Handbuch und im
  CHANGELOG.
- Notzugang: Bei WLAN-Geräten bringt ESPHome über `captive_portal` weiterhin
  `ota: platform: web_server` mit (nur im Notzugang erreichbar, wie bisher).
  „Kein Aufspielen über die Webseite“ gilt für den Betrieb.
- Migration 194 (nächste freie Nummer bei Beginn).

## Entscheidungen des Anwenders (22.11.2026, „1a 2a“)

- **Veraltet = nur echte Änderungen:** Die Sammelaktion nimmt Geräte, deren
  Konfiguration sich seit dem letzten erfolgreichen Bau geändert hat
  (`ConfigHash` ≠ `built_hash`) oder die nicht die Kennung des letzten
  erfolgreichen Baus melden. Eine neue ESPHome-Version allein zählt nicht
  (bleibt Hinweis auf der Geräteseite).
- **Nur in freigegebene Netze:** Aufgespielt wird nur, wenn die gemeldete
  Adresse in einem Netz der Seite „Netzwerk“ liegt, das für ESPPDH-Geräte
  freigegeben ist – das Netz des WLANs mit Haken „Für ESPPDH-Geräte nutzbar“
  bzw. bei Kabel das eingetragene Netz des Geräts (`segment_id`). Sonst nennt
  der Knopf den Grund („Adresse … liegt in keinem freigegebenen Netz“).

## Dem Bauenden überlassen

- Wie die Sammelaktion ihren Stand speichert (batch_id oder eigener Zustand).
- Genaue Prüfung, ob `esphome upload --file` in der installierten
  ESPHome-Version so heißt (sonst gleichwertiger Aufruf).
