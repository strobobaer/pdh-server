# ESPPDH: ESPHome hält sich selbst aktuell (0.82.1) – Entwurf

Stand 19.11.2026. Auftrag des Anwenders: „ESPPDH soll auch immer aktuell sein,
quasi Aktualisierung mit ESPHome“ – gewählt: **selbst, mit Probe**, als eigene
Version **0.82.1** nach 0.82.0.

## Ausgangslage (0.82.0)

- Die ESPHome-Version steht fest in `scripts/esphome.version` (2026.10.2) und
  ist ins Programm eingebettet (`esppdh.PinnedVersion`).
- `scripts/install-esphome.sh` (läuft als root über den Update-Agenten) richtet
  genau diese Version in `/opt/pdh-esphome` ein; `install_update.sh` zieht sie
  bei jedem PDH-Update nach.
- Weicht die installierte Version ab, zeigt der Reiter „ESPPDH“ „veraltet“.
- Eine neue ESPHome-Version kommt also nur mit einem PDH-Update.

## Ziel

Jeder Server prüft täglich selbst, ob es eine neuere ESPHome-Version gibt,
probiert sie aus und schaltet nur um, wenn die Probe besteht. Geht etwas
schief, baut ESPPDH mit der bisherigen Version weiter und zeigt einen Hinweis.
Firmware der Geräte wird nie von selbst neu gebaut oder verteilt.

## Ablauf

1. **Prüfen (täglich, je Server).** Das PDH fragt bei PyPI die
   Versionsliste von `esphome` ab. Kandidat ist die höchste **stabile**
   Version (keine Vor- und Entwicklungsversionen), die
   - neuer ist als die installierte,
   - seit mindestens **3 Tagen** veröffentlicht ist (Karenz – fehlerhafte
     Ausgaben werden meist binnen Tagen ersetzt),
   - nicht zurückgezogen ist und zur Python-Version des Servers passt,
   - nicht schon einmal an der Probe gescheitert ist (siehe 4).
   Kein Internet oder PyPI nicht erreichbar: nichts tun, „Prüfung nicht
   möglich“ mit Zeitpunkt vermerken. Das PDH läuft unverändert.
2. **Daneben einrichten.** Der Update-Agent richtet den Kandidaten in einer
   **eigenen** Python-Umgebung neben der laufenden ein. Die laufende Umgebung
   wird dabei nicht angefasst; Bauläufe gehen weiter.
3. **Probe.** Das PDH (ohne Root-Rechte) prüft mit dem Kandidaten:
   - `esphome config` für die erzeugte Konfiguration **jedes nicht gesperrten
     Geräts** und für die mitgelieferten Musterkonfigurationen aller Platinen
     und Bausteine (damit auch ein Server ohne Geräte eine Probe hat);
   - **ein echter Probebau je verwendeter Chip-Familie** (ESP32, ESP32-S3,
     ESP32-C3, ESP8266) – die Konfigurationsprüfung allein erkennt keine
     Übersetzungsfehler. Der Probebau läuft in der normalen Bau-Schlange mit
     niedriger Priorität hinter echten Aufträgen; sein Ergebnis wird verworfen.
   Die Probe arbeitet wie der normale Bau in einem eigenen Ordner, der danach
   gelöscht wird (Kennwörter).
4. **Umschalten oder bleiben.**
   - Besteht alles: Der Agent schaltet auf den Kandidaten um. Die bisherige
     Umgebung bleibt **eine Generation** liegen (Knopf „Zurück zur vorigen
     Version“), ältere werden gelöscht.
   - Scheitert etwas: Der Kandidat wird entfernt, die Version als
     „gescheitert“ vermerkt (mit dem Ende der Ausgabe) und **nicht täglich neu
     versucht** – erst wieder nach einem PDH-Update (der Erzeuger kann dann
     angepasst sein) oder über den Knopf. Eine noch neuere ESPHome-Version wird
     normal geprüft.
5. **Hinweise.**
   - Reiter „ESPPDH“ (Server-Einstellungen): installierte Version, zuletzt
     geprüft, Ergebnis, ggf. gescheiterte Version mit Ausgabe.
   - Bei gescheiterter Probe einmal je Version eine Meldung an die
     ESPPDH-Bereichsadministratoren (`esppdh.admin`).
   - Gerät: „Firmware mit ESPHome x gebaut, aktuell ist y“ als sachlicher
     Hinweis (kein Fehler); neu bauen und flashen entscheidet der Anwender.

