# Ausfallschutz Teil 3 – Datenbank-Cluster

Stand: 2026-10-25 · Status: Entwurf zur Freigabe ·
Gesamt-Architektur: `2026-10-24-ausfallschutz-design.md` (Abschnitte 6, 7, 12, 14) ·
Vorgänger: `2026-10-25-pdh-ha-kern-design.md`

## 1. Ziel und Umfang

Teil 3 macht aus dem beobachtenden `pdh-ha` einen echten Datenbank-Spiegel:
Streaming-Replikation vom aktiven auf den passiven Knoten, synchron im
Normalfall, asynchron nur mit Bestätigung des Zeugen, echte Aktionen für
Hochstufen, Abgeben, Abschalten und Wiedereingliederung (`pg_rewind`, sonst
Neukopie) samt Sicherung abgezweigter Daten.

Im Umfang:

- Replikation (TLS, eigener Benutzer, Slots), synchron/asynchron, echter
  `data_current` im Heartbeat.
- `pgActor` in `cmd/pdh-ha` für `promote`, `demote`, `fence`, `unfence`,
  `rejoin`; Abgleich von Dienst und Standby mit der Rolle.
- Einstellung „Automatische Umschaltung: Beobachtung | echt“.
- Vollständiger Knoten 2 (`pdh-ha-join.sh --voll`): PDH-Programm, Update-Agent,
  PostgreSQL als Standby, Konfiguration vom aktiven Knoten.
- Netzlaufwerk-Sicherung nur auf dem aktiven Knoten (Sperre scharf, R20).
- Oberfläche, Testlabor mit PostgreSQL, Handbuch, Version 0.76.0.

Nicht im Umfang: Lesemodus des PDH auf dem passiven Knoten (Teil 4 – bis dahin
ist der Dienst `pdh` dort gestoppt), cloudflared/virtuelle IP/DNS (Teil 5 –
`vip_*` bleibt Beobachtung), laufende Spiegelung von Dateien und Konfiguration
(Teil 6), Samba (Teil 7).

Entscheidungen des Nutzers:

- **Variante A:** Replikation, `data_current` und Wiedereingliederung echt;
  Handbefehle („Rollen tauschen“, „Übernahme erzwingen“) echt; automatische
  Übernahme und Selbst-Abschaltung standardmäßig Beobachtung, bis Teil 5 den
  Zugangsweg mitschaltet.
- Knoten 2 bekommt eine vollständige PDH-Installation; `pdh` läuft nur auf dem
  aktiven Knoten.
- `pdh-ha` steuert PostgreSQL selbst (kein Umweg über den Update-Agenten, kein
  Patroni/repmgr).

Testumgebung: Knoten 1 `pdh` (10.10.0.14), Knoten 2 `pdh-server` (10.10.0.17),
Zeuge Container (10.10.0.231) – alles Testdaten. Der Produktivserver
10.10.0.202 wird nie Knoten.

## 2. Replikation

### 2.1 Verbindung

- Benutzer `pdh_replica` (`REPLICATION`, `LOGIN`), Passwort 32 Byte zufällig,
  scram-sha-256. Der CA-Halter erzeugt es bei der Einrichtung; neue Knoten
  erhalten es in der Beitrittsantwort bzw. über den Befehl `--voll` (über die
  mTLS-Verbindung). Ablage `/etc/pdh/ha/replica.pass` (0600 root); für
  PostgreSQL als `primary_conninfo`-Bestandteil in `conf.d/pdh-ha.conf`
  (0600 postgres).
- Zusätzlich die Leserechte, die `pg_rewind` an der Quelle braucht
  (`pg_ls_dir`, `pg_stat_file`, `pg_read_binary_file`,
  `pg_read_binary_file(text,bigint,bigint,boolean)`), kein Superuser.
- TLS: PostgreSQL bekommt das Knotenzertifikat der Cluster-CA als
  `ssl_cert_file`/`ssl_key_file` (Kopie mit Eigentümer postgres, 0600), der
  Standby verbindet mit `sslmode=verify-ca sslrootcert=<ca.crt>`.
