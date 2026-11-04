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
bash deploy/ha-lab/ha-lab.sh race        # go test -race in golang:1.24, mit echtem rsync
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
| `vip-absender` | feste Adresse am Halter neu setzen, danach die Adresse als `/24` setzen | Absender bleibt die feste Adresse, die Adresse bleibt erreichbar; `/24` wird binnen 5 s auf `/32` umgestellt |
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

### Datei-Spiegelung und Nextcloud im Verbund

Die Szenarien schalten die Spiegelung selbst ein (`pdh-ha settings`, Abstand 30 s,
Alarm nach 1 min) und warten auf den Erstabgleich. Quellen liegen wie im Betrieb
unter `/opt/pdh-server` (eigenes Volume), der Papierkorb daneben. Die Server haben
dafür `rsync`.

Nextcloud ist die Attrappe `nc-dummy` (`PDH_HA_NC_CMD`), Marke
`/run/nc-dummy.running`, Protokoll `/var/lib/pdh-lab/nc.log` mit dem Stand der
Datenbank bei jedem Start; Schalter `fail-nc-start`. Sie wirkt erst mit `occ` im
Programmordner und „Nextcloud mitnehmen“: `/var/www/nextcloud` (nur `occ` und
`config/config.php`) und `/srv/nc-data` liegen anfangs nur auf node1 und kommen über die
Spiegelung auf node2. Der Prüflauf liest die Attrappen-Tabellen `oc_filecache`,
`oc_storages` und `oc_mimetypes` in der Datenbank `nextcloud`.

Eine hochgeladene Datei gilt als bestätigt, wenn sie geschrieben war, bevor
`pdh-dummy stop` fertig ist (gemeinsame Sperrdatei, wie das echte pdh, das beim Stoppen
auf offene Anfragen wartet). Wo zwei schreibende Server ausgeschlossen werden, misst
`rw_probe` im Container alle 0,1 s die Schreibbarkeit; `pg_rewind_wrap` ersetzt `pg_rewind`
durch ein Skript, das jeden Aufruf protokolliert und den echten Lauf ausführt oder nach der ersten Änderung am Datenordner abbricht. `rw_probe_check` und `rw_probe_only` bestehen nicht, wenn die Messung am alten Server nie „schreibbar“ gelesen hat.

