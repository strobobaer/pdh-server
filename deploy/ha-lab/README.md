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

Netze: `lan` (Server + Gateway + Zeuge), `vpn` (Server), `wit` (Server + Zeuge).
Die Server erreichen sich nur über `lan` und `vpn`, den Zeugen über `wit`.
Der Zeuge hängt zusätzlich am `lan`, damit er beim Adresstest die
mitwandernde Adresse prüfen kann; als Weg meldet er nur seine `wit`-Adresse.
Server und Gateway dürfen Adressen setzen (`NET_ADMIN`, `NET_RAW`); die
Server haben dafür `ip` und `arping` (Paket `iputils-arping`).

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

### Zugangsweg (mitwandernde Adresse, Skripte, Umstellung)

Mitwandernde Adresse im Labor: `10.77.1.50/24`. Sobald sie eingeschaltet ist,
liest ein Wächter alle 0,5 s die Adressen beider Server; tragen beide sie
gleichzeitig, ist das Szenario sofort nicht bestanden.

| Szenario | Ablauf | Erwartung |
|---|---|---|
| `vip-tausch` | Automatik `observe`; `vip test`, Adresse einschalten, Rollen tauschen | erst node1 Halter, binnen 10 s node2; `gw` erreicht die Adresse |
| `vip-ausfall` | node1 anhalten | node2 übernimmt mit der Adresse |
| `vip-abschaltung` | node1 von `vpn`/`wit` trennen, seine `lan`-Karte abschalten | node1 schaltet ab (3 Takte + 5 s) und gibt die Adresse binnen 2 s ab; node2 übernimmt sie |
| `vip-duplikat` | `gw` setzt die Adresse selbst, dann einschalten | node1 übernimmt nicht, Alarm „wird noch von einem anderen Gerät benutzt“; nach dem Entfernen auf `gw` übernimmt node1 binnen 5 s |
| `vip-entfernt` | Adresse am Halter mit `ip addr del` entfernen | binnen 3,5 s wieder da (bis 1 s Takt des Abgleichs + 2 s Duplikat-Prüfung + Setzen/Messen) |
| `vip-neustart` | aktiven node1 anhalten, node2 übernimmt, node1 starten | node1 holt die Adresse nicht, wird Standby |
| `hooks` | Skript `/etc/pdh/ha-hooks/10-log` auf beiden, Skripte ein, Rollen tauschen, Skript nur auf node2 ändern | Protokoll: node2 `aktiv`, node1 `passiv`, gleiche Amtszeit, Adresse; vor der Änderung Status „gleich“ auf beiden, danach „anders als beim Partner“ |
| `umstellung` | Automatik `observe`; node1 bekommt 10.77.1.13, `addrs set … --neustart`, Test mit 10.77.1.60, 10.77.1.11 wird mitwandernde Adresse, Rollen tauschen | Spiegel wieder synchron; erst node1, dann node2 hält 10.77.1.11 |

In `vip-abschaltung` bleibt die Karte im Container (abgeschaltet statt
getrennt) – nur so ist zu sehen, dass pdh-ha die Adresse selbst abgibt.
In `umstellung` setzt das Szenario die Absenderadresse mit
`ip route replace 10.77.1.0/24 dev <Karte> proto kernel scope link src 10.77.1.13`
(wie das Umstellungsskript), sonst gingen Verbindungen weiter von 10.77.1.11 aus.

### Update im Verbund

pdh und der Update-Agent sind Platzhalter: `lab-agent` „spielt einen Stand
ein“, indem er den Commit nach `/var/lib/pdh-lab/commit` schreibt (pdh-ha liest
ihn je Takt, `PDH_HA_COMMIT_FILE`) und `pdh-dummy` neu startet;
`install_norestart` merkt den Stand nur vor, `finish` setzt ihn ein. `rollback`
startet danach pdh-ha neu – wie `install_update.sh --rollback` im Betrieb (das
Einstiegsskript startet pdh-ha in einer Schleife wieder); `rollback_norestart`
nicht. Ausgangsstand ist `lab0`. `pdh-dummy` beantwortet `/health` auf Port 8090
(`socat`) mit der Version je Stand: `lab0` = Inhalt von `VERSION`, sonst
`<VERSION>-<Stand>` (z. B. `0.77.8-lab1`). Die Szenarien starten Aufträge ohne
`--version`; pdh-ha übernimmt die Zielversion von B nach dem Einspielen. Als
Primary mit einem Stand ungleich `lab0` legt pdh-dummy die Tabelle
`mig_<Commit>` in der Datenbank `lab` an („Datenbank-Änderung“); bis die Tabelle
da ist, meldet es `migrations_ok: false` – auch am Spiegel (eingeschränkter
Spiegel, der auf die Migration am Primary wartet).