- `pg_hba.conf` (eigener, markierter Block, von `pdh-ha` verwaltet):
  `hostssl replication pdh_replica <Adresse>/32 scram-sha-256` und
  `hostssl all pdh_replica <Adresse>/32 scram-sha-256` (für `pg_rewind`) je
  Adresse des anderen Knotens. `listen_addresses` um die eigenen Clusteradressen
  ergänzt. `ufw` öffnet 5432 nur für die Adressen des anderen Knotens.
- `primary_conninfo` nennt alle Adressen des aktiven Knotens als Mehrfach-Host
  (`host=a,b port=5432,5432`), `application_name=pdh_ha_<Kennung>`.

### 2.2 Slots und Platz

- Je Standby ein physischer Slot `pdh_ha_<Kennung>` auf dem aktiven Knoten.
- `max_slot_wal_keep_size` (Einstellung, Standard 20 GB). Wird der Slot
  ungültig, braucht der Standby bei Rückkehr eine Neukopie (Ereignis + Alarm).
- `wal_log_hints = on` (für `pg_rewind`), `hot_standby = on`,
  `wal_level = replica` (mindestens), `max_wal_senders`/`max_replication_slots`
  ausreichend. Änderungen, die einen Neustart brauchen, macht die Einrichtung
  einmal mit Ansage.

### 2.3 Synchron und asynchron

- Normalfall: `synchronous_standby_names = 'pdh_ha_<Partner>'`,
  `synchronous_commit = on` – eine bestätigte Speicherung liegt auch auf der
  Platte des Standby.
- Fehlt der Standby in `pg_stat_replication` (Zustand `streaming`) länger als
  „asynchron nach“ (Einstellung, Standard 10 s), schaltet der aktive Knoten auf
  asynchron (`synchronous_standby_names = ''`, Neuladen) – **nur nach
  Bestätigung des Zeugen** (2.4). Bis dahin können Speicherungen hängen.
- Zurück auf synchron, sobald der Standby wieder `streaming` ist und die
  Verzögerung unter dem Grenzwert liegt (Einstellung, Standard 16 MB); danach
  Meldung „wieder aktuell“ an den Zeugen.

### 2.4 Zeugen-Bestätigung „nicht aktuell“

- Neue Nachrichten: `stale {node, term, lsn}` (aktiv → Zeuge) mit Antwort
  `stale_ack {node, term}`; `current {node, term}` mit `current_ack`.
- Der Zeuge speichert die Liste „nicht aktuell“ in `state.json` **vor** der
  Antwort und verweigert einem dort eingetragenen Knoten jede Stimme. Ein
  Eintrag wird nur durch `current` des aktiven Knotens derselben oder höheren
  Amtszeit entfernt.
- Der aktive Knoten schaltet erst nach `stale_ack` auf asynchron. Erreicht er
  den Zeugen nicht, bleibt er synchron (Speicherungen hängen); ohne Mehrheit
  schaltet er sich ohnehin nach der Selbst-Abschaltzeit ab.
- Der aktive Knoten vermerkt „Partner nicht aktuell“ zusätzlich in seinem
  eigenen `state.json` und im Heartbeat; der zurückkehrende Partner übernimmt
  das als `data_current = false`.
- **Duo:** kein Zeuge; der aktive Knoten schaltet nach „asynchron nach“ ohne
  Bestätigung um. Ein Datenverlust ist nur im bekannten Restfall (Direktwege
  weg, beide sehen das Gateway) möglich; Anzeige „eingeschränkter
  Ausfallschutz“ und Handbuch nennen das.

### 2.5 `data_current`

Der passive Knoten meldet `data_current = true` (`data_assumed = false`), wenn:
seine Datenbank Standby ist und streamt, er als synchroner Standby geführt wird
(aus dem Heartbeat des aktiven Knotens), die Einspiel-Verzögerung unter dem
Grenzwert liegt und weder er selbst noch der aktive Knoten „nicht aktuell“
vermerkt hat. Fällt der aktive Knoten weg, gilt der zuletzt gemeldete Wert.
Der Heartbeat-Wert `pg` wird echt: `primary`, `standby`, `stopped`, `error`;
neu `lsn`, `lag_bytes`, `sync` (bool), `timeline`.

## 3. Aktionen

### 3.1 Beobachtung und echt

