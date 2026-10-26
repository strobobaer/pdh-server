# Testlabor für pdh-ha

Nur Testumgebung – nie auf einem Produktivserver. Das Labor startet zwei
Server (`node1`, `node2`) mit PostgreSQL (Ubuntu 26.04, Dienst `pdh` als
Platzhalter `pdh-dummy`), einen Zeugen (`witness`) und ein Gateway (`gw`) als
Container und spielt Ausfälle durch.

## Voraussetzungen

- Linux mit Docker und Compose-Plugin (`docker compose version`)
- `python3` (Ubuntu hat es) oder ersatzweise `jq`
- freie Netze `10.77.1.0/24`, `10.77.2.0/24`, `10.77.3.0/24`
- Containernamen `node1`, `node2`, `witness`, `gw` dürfen nicht belegt sein

## Befehle

```bash
bash deploy/ha-lab/ha-lab.sh up          # bauen, einrichten, Wartung aus
bash deploy/ha-lab/ha-lab.sh status      # pdh-ha status aller drei
bash deploy/ha-lab/ha-lab.sh <Szenario>  # ein Szenario (jeweils frisch aufgebaut)
bash deploy/ha-lab/ha-lab.sh alle        # alle Szenarien nacheinander
bash deploy/ha-lab/ha-lab.sh down        # Labor samt Volumes entfernen
bash deploy/ha-lab/ha-lab.sh race        # go test -race in golang:1.24
```

`up` richtet node1 ein, lässt node2 und den Zeugen mit Beitrittscode
beitreten, setzt die Laborzeiten (Takt 1 s, Selbst-Abschaltung 5 s,
Übernahme 15 s mit / 20 s ohne Zeuge, keine Wartezeit, 3 Umschaltungen je
Tag, asynchron nach 3 s, Gateway-Ziel `gw`) und schaltet die Wartung aus.
Vorher legt es auf node1 die Datenbank `lab` mit Tabelle `t(n)` an und macht
node2 mit `pdh-ha replica-init` zum Datenbank-Spiegel. Szenarien mit
automatischer Umschaltung „echt“ tauschen danach einmal hin und zurück
(Pflicht-Testumschaltung) und setzen `auto_mode` auf `real`.

Netze: `lan` (Server + Gateway), `vpn` (Server), `wit` (Server + Zeuge).
Die Server erreichen sich nur über `lan` und `vpn`, den Zeugen über `wit`.

## Szenarien

Während jedes Szenarios wird jede Sekunde der Zustand beider Server gelesen;
sind beide eingerichtet und gleichzeitig `aktiv`, ist das Szenario sofort
nicht bestanden. Ebenso, wenn beide Datenbanken länger als 15 s schreibbar
sind (ein neu gestarteter früherer aktiver Server startet PostgreSQL kurz
schreibbar, bis pdh-ha ihn abschaltet). Der Schreib-Generator `writer.sh`
fügt laufend Zeilen ein und merkt jede bestätigte; nach dem Szenario muss
jede bestätigte Zeile auf dem aktiven Server liegen. Ergebnis: `BESTANDEN` oder `NICHT BESTANDEN: <Grund>`,
Rückgabewert 0 bzw. 1.

| Szenario | Ablauf | Erwartung |
|---|---|---|
| `aktiv-weg` | node1 stoppen | node2 wird `aktiv` |
| `leitung-weg` | node2 von `lan` und `vpn` trennen | node1 bleibt `aktiv`, node2 `passiv` (Zeuge sieht node1) |
| `zeuge-weg` | Zeugen stoppen | node1 bleibt `aktiv`, node2 `passiv` |
| `hin-und-her` | node1 (aktiv) ganz trennen, node2 übernimmt, node1 verbinden, Rollentausch zurück – wiederholt | genau 3 Übernahmen, beim 4. Mal keine Übernahme, Wartung an auf beiden |
| `rueckkehr` | node1 stoppen, node2 übernimmt, node1 starten | node1 durchläuft `wiedereingliederung` und wird `passiv` |
| `neustart-aktiv` | node1 neu starten, node2 läuft | node1 wieder `aktiv`, node2 `passiv` |
| `version-anders` | node2 mit Version 0.0.0, dann node1 stoppen | keine Übernahme, node2 meldet „Versionen unterschiedlich“ |
| `beobachtung` | Automatik `observe`, node1 stoppen | node2 bleibt `passiv`, Amtszeit gleich, Ereignis „würde jetzt“ |
| `tausch` | Rollen hin und zurück tauschen bei laufenden Speicherungen | Spiegel jeweils synchron, Pflicht-Testumschaltung vermerkt |
| `aktiv-weg-echt` | node1 stoppen, später starten | node2 übernimmt mit allen Zeilen, node1 wird synchroner Standby ohne abgezweigte Daten |
| `asynchron-zeuge` | node2 von `lan`/`vpn` trennen, dann node1 stoppen | node1 asynchron erst nach Zeugen-Bestätigung; veralteter node2 übernimmt nicht |
| `asynchron-ohne-zeuge` | Zeugen stoppen, node2 trennen | node1 bleibt synchron, keine Speicherung bestätigt; danach wieder synchron |
| `erzwingen-abgezweigt` | node2 stoppen, node1 schreibt asynchron, node1 stoppen, auf node2 „Übernahme erzwingen“, node1 starten | node1 wird Standby und meldet abgezweigte Daten |
| `rewind-fehler` | wie `erzwingen-abgezweigt`, `pg_rewind` auf node1 scheitert | Neukopie, abgezweigte Daten trotzdem gesichert |
| `slot-grenze` | Höchstmenge 1 GB, node2 stoppen, 1,3 GB schreiben, node2 starten | Slot ungültig, node2 per Neukopie wieder synchroner Standby |

Der Rollentausch in `hin-und-her` zählt nicht zur Tageshöchstzahl; so
übernimmt immer node2, und dessen Zähler erreicht die Grenze.

In `erzwingen-abgezweigt` und `rewind-fehler` gehen die Zeilen aus der Zeit,
in der nur node1 schrieb, gewollt in die abgezweigten Daten; sie zählen nicht
zur Prüfung. `slot-grenze` braucht rund 4 GB freien Platz.