| Szenario | Ablauf | Erwartung |
|---|---|---|
| `spiegel-datei` | Datei mit Leerzeichen und Umlaut auf node1, Besitzer `www-data`, danach ändern | binnen 10 s auf node2 mit gleichem Inhalt und Besitzer; alte Fassung im Papierkorb |
| `spiegel-loeschen` | Datei auf node1 löschen | binnen eines Abstands auf node2 weg und im Papierkorb, Grundbestand bleibt, Zähler der Rückkehrer 0 |
| `spiegel-tausch` | Hochladen auf node1 bis zum Stopp von pdh, Rollen tauschen | keine bestätigte Datei fehlt; nie zwei schreibbare Datenbanken (Messung 0,1 s); danach läuft die Spiegelung von node2 nach node1, Papierkorb von node1 leer |
| `spiegel-tausch-frist` | 50 MB bei 200 KB/s, Frist 10 s, Tausch | Tausch bricht ab („Rollentausch abgebrochen“, Grund genannt), node1 aktiv mit pdh und schreibbarer Datenbank, gleiche Amtszeit, keine Wartung, auf node2 die alte Fassung; nach Freigabe der Bandbreite kommt die Datei an; node2 war nie schreibbar, node1 am Ende schon |
| `spiegel-tausch-wiederholung` | wie `spiegel-tausch-frist`, dann Bandbreite frei und zweiter Tausch | zweiter Tausch gelingt in derselben Amtszeit (Amtszeit genau +1), große Datei auf node2; nie zwei schreibbare Datenbanken |
| `spiegel-ausfall` | Nextcloud an, 30 MB unterwegs bei 200 KB/s, `oc_filecache` kennt die Datei schon, node1 hart anhalten | node2 übernimmt, Alarm und Status nennen `alice/files/unterwegs.bin` (und nicht die vorhandene Datei), Attrappe läuft auf node2 |
| `spiegel-rueckkehr` | node1 hart anhalten, Datei in den gestoppten node1 legen, node2 übernimmt, node1 starten; rsync von Hand mit alter Amtszeit | Datei im Papierkorb von node1, Zähler 1 auf beiden; alter Absender abgelehnt (am aktiven node2 „nicht passiv“, nach dem Rücktausch am passiven node2 „alte Amtszeit“, je ein Ereignis) |
| `spiegel-heartbeat` | 500 MB auf node1, dazu laufend neue Fassungen (2 min) | keine Rolle und Amtszeit ändert sich, beide sehen sich in jeder Sekunde, keine Ereignisse zu Tausch oder Übernahme; danach alle Dateien gleich |
| `spiegel-nextcloud` | Tausch, Start von Hand am passiven Server, Neustart des passiven und des aktiven Servers, dann Tausch mit `fail-nc-start` | Attrappe läuft nur am aktiven Server und nie auf beiden; Start erst bei schreibbarer Datenbank; Startfehler gibt Alarm, aber keine Umschaltung und keine Wartung |
| `spiegel-aus` | Standard (aus): Datei auf node1, `mirror run`, rsync von Hand, Tausch hin und zurück; dann ein- und wieder ausschalten | nichts übertragen, `mirror run` abgelehnt, Empfänger lehnt ab („Spiegelung ist aus“, Ereignis), Tausch wie bisher; nach dem Ausschalten nichts Neues mehr |
| `spiegel-uebernahme-im-abgleich` | 50 MB bei 200 KB/s, Frist 90 s, Tausch hängt im letzten Abgleich, node2 „Übernahme erzwingen“ | nie zwei schreibbare Datenbanken (Messung 0,1 s, Wächter sofort); node1 wird über genau einen echten `pg_rewind` Standby, keine Neukopie, Merker weg; Spiegelung läuft von node2 nach node1 |
| `spiegel-rejoin-abbruch` | node1 hart anhalten, node2 übernimmt und schreibt 300 MB, node1 startet, echtes `pg_rewind` wird beendet, sobald es den Datenordner verändert hat (sonst nicht bestanden), pdh-ha stirbt mit; dann node2 anhalten, node1 neu starten, node2 starten, `pdh-ha rejoin` | Datenbank auf node1 bleibt gestoppt, Markierdatei und Alarm, „Übernahme erzwingen“ abgelehnt, auch ohne Partner; nach dem Neustart wieder gestoppt; `rejoin` kopiert neu ohne weiteren `pg_rewind`, Markierdatei weg, synchroner Standby |
| `spiegel-rolling-update` | erst im Erstabgleich Update und Tausch versuchen, dann Aufbau wie `update-ok` mit Hochladen bis zum Tausch | im Erstabgleich beides abgelehnt („Erstabgleich“); danach Update abgeschlossen, node2 aktiv, keine bestätigte Datei fehlt, Spiegelung in neuer Richtung; nie zwei schreibbare Datenbanken |
| `spiegel-ruecknahme-uebernahme` | 100 MB bei 200 KB/s, Frist 10 s; sobald die Abgabe als gescheitert gemeldet ist, node2 „Übernahme erzwingen“ (lehnt node2 ab, weil node1 schon wieder aktiv ist, neuer Versuch, höchstens 6) | nie zwei schreibbare Datenbanken (Wächter sofort, Messung 0,1 s); node1 endet passiv, abgeschottet oder als Standby |
| `spiegel-netz-weg-im-abgleich` | 50 MB bei 200 KB/s, Frist 60 s, node1 verliert im letzten Abgleich alle Netze, node2 übernimmt mit dem Zeugen, node1 kommt zurück | node1 wird nie schreibbar und geht nach der Frist auf abgeschaltet; nach der Rückkehr über genau einen echten `pg_rewind` Standby |

`spiegel-heartbeat` braucht rund 2,5 GB freien Platz (Dateien auf beiden Servern und alte Fassungen im Papierkorb).