- Neue Einstellung `auto_mode`: `observe` (Standard) | `real`.
- `internal/ha`: `Action` erhält `Manual bool`. Automatische Entscheidungen
  bei `observe` ändern **weder Rolle noch Amtszeit** und erzeugen nur das
  Ereignis „würde jetzt: …“ (anders als Teil 2). Bei `real` und für
  Handbefehle (swap/handover, force_takeover) laufen Rolle, Amtszeit und
  Aktionen echt.
- Im PDH bei `real` dauerhaft der Hinweis „Zugangsweg wird erst mit Teil 5
  umgeschaltet – nach einer Übernahme ist das PDH nur unter der Adresse des
  neuen aktiven Knotens erreichbar“.

### 3.2 `pgActor`

Systembefehle als root über eine austauschbare Ausführer-Schnittstelle (für
Tests vortäuschbar); `psql` über `runuser -u postgres`; Cluster über
`pg_lsclusters`/`pg_ctlcluster` (Ubuntu-Paket).

| Aktion | Ablauf |
|---|---|
| `demote` (Tausch, aktiv) | Konfiguration an Partner schieben (4.4) → `pdh` stoppen → warten, bis Partner `replay_lsn` = eigene `pg_current_wal_lsn()` (Frist 60 s; sonst `pdh` wieder starten, Abbruch, Ereignis) → PostgreSQL sauber stoppen → `standby.signal` + `primary_conninfo` auf den Partner → `handover` senden → PostgreSQL starten (folgt nach der Hochstufung der neuen Timeline) |
| `promote` | Prüfen: eigene DB Standby, `data_current` (bei erzwungen übersprungen, Warnung) → `pg_promote(wait => true)` → `pg_is_in_recovery() = false` prüfen → Slot und `synchronous_standby_names` für den Partner → `pdh` starten → Update-Agent stößt Grundsicherung an (5) |
| `fence` | `pdh` stoppen → `default_transaction_read_only = on`, Neuladen → Client-Sitzungen außer Replikation beenden |
| `unfence` | `default_transaction_read_only` entfernen, Neuladen → `pdh` starten |
| `rejoin` | Abschnitt 3.4 |
| `vip_take`/`vip_release` | weiterhin nur Ereignis (Teil 5) |

Fehler → `wartung` + Alarm (wie Teil 2). Ein `demote` mit Fristüberschreitung
bricht ab, bevor PostgreSQL gestoppt ist.

`ActFence` bei Speicherfehler (Teil-2-Ruling) bleibt: Kann `pdh-ha` seinen
Zustand nicht speichern, schaltet sich der aktive Knoten ab.

### 3.3 Abgleich

Beim Start und alle 30 s, nur in die sichere Richtung: auf einem nicht aktiven
Knoten läuft `pdh` nicht; ein Standby zeigt mit `primary_conninfo` auf den
aktuellen aktiven Knoten; ein nicht aktiver Knoten mit schreibbarer Datenbank
wird abgeschaltet (`fence`) und meldet Alarm. Der Abgleich stuft nie hoch und
ruht, solange eine Aktion läuft (z. B. ist beim `demote` die Rolle schon
abgegeben, die Datenbank aber noch schreibbar).

### 3.4 Wiedereingliederung

Auslöser: höhere Amtszeit gesehen und eigene Datenbank ist nicht Standby der
Timeline des aktiven Knotens.

1. `pdh` und PostgreSQL stoppen.
2. `pg_rewind --dry-run --source-server=<conninfo>` ermittelt den Trennpunkt.
   Eigener Stand nicht dahinter → weiter mit 4.
3. **Abgezweigte Daten sichern:** freien Platz prüfen; alte Datenbank
   abgeschottet starten (nur `localhost`, eigener Port, `default_transaction_read_only`,
   keine Archivierung, keine Replikation) → `pg_dumpall | gzip` nach
   `/var/lib/pdh-ha/abgezweigt/<Zeit>-amtszeit<T>/dump.sql.gz`; `pg_waldump`
   über den Bereich Trennpunkt → eigenes Ende → `summary.json` (Tabelle,
   neu/geändert/gelöscht); stoppen. Fehlschlag → nichts überschreiben,
   `wartung` + Alarm.
4. `pg_rewind --source-server=<conninfo>` (Benutzer `pdh_replica`).
5. Fehlschlag → alten Datenordner umbenennen (`…main.alt-<Zeit>`) →
   `pg_basebackup -R -X stream -S pdh_ha_<Kennung>` → bei Erfolg den
   umbenannten Ordner löschen, bei Fehlschlag zurückbenennen, `wartung` + Alarm.