## Bedienung

Reiter „ESPPDH“ bekommt:

- Schalter **„ESPHome selbst aktuell halten“** – Standard **ein**. Aus: Die
  Version ändert sich nur noch über den Knopf oder ein PDH-Update.
- Knopf **„Jetzt prüfen“** (führt 1–4 sofort aus, ohne die Karenz zu umgehen;
  eine gescheiterte Version wird dabei erneut versucht).
- Knopf **„Zurück zur vorigen Version“**, solange eine vorige Umgebung da ist.
  Danach gilt die verlassene Version als gescheitert (kein Hin und Her).

Kein neues Recht: Schalter und Knöpfe prüfen dasselbe Recht wie „Umgebung
einrichten“ in 0.82.0. Handbuch-Kapitel „ESPPDH“ und Änderungsprotokoll
werden nachgezogen.

## Rolle der Version im Repository

`scripts/esphome.version` bleibt, bedeutet aber künftig **Ausgangs- und
Mindestversion**:

- Die erste Einrichtung nimmt weiterhin diese Version (mit ihr ist der
  Erzeuger geprüft); Neueres holt die tägliche Prüfung nach.
- `install_update.sh` hebt nur noch an, wenn die installierte Version
  **älter** ist; es setzt eine selbst geholte neuere Version nicht zurück.
- „Veraltet“ heißt im Reiter nur noch „älter als die Mindestversion“.
- Bei jeder PDH-Version wird die Datei auf den dann aktuellen Stand gehoben,
  damit Erzeuger, Erwartungsdateien und Mindestversion zusammenpassen.

## Zwei Server (Spiegelverbund)

Jeder Server hat seine eigene Umgebung und prüft selbst (wie schon das
Einrichten in 0.82.0). Die Probe braucht nur Lesezugriff auf die Geräte – das
geht auch am Spiegel. Die Server können für kurze Zeit verschiedene Versionen
haben; gebaut wird nur am aktiven Server, und jeder Bau vermerkt seine
ESPHome-Version (gibt es schon). Der Probebau läuft nur am aktiven Server; der
Spiegel begnügt sich mit der Konfigurationsprüfung.

## Sicherheit

- Der Agent nimmt nur Versionsangaben der Form `JJJJ.M.P` an und holt nur das
  Paket `esphome` von PyPI – keine freie Paketangabe, keine andere Quelle.
- Der Agent stuft nie herab, außer auf die eine aufbewahrte vorige Umgebung.
- Die Schutzregeln des Einrichtungsskripts (nur eigene Ordner löschen, keine
  Verweise) gelten auch für die Umgebungen der Kandidaten.
- Karenz von 3 Tagen als Schutz vor zurückgezogenen oder manipulierten
  Ausgaben. Mehr (Prüfsummen festschreiben) ist mit „selbst aktuell“ nicht
  vereinbar – das ist der Preis der Entscheidung.
- Plattenplatz: höchstens drei Umgebungen gleichzeitig (laufend, vorige,
  Kandidat); unter 3 GB frei wird kein Kandidat eingerichtet (Hinweis).

## Nicht enthalten

- Kein selbsttätiges Neu-Bauen oder Verteilen von Firmware (OTA ist Paket 2/3).
- Keine Aktualisierung von ESP Web Tools (bleibt mitgeliefert im PDH).
- Docker-Betrieb: ESPPDH gibt es dort weiterhin nicht.

## Festlegungen – vom Anwender am 19.11.2026 bestätigt („1-7 passt so“)

1. Karenz **3 Tage** nach Veröffentlichung.
2. Probe = Konfigurationsprüfung aller Geräte **plus ein echter Probebau je
   Chip-Familie** (am aktiven Server; kostet je neuer Version einige Minuten
   Rechenzeit).
3. Eine gescheiterte Version wird **nicht täglich** neu versucht.
4. Schalter Standard **ein**; Knopf „Zurück zur vorigen Version“.
5. `esphome.version` bleibt als **Mindestversion**; PDH-Updates stufen nicht
   mehr herab.
6. Meldung bei gescheiterter Probe an `esppdh.admin`.
7. Kein neues Recht, voraussichtlich keine Migration (Zustand in den
   Server-Einstellungen).