Schalter (Dateien in `/var/lib/pdh-lab/`): `fail-install` (Einspielen
scheitert), `fail-start-<Commit>` (pdh startet mit diesem Stand nicht),
`fail-health-<Commit>` (`/health` antwortet 500), `slow-install` (Einspielen braucht 40 s). Läufe von `lab-agent` gehen nacheinander (Sperrdatei), wie beim echten Agenten. Die Prüffrist ist im Labor
60 s (`PDH_HA_UPDATE_VERIFY`), der Countdown 1 min. In der Sperre ruht der
Schreib-Generator, wo das Szenario einen Rückweg erwartet – im Betrieb schreibt
dann niemand, die Zeilen aus der Sperre gingen beim Zurücksetzen gewollt verloren.

| Szenario | Ablauf | Erwartung |
|---|---|---|
| `update-ok` | `update start lab1 --kind rolling` | node2 spielt lab1 ein, Tausch, node1 zieht nach; beide auf lab1, node2 aktiv, `mig_lab1` auf beiden; Ergebnis nennt die übernommene Version („Update auf 0.77.8-lab1 abgeschlossen“) |
| `update-b-vorher` | `fail-health-lab1` auf node2, dann Update | Prüffrist überschritten, node2 zurück auf lab0, lab1 als fehlerhaft gemerkt, keine `mig_lab1`; pdh-ha auf node2 startet beim Zurückdrehen neu – trotzdem genau ein Lauf und kein „braucht Hilfe“ |
| `update-b-nachher` | im Countdown `fail-health-lab1` auf node2 | nach dem Tausch legt node2 `mig_lab1` an, Prüfung scheitert, Zurücksetzen: node1 mit alter Datenbank aktiv, `mig_lab1` nirgends, lab1 fehlerhaft; „update abort“ im Zurücksetzen abgewiesen („Zurücksetzen läuft bereits“), genau ein Zurückdrehen, kein Dringend-Alarm |
| `update-abbruch` | in der Sperre erst Handtausch, Übernahme erzwingen und Wartung versuchen, dann `pdh-ha update abort` auf node2 | Handbefehle abgewiesen („zuerst pdh-ha update abort“), dann Zurücksetzen, Ergebnis „abgebrochen von Befehlszeile“, lab1 nicht fehlerhaft |
| `update-neustart` | node1 vor der Sperre neu starten; zweiter Auftrag: node2 in der Prüfung neu starten | erst „abgebrochen von Neustart“ mit beiden auf lab0, dann setzt node2 den Auftrag fort: abgeschlossen |
| `update-nicht-gruen` | node2 von `lan`/`vpn` trennen, Start mit Quelle „zeitplan“ und per Befehl | HTTP 409 bzw. Fehler, kein Auftrag |
| `update-nacheinander` | `update start lab1 --kind sequential` | node2 eingefroren, node1 startet mit lab1 neu, node2 zieht nach; node1 aktiv, beide auf lab1 |
| `update-nacheinander-fehler` | `fail-health-lab1` auf node1, Art „alle nacheinander“ | node2 wird mit seinem eingefrorenen Stand aktiv, node1 zurück auf lab0, `mig_lab1` nirgends, lab1 fehlerhaft |
| `update-tausch-frist` | Höchstdauer 10 min; im Countdown node2 trennen, nach dem Beginn des Zurücksetzens verbinden | Tausch-Befehl wird wiederholt, nach der Tauschfrist (3 min) Zurücksetzen, Ergebnis „Rollentausch nicht gelungen“, deutlich vor der Höchstdauer, lab1 nicht fehlerhaft, kein Dringend-Alarm |
| `update-nacheinander-gleichstand` | WAL-Einspielen auf node2 angehalten, Zeile auf node1 geschrieben, Start „alle nacheinander“ mit Trotzdem (`force`) | node1 wird abgeschottet, Gleichstand kommt nicht: node2 wird nicht eingefroren, Ergebnis „Spiegel nicht auf Gleichstand – Update abgebrochen, nichts geändert“, Zeile bleibt erhalten, lab1 nicht fehlerhaft |
| `update-abbruch-vorher` | `slow-install` auf node2 (Bau 40 s), Update starten, während des Baus `pdh-ha update abort` | Auftrag endet erst, nachdem node2 nach dem Bau wirklich auf lab0 zurückgedreht ist (letzter Lauf `rollback lab0`), beide bleiben auf lab0, lab1 nicht fehlerhaft |
| `update-verweigert` | `refuse-install` auf node2 (Labor-Agent meldet wie `install_update.sh` „…; nichts geaendert.“), Update starten | Auftrag bricht vor der Sperre ab, Ergebnis „nicht eingespielt“ mit Grund, beide bleiben auf lab0, lab1 **nicht** fehlerhaft, keine `mig_lab1` |