6. `standby.signal` + `primary_conninfo`, starten, auf `streaming` warten →
   `passiv`.

Abgezweigte Daten: Alarm; Kurzangaben (Zeit, Amtszeit, Größe, Tabellen) im
Heartbeat und in `GET /v1/status`, Anzeige auf beiden Knoten; nie automatisch
gelöscht; `POST /v1/diverged/delete {id}` nach Bestätigung im PDH.

## 4. Vollständiger Knoten 2

### 4.1 Befehl

`pdh-ha-join.sh --voll <Code>` (neuer Knoten) bzw.
`pdh-ha-join.sh --update <Commit> --voll` (bestehender Knoten).

### 4.2 Ablauf

1. Vorprüfung, sonst Abbruch: Knoten nicht aktiv und nicht CA-Halter; aktiver
   Knoten erreichbar; PostgreSQL-Hauptversion gleich der des aktiven Knotens
   (Paket notfalls aus PGDG; andere Hauptversion abgelehnt); Platz für die
   Kopie (Größe vom aktiven Knoten).
2. Schutz vorhandener Daten: Liste lokaler Datenbanken außer `postgres`,
   `template0`, `template1` mit Größe; Fortsetzen nur nach Eingabe des
   Hostnamens; vorher `pg_dumpall` nach `/var/lib/pdh-ha/vorher/<Zeit>.sql.gz`.
3. Installation wie `pdh-vm-prepare.sh` (PDH auf demselben Commit,
   Update-Agent, PostgreSQL), ohne eigene Datenbank-Einrichtung.
4. Konfiguration vom aktiven Knoten (4.4), Replica-Passwort, CA-Zertifikat.
5. Erstkopie: `pg_basebackup -R -X stream -S pdh_ha_<Kennung> -C` vom aktiven
   Knoten, Einstellungen (2.1, 2.2) setzen, starten, auf `streaming` warten.
6. Dienststeuerung (4.3) auf beiden Knoten aktivieren.

### 4.3 Dienst `pdh` unter `pdh-ha`

Im Duo/Trio wird `pdh.service` nicht mehr beim Hochfahren gestartet
(`systemctl disable`); `pdh-ha` startet ihn, wenn der Knoten aktiv ist (Startregeln
Teil 2 §5.4), und stoppt ihn sonst. Im Einzelbetrieb unverändert; beim Entfernen
eines Knotens (Duo → Einzel) wird der Autostart wiederhergestellt.

### 4.4 Konfiguration

`.env` des PDH und `/etc/pdh` ohne `/etc/pdh/ha/` werden als Archiv über die
mTLS-Verbindung (`POST /v1/config/pull`, nur Knoten-Zertifikate) übertragen –
bei `--voll` und vor jedem `demote`. Hochgeladene Dateien folgen erst mit Teil 6:
„Rollen tauschen“ warnt „Dateien werden erst mit Teil 6 gespiegelt – auf dem
anderen Knoten fehlen neue Anhänge“.

### 4.5 Pflicht-Testumschaltung

`auto_mode = real` lässt sich erst einschalten, nachdem einmal erfolgreich hin
und zurück getauscht wurde (Vermerk in `cluster.json`, Schritt im Reiter). Die
Wartung („Automatik an/aus“) bleibt davon unberührt – bei `observe` schaltet sie
ohnehin nichts.

## 5. Netzlaufwerk-Sicherung im Verbund

- Sperre scharf (R20 aufgehoben, `haGateEnforced = true`): Grundsicherung und
  Dateiabgleich nur, wenn `pdh-ha` „aktiv“ meldet **und** die lokale Datenbank
  nicht in Recovery ist. `pdh-ha` eingerichtet, aber nicht erreichbar → Pause,
  nach 15 min Alarm. Ohne `pdh-ha` unverändert. Teil-2-Spec §4.4 wird
  entsprechend angepasst.
- Ein Standby archiviert nicht (`archive_mode = on`), der Zwischenspeicher wächst
  auf dem passiven Knoten nicht. Vorhandene Dateien im Zwischenspeicher
  verschiebt der Agent unabhängig von der Rolle aufs Laufwerk.
- Nach einer Hochstufung (neue Timeline) stößt der Agent sofort eine
  Grundsicherung an. `pdh-restore.sh` stellt die neueste Timeline her (im Labor
  geprüft).
- Die Sicherungs-Einstellungen stehen in der Datenbank, die Zugangsdaten kommen
  mit 4.4; der neue aktive Knoten bindet das Laufwerk beim Start des PDH ein. Der
  Reiter zeigt, welcher Knoten zuletzt gesichert hat.

## 6. Oberfläche

Reiter „Ausfallschutz“:

- Je Knoten: Datenbank (schreibend/Standby/gestoppt/Fehler), LSN, Verzögerung,
  synchron/asynchron, „nicht aktuell“, letzte Sicherung von diesem Knoten.
- Einstellungen: automatische Umschaltung Beobachtung/echt (mit Hinweis 3.1),
  „asynchron nach“, Verzögerungsgrenze, Höchstmenge zurückgehaltener Änderungen.
- Schritt „Pflicht-Testumschaltung“, Liste „Abgezweigte Daten“ mit
  „Geprüft – löschen“, Warnung beim Tausch (4.4).
- Alarme neu: asynchron geschaltet, wieder synchron, Slot ungültig,
  abgezweigte Daten, Wiedereingliederung fehlgeschlagen, nicht aktiver Knoten
  mit schreibbarer Datenbank, Sicherung pausiert > 15 min.

## 7. Testen

- **`internal/ha`:** Tabellen und Simulation – Zeuge verweigert Stimme bei
  „nicht aktuell“; asynchron nur nach `stale_ack` (Trio), ohne im Duo;
  Beobachtung ändert weder Rolle noch Amtszeit; Handbefehle echt.
  Fuzz-Invarianten zusätzlich: nie wird ein Knoten mit `data_current = false`
  automatisch aktiv; nie zwei Knoten, die sich beide für schreibend halten.
- **`cmd/pdh-ha`:** `pgActor` mit vorgetäuschtem Ausführer (Reihenfolge je
  Aktion, Abbruch bei Frist vor dem Stopp, kein Überschreiben bei
  fehlgeschlagenem Abzug, Neukopie mit Zurückbenennen), Abgleich, Nachrichten
  `stale`/`current`, Konfigurationsübertragung nur für Knoten-Zertifikate,
  Replica-Passwort im Beitritt.
- **Update-Agent:** Sperre nach Rolle und Recovery, Zwischenspeicher immer
  verschoben, Grundsicherung nach Timeline-Wechsel.
- **Testlabor** `deploy/ha-lab/`: Knoten mit PostgreSQL derselben Hauptversion
  wie Ubuntu 26.04, Dienst `pdh` durch einen Platzhalter ersetzt,
  Schreib-Generator (fortlaufende Zeilen, bestätigte merken). Szenarien:
  `tausch`, `erzwingen-abgezweigt`, `rewind-fehler`, `asynchron-zeuge`,
  `asynchron-ohne-zeuge`, `slot-grenze`, `aktiv-weg-echt`, dazu die
  bestehenden, `alle`. Ständiger Wächter: höchstens eine schreibbare
  Datenbank; nach jedem Szenario jede bestätigte Zeile vorhanden.
- **Echtes Netz:** `--voll` auf 10.10.0.17, Pflicht-Tausch, „Übernahme
  erzwingen“ mit Rückkehr und `pg_rewind`, optional `real` mit VM-Stopp.

## 8. Handbuch und Version

Kapitel „Ausfallschutz“ ergänzen: Datenbank-Spiegel einrichten (`--voll`),
synchron/asynchron und hängende Speicherungen, Rollen tauschen mit Hinweis auf
Dateien, Übernahme erzwingen, abgezweigte Daten prüfen und einspielen,
Duo-Restrisiko, Beobachtung/echt. Skizze der Übersicht anpassen. Changelog
unter „Neu“, Version 0.76.0.

## 9. Offene Punkte

- Kopie von `ca.key` auf den zweiten Knoten (Beitritte nach Ausfall des ersten)
  – weiter offen, nicht in Teil 3.
- Weitere Datenbanken im selben PostgreSQL (z. B. Nextcloud) werden
  mitrepliziert; Nextcloud selbst schaltet nicht um.
