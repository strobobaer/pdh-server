# Ausfallschutz Teil 3 – Datenbank-Cluster: Implementierungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pdh-ha` spiegelt PostgreSQL per Streaming-Replikation vom aktiven auf den passiven Knoten (synchron, asynchron nur mit Zeugen-Bestätigung) und führt Hochstufen, Abgeben, Abschalten und Wiedereingliederung echt aus; Knoten 2 wird ein vollständiger PDH-Knoten, auf dem `pdh` nur läuft, wenn er aktiv ist.

**Architecture:** Der Entscheidungskern `internal/ha` bekommt die Einstellung Beobachtung/echt (automatische Entscheidungen ändern bei Beobachtung weder Rolle noch Amtszeit), die „nicht aktuell“-Liste des Zeugen und eine reine Funktion für synchron/asynchron. `cmd/pdh-ha` erhält einen `pgActor` (Systembefehle über eine vortäuschbare `runner`-Schnittstelle, `pg_ctlcluster`/`psql`/`pg_rewind`/`pg_basebackup`), eine Aktions-Warteschlange im Hintergrund, die Replikationsüberwachung im Takt, die Konfigurationsübergabe über Port 7480 und den Befehl `replica-init`. Update-Agent, PDH-Reiter, Skripte und Testlabor ziehen nach.

**Tech Stack:** Go 1.24 (Standardbibliothek), PostgreSQL aus dem Ubuntu-Paket (`pg_ctlcluster`, `pg_lsclusters`, `pg_rewind`, `pg_basebackup`, `pg_waldump`, `pg_dumpall`), Bash, systemd, ufw, Docker Compose (nur Labor).

**Spec:** `docs/superpowers/specs/2026-10-25-ausfallschutz-db-cluster-design.md` (Vorgänger `2026-10-25-pdh-ha-kern-design.md`, Gesamt `2026-10-24-ausfallschutz-design.md`)

## Global Constraints

- Standard ist Beobachtung: `auto_mode` = `observe`. Bei `observe` ändern **automatische** Entscheidungen weder Rolle noch Amtszeit; Ereignis „würde jetzt: <Aktion> (Beobachtung)“. Handbefehle (`swap`/`handover`, `force_takeover`) und `rejoin` sind immer echt.
- `auto_mode = real` ist erst erlaubt, wenn `cluster.json` `swap_tested: true` hat; Meldung sonst: „Erst einmal Rollen tauschen – hin und zurück“.
- Neue Einstellungen (alle in `cluster.json` → `settings`): `async_after_s` 10 (3–300), `lag_limit_mb` 16 (1–1024), `slot_keep_gb` 20 (1–1000), `auto_mode` `observe`|`real`. Fehlt ein Feld in einer alten Datei, gilt der Standardwert.
- Replikationsbenutzer `pdh_replica`, Passwort 32 Byte zufällig (hex), Datei `/etc/pdh/ha/replica.pass` (0600 root); nie im Ereignisprotokoll, nie in Sicherungen, nie in Prozessargumenten (Passwort per Umgebungsvariable `PGPASSWORD`, stdin oder Datei).
- Slot und `application_name`: `pdh_ha_<Kennung>`, Kennung mit `-` → `_`.
- PostgreSQL-Port 5432; `ufw` öffnet 5432 nur für die Adressen des anderen Knotens (Kommentar `pdh-ha-pg`). `pg_hba.conf`-Block zwischen `# pdh-ha begin` und `# pdh-ha end`.
- PostgreSQL-Einstellungen nur in `conf.d/pdh-ha.conf` (0600 postgres, atomar geschrieben), nie per `ALTER SYSTEM`.
- `demote`: Frist 60 s für Gleichstand; bei Überschreitung `pdh` wieder starten, nichts gestoppt, Fehler.
- Abgezweigte Daten: `/var/lib/pdh-ha/abgezweigt/<JJJJMMTT-hhmmss>-amtszeit<T>/` mit `dump.sql.gz` und `summary.json`; nie automatisch gelöscht. Vorher-Sicherung bei `replica-init`: `/var/lib/pdh-ha/vorher/<JJJJMMTT-hhmmss>.sql.gz`.
- Überschreibbar für Tests/Labor: `PDH_HA_DIVERGED` (Ordner abgezweigt), `PDH_HA_SERVICE_CMD` (Skript `start|stop|is-active|enable|disable` statt systemctl), `PDH_REPO` (Standard `/opt/pdh-server`).
- Produktiv nie Knoten: 10.10.0.202. Testknoten 10.10.0.14 (pdh) und 10.10.0.17 (pdh-server), Zeuge 10.10.0.231.
- Texte Deutsch, Anwendersprache; Handbuch und `CHANGELOG.md`/`VERSION` laut AGENTS.md, Version 0.76.0.

## Review Focus

1. Alte `cluster.json`/`state.json` ohne die neuen Felder (laufender Testverbund) → Standardwerte statt Null; `Validate` lehnt nicht ab. (Task 1 `TestSettingsMissingFieldsDefault`)
2. Rollentausch: Die Übergabe darf nie gesendet werden, bevor `demote` die alte Datenbank gestoppt hat – sonst zwei schreibende Datenbanken. (Task 2 `TestHandoverWaitsForDemote`, Task 5 `TestDaemonHandoverAfterDemote`)
3. Zeuge nicht erreichbar, Standby weg → aktiver Knoten bleibt synchron (Speicherungen hängen), schaltet nie ohne `stale_ack` asynchron. (Task 3 `TestSyncStepTrioNeedsAck`, Task 8 `TestReplStaysSyncWithoutAck`)
4. `pdh-ha` stürzt während Neukopie ab (Datenordner umbenannt, neuer leer/halb) → beim Start wird der alte Ordner zurückgeholt statt ein leerer Cluster gestartet. (Task 7 `TestRecoverDataDirAfterCrash`)
5. Platte voll oder Abzug schlägt fehl → nichts überschrieben, `pg_rewind` nie aufgerufen. (Task 7 `TestRejoinDumpFailsKeepsData`)

---

## Dateiübersicht

| Datei | Aufgabe |
|---|---|
| `internal/ha/types.go` | neue Einstellungen, `PeerObs`-/`Input`-/`Output`-/`Persisted`-Felder, `Action.Manual`, `ActProbeVote` |
| `internal/ha/step.go` | Beobachtung ohne Rollenwechsel, Probestimme, Übergabe erst nach `demote` |
| `internal/ha/witness.go` | Stimme verweigern bei „nicht aktuell“, `MarkStale`/`ClearStale` |
| `internal/ha/sync.go` (neu) | `SyncStep`, `DataCurrent` |
| `cmd/pdh-ha/runner.go` (neu) | `runner`, `execRunner` |
| `cmd/pdh-ha/pg.go` (neu) | Cluster finden, Abfragen, Status, `pdh-ha.conf`, `pg_hba`-Block |
| `cmd/pdh-ha/svc.go` (neu) | Dienst `pdh` starten/stoppen/Autostart |
| `cmd/pdh-ha/pgactor.go` (neu) | `promote`, `demote`, `fence`, `unfence`; Abgleich |
| `cmd/pdh-ha/rejoin.go` (neu) | Wiedereingliederung, abgezweigte Daten, Absturz-Rückholung |
| `cmd/pdh-ha/repl.go` (neu) | Replikationsüberwachung im Takt, `stale`/`current`, `data_current` |
| `cmd/pdh-ha/primary.go` (neu) | aktive Seite einrichten (`ensurePrimary`), `POST /v1/config/pull` |
| `cmd/pdh-ha/replica.go` (neu) | Befehle `replica-init`, `peer-pg-major`, `should-run-pdh` |
| `cmd/pdh-ha/daemon.go`, `messages.go`, `transport.go`, `api.go`, `observe.go`, `main.go`, `store.go`, `system.go` | Anbindung |
| `scripts/pdh-ha-join.sh`, `scripts/install-pdh-program.sh` (neu), `scripts/pdh-vm-prepare.sh`, `install-update-agent.sh`, `install_update.sh` | `--voll`, `pdh` nicht auf passivem Knoten starten |
| `cmd/update-agent/ha.go`, `dr*.go` | Sperre scharf, Zwischenspeicher immer, Grundsicherung nach Timeline-Wechsel |
| `internal/web/ha.go`, `web/templates/widgets/ha.gohtml`, `help.gohtml`, `widgets/help_figures.gohtml` | Anzeige, Einstellungen, Abgezweigte Daten, Handbuch |
| `deploy/ha-lab/*`, `deploy/ha-witness/Dockerfile` | Labor mit PostgreSQL, Szenarien |
| `docs/superpowers/specs/2026-10-25-pdh-ha-kern-design.md` | §4.4 an R20/Teil 3 anpassen |

---

### Task 1: Neue Einstellungen und Felder im Entscheidungskern

**Files:**
- Modify: `internal/ha/types.go`
- Test: `internal/ha/types_test.go`

**Interfaces:**
- Produces:
  - `Settings` neu: `AsyncAfter time.Duration`, `LagLimit int64` (Bytes), `SlotKeep int64` (Bytes), `AutoMode string`; `func (s Settings) Real() bool` (`AutoMode == AutoReal`).
  - Konstanten `AutoObserve = "observe"`, `AutoReal = "real"`.
  - JSON: `async_after_s`, `lag_limit_mb`, `slot_keep_gb`, `auto_mode`; fehlend/0/leer → Standard.
  - `PeerObs` neu: `PG string`, `Sync bool`, `Stale bool`, `LSN uint64`, `LagBytes int64`, `Timeline uint32`.
  - `Persisted` neu: `Stale map[string]uint64` (JSON `stale,omitempty`).
  - `Action` neu: `Manual bool`; `ActProbeVote ActionKind = "probe_vote"`.
  - `Input` neu: `ProbeGranted uint64`, `DemoteDone uint64`, `DemoteFailed uint64`, `Would ActionKind`, `HandoverReadyAt time.Duration`; `Output` neu: `Would ActionKind`, `HandoverReadyAt time.Duration`.

- [ ] **Step 1: Failing tests**

```go
func TestSettingsMissingFieldsDefault(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.AsyncAfter != 10*time.Second || s.LagLimit != 16<<20 || s.SlotKeep != 20<<30 || s.AutoMode != AutoObserve {
		t.Fatalf("Standardwerte fehlen: %+v", s)
	}
	if s != DefaultSettings() || s.Validate() != nil {
		t.Fatalf("soll Standard sein: %+v", s)
	}
}

func TestSettingsNewRanges(t *testing.T) {
	for _, c := range []struct {
		mod  func(*Settings)
		want string
	}{
		{func(s *Settings) { s.AsyncAfter = 2 * time.Second }, "„asynchron nach“ muss zwischen 3 und 300 s liegen"},
		{func(s *Settings) { s.LagLimit = 2048 << 20 }, "Verzögerungsgrenze muss zwischen 1 und 1024 MB liegen"},
		{func(s *Settings) { s.SlotKeep = 0 }, "Höchstmenge zurückgehaltener Änderungen muss zwischen 1 und 1000 GB liegen"},
		{func(s *Settings) { s.AutoMode = "x" }, "Automatische Umschaltung muss „observe“ oder „real“ sein"},
	} {
		s := DefaultSettings()
		c.mod(&s)
		if err := s.Validate(); err == nil || err.Error() != c.want {
			t.Errorf("%v, möchte %q", err, c.want)
		}
	}
}
```

Dazu Rundlauf-Test: `DefaultSettings()` mit `AutoMode = AutoReal` → Marshal → Unmarshal gleich.

- [ ] **Step 2:** `go test ./internal/ha -run TestSettings` → FAIL (Felder fehlen).
- [ ] **Step 3:** Felder, Standardwerte, `Validate`-Regeln (Texte wie oben), `MarshalJSON`/`UnmarshalJSON` (MB/GB mit `<<20`/`<<30`, fehlend → Standard) in `types.go`.
- [ ] **Step 4:** `go test ./internal/ha` → PASS (bestehende Tests unverändert grün).
- [ ] **Step 5:** Commit `feat(ha): Einstellungen für Datenbank-Spiegel und Beobachtung/echt`.

### Task 2: Beobachtung ohne Rollenwechsel, Probestimme, Übergabe nach `demote`

**Files:**
- Modify: `internal/ha/step.go`, `internal/ha/step_test.go`, `internal/ha/sim_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces: Verhalten von `Step`:
  - Bei `!Settings.Real()`: `passiveTrio` sendet `ActProbeVote` statt `ActRequestVote` (gleiche Wiederholregel); mit `ProbeGranted == VotePending` und erfüllten Übernahmebedingungen → nur `would(ActPromote)`. `passiveDuo` → `would(ActPromote)`. `active()` bei „verloren“ → `would(ActFence)`, Rolle bleibt `aktiv`. `boot()` → Rolle bleibt wie gespeichert, bei fehlendem Partner `would(ActFence)`.
  - `func (s *stepper) would(k ActionKind)`: Ereignis Art `action` „würde jetzt: <k> (Beobachtung)“ nur, wenn `k != in.Would`; `out.Would = k`; ohne `would` in diesem Schritt `out.Would = ""`.
  - Aktionen aus `forceTakeover`, `swap`, `receiveHandover` und `handover()` tragen `Manual: true`.
  - `handover()`: `ActHandover` erst, wenn `in.DemoteDone == hp-1`; im ersten Schritt mit erledigtem `demote` `HandoverReadyAt = Now`, Übernahmefrist und Wiederholung zählen ab `HandoverReadyAt` statt `HandoverAt`. Wartet länger als `took + 90 s` auf `demote` oder `in.DemoteFailed == hp-1` → `clearHandover`, Alarm „Rollentausch abgebrochen“, `unfence()`.
  - Test-Settings in `step_test.go`/`sim_test.go`: `AutoMode = AutoReal` (bestehende Tabellen behalten ihr Teil-2-Verhalten); die Simulation setzt `DemoteDone` im Schritt nach `ActDemote`.

- [ ] **Step 1: Failing tests** in `step_test.go`:
  - `TestObserveNoRoleChange`: Trio, `observe`, Partner seit `TakeoverTrio` weg, `ProbeGranted` = Pending → `Persist.Term`/`Role` unverändert, keine `ActPromote`, ein Ereignis „würde jetzt: promote (Beobachtung)“; zweiter Schritt mit `Would = ActPromote` → kein weiteres Ereignis.
  - `TestObserveProbeVote`: ohne `ProbeGranted` → `ActProbeVote`, kein `ActRequestVote`.
  - `TestObserveActiveNoFence`: aktiv, `LastMutual` älter als `FenceAfter` → Rolle `aktiv`, Ereignis „würde jetzt: fence (Beobachtung)“.
  - `TestObserveManualReal`: `CmdForceTakeover` bei `observe` → Rolle `aktiv`, Amtszeit +1, `ActPromote` mit `Manual == true`.
  - `TestHandoverWaitsForDemote`: nach Swap (Pending = T+1) mit `DemoteDone = 0` über 10 Schritte → keine `ActHandover`; dann `DemoteDone = T` → `ActHandover` im selben Schritt, `HandoverReadyAt == Now`.
  - `TestHandoverDemoteFailed`: `DemoteFailed = T` → Rolle `aktiv`, `ActUnfence`, Ereignis „Rollentausch abgebrochen“.
- [ ] **Step 2:** `go test ./internal/ha -run 'Observe|HandoverWaits|HandoverDemote'` → FAIL.
- [ ] **Step 3:** In `step.go` umsetzen; `Settings.Real()` nur in den automatischen Pfaden prüfen.
- [ ] **Step 4:** `go test ./internal/ha` und je vorhandenem Fuzz-Ziel `go test ./internal/ha -run '^$' -fuzz=<Ziel> -fuzztime=20s` → PASS. Zusätzlicher Simulationsfall: `observe`, aktiver Knoten weg → in 10 simulierten Minuten kein Rollen-/Amtszeitwechsel.
- [ ] **Step 5:** Commit `feat(ha): Beobachtung ändert keine Rollen, Übergabe erst nach Abgabe`.

### Task 3: Zeuge „nicht aktuell“, synchron/asynchron, `DataCurrent`

**Files:**
- Modify: `internal/ha/witness.go`, `internal/ha/witness_test.go`, `internal/ha/sim_test.go`
- Create: `internal/ha/sync.go`, `internal/ha/sync_test.go`

**Interfaces:**
- Produces:
  - `Vote`: verweigert, wenn `req.From` in `p.Stale`.
  - `func MarkStale(p Persisted, node string, term uint64) Persisted` (Map kopieren, Eingabe unverändert), `func ClearStale(p Persisted, node string, term uint64) Persisted` (nur wenn `term >= p.Stale[node]`).
  - ```go
    type SyncIn struct {
        Mode          Mode
        Now           time.Duration
        Settings      Settings
        Sync          bool          // synchronous_standby_names gesetzt
        Streaming     bool          // Standby in pg_stat_replication „streaming“
        LagBytes      int64
        LastStreaming time.Duration // monoton, zuletzt streaming gesehen
        StaleAcked    bool          // Zeuge hat stale für den Partner bestätigt
        PartnerStale  bool          // eigener Vermerk „Partner nicht aktuell“
    }
    type SyncOut struct{ WantSync, SendStale, SendCurrent, MarkStale, ClearStale bool }
    func SyncStep(in SyncIn) SyncOut
    ```
    Regeln: synchron und nicht streaming seit ≥ `AsyncAfter` → Trio: ohne `StaleAcked` `SendStale` und `WantSync = true`; mit `StaleAcked` `WantSync = false`, `MarkStale`. Duo: `WantSync = false`, `MarkStale`. Asynchron, streaming, `LagBytes <= LagLimit` → `WantSync = true`. Synchron, streaming, `PartnerStale` → Trio `SendCurrent`, Duo `ClearStale`. Sonst Zustand halten (`WantSync = Sync`).
  - `func DataCurrent(standbyStreaming bool, lagBytes, lagLimit int64, active *PeerObs) bool` – wahr nur, wenn streaming, Verzögerung ≤ Grenze und `active` gesehen mit `PG == "primary"`, `Sync`, `!Stale`.

- [ ] **Step 1: Failing tests:** `TestVoteRefusesStale`, `TestClearStaleNeedsTerm`, `TestSyncStepTrioNeedsAck` (ohne Ack über 60 s → `WantSync` immer `true`, `SendStale` ab 10 s), `TestSyncStepTrioAcked`, `TestSyncStepDuo`, `TestSyncStepBackToSync`, `TestDataCurrent` (Tabelle: jede Bedingung einzeln falsch → `false`).
- [ ] **Step 2:** `go test ./internal/ha -run 'Stale|SyncStep|DataCurrent'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** `go test ./internal/ha` → PASS. Fuzz-Invariante in `sim_test.go` ergänzen: ein Knoten mit `DataCurrent == false` wird nie automatisch `aktiv`.
- [ ] **Step 5:** Commit `feat(ha): Zeuge sperrt nicht aktuellen Knoten, Regeln synchron/asynchron`.

### Task 4: PostgreSQL-Steuerung (`runner`, `pgCtl`)

**Files:**
- Create: `cmd/pdh-ha/runner.go`, `cmd/pdh-ha/pg.go`, `cmd/pdh-ha/pg_test.go`

**Interfaces:**
- Produces:
  ```go
  type runner interface {
      Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error)
  }
  type execRunner struct{} // exec.CommandContext, CombinedOutput, Fehler enthält Ausgabe
  // nur in _test.go: fakeRunner (Aufrufe merken, Antworten je argv-Präfix)

  type pgCluster struct{ Version, Name, DataDir, ConfDir string; Port int }
  func discoverCluster(ctx context.Context, r runner) (pgCluster, error) // pg_lsclusters --no-header; genau ein Cluster
  type pgCtl struct{ r runner; c pgCluster }
  func (p *pgCtl) query(ctx context.Context, sql string) (string, error) // runuser -u postgres -- psql -XAtq -p <Port> -v ON_ERROR_STOP=1, SQL über stdin
  func (p *pgCtl) ctl(ctx context.Context, op string) error               // pg_ctlcluster <Version> <Name> start|stop|restart|reload; stop mit -m fast
  type standbyStat struct{ App, State string; FlushLSN, ReplayLSN uint64; Sync bool }
  type pgStatus struct {
      State     string // primary | standby | stopped | error
      LSN       uint64 // primary: pg_current_wal_lsn; standby: pg_last_wal_replay_lsn
      Timeline  uint32
      Streaming bool   // standby: pg_stat_wal_receiver.status = 'streaming'
      Standbys  []standbyStat
      ReadOnly  bool   // default_transaction_read_only
      SyncNames string
      Err       string
  }
  func (p *pgCtl) status(ctx context.Context) pgStatus
  type pgConf struct {
      Listen                    []string
      SyncNames                 string
      ReadOnly                  bool
      PrimaryConninfo           string // leer = keiner
      SlotName                  string // Standby: primary_slot_name
      SlotKeepMB                int64
      CertFile, KeyFile, CAFile string
  }
  func renderPGConf(c pgConf) string // feste Zeilen: wal_level=replica, hot_standby=on, wal_log_hints=on, max_wal_senders=10, max_replication_slots=10, ssl=on, synchronous_commit=on, recovery_target_timeline='latest'
  func (p *pgCtl) writeConf(ctx context.Context, c pgConf) error // <ConfDir>/conf.d/pdh-ha.conf, tmp+rename, Eigentümer postgres, 0600
  func renderHBABlock(addrs []string) string
  func replaceHBABlock(hba, block string) string // vorhandenen Block ersetzen, sonst vor der ersten Nicht-Kommentarzeile einfügen
  func parseLSN(s string) (uint64, error)        // "16/B374D848"
  func slotName(id string) string                // "pdh_ha_" + id, '-' → '_'
  ```
- [ ] **Step 1: Failing tests:** `TestDiscoverCluster` (Zeile `18  main    5432 online postgres /var/lib/postgresql/18/main /var/log/postgresql/postgresql-18-main.log` → Felder, `ConfDir` `/etc/postgresql/18/main`; zwei Cluster → Fehler „Mehrere PostgreSQL-Cluster – nicht unterstützt“), `TestRenderPGConf` (alle festen Zeilen; `primary_conninfo` in `'…'` mit verdoppelten `'`), `TestReplaceHBABlockIdempotent` (zweimal = gleich; Block vor `local all postgres peer`), `TestParseLSN`, `TestStatusPrimary`, `TestStatusStandby`, `TestQueryUsesStdin` (SQL nicht in argv).
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'Discover|PGConf|HBA|LSN|TestStatus|QueryUses'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** Tests → PASS; `go vet ./cmd/pdh-ha`.
- [ ] **Step 5:** Commit `feat(pdh-ha): PostgreSQL-Steuerung`.

### Task 5: Dienst `pdh` und Aktionen im Hintergrund

**Files:**
- Create: `cmd/pdh-ha/svc.go`, `cmd/pdh-ha/svc_test.go`
- Modify: `cmd/pdh-ha/daemon.go`, `cmd/pdh-ha/observe.go` (`inputLocked`), `cmd/pdh-ha/daemon_test.go`

**Interfaces:**
- Consumes: Task 2 (`Input.DemoteDone/DemoteFailed`), Task 4 (`runner`).
- Produces:
  ```go
  type serviceCtl interface {
      Start(ctx context.Context) error
      Stop(ctx context.Context) error
      Active(ctx context.Context) bool
      SetAutostart(ctx context.Context, on bool) error
  }
  func newServiceCtl(r runner) serviceCtl // PDH_HA_SERVICE_CMD gesetzt → Skript mit start|stop|is-active|enable|disable, sonst systemctl … pdh
  ```
  Daemon: Aktionen außer `request_vote`, `probe_vote`, `handover` (bleiben Nachrichten) gehen in die Warteschlange `d.actQ chan ha.Action` (Puffer 16) und werden von einer Goroutine nacheinander über `d.act.Do` ausgeführt; der Takt läuft weiter. `d.busy atomic.Bool` während einer Aktion. Nach erfolgreichem `ActDemote` setzt der Worker `d.demoteDone = a.Term`, bei Fehler `d.demoteFailed = a.Term`; `inputLocked` reicht beide und `d.probeGranted` an `ha.Input` weiter. Fehler sonst wie bisher `actionFailed`. `request_vote`/`probe_vote` → Nachricht `vote_req`/`vote_probe` an den Zeugen.
- [ ] **Step 1: Failing tests:** `TestCmdServiceArgs`, `TestSystemdServiceArgs`, `TestDaemonActionsAsync` (Aktor blockiert 2 s → Heartbeats laufen weiter, `busy` gesetzt), `TestDaemonHandoverAfterDemote` (Fake-Aktor, `demote` dauert 3 Takte → vorher keine `handover`-Nachricht, danach welche).
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'Service|ActionsAsync|HandoverAfterDemote'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** `go test ./cmd/pdh-ha` → PASS; `-race` auf Linux (unter Windows ohne cgo nicht möglich, im Bericht vermerken).
- [ ] **Step 5:** Commit `feat(pdh-ha): Aktionen im Hintergrund, Dienst pdh steuerbar`.

### Task 6: `pgActor` – hochstufen, abgeben, abschalten; Abgleich

**Files:**
- Create: `cmd/pdh-ha/pgactor.go`, `cmd/pdh-ha/pgactor_test.go`
- Modify: `cmd/pdh-ha/daemon.go` (Aktor wählen, Abgleich-Takt)

**Interfaces:**
- Consumes: Task 4, Task 5.
- Produces:
  ```go
  type pgEnv struct {
      SelfID, PartnerID         string
      PartnerAddrs              []string // Hosts ohne Port
      ReplicaPass               string
      CAFile, CertFile, KeyFile string   // PostgreSQL-Kopien (postgres, 0600)
      SlotKeepMB                int64
      Listen                    []string
      DataCurrent               bool     // für promote ohne Manual
  }
  type pgActor struct {
      pg      *pgCtl
      svc     serviceCtl
      env     func() pgEnv
      pushCfg func(ctx context.Context) error // Task 9: Konfiguration an den Partner
      wait    time.Duration                   // Frist Gleichstand, 60 s
  }
  func (a *pgActor) Do(ctx context.Context, act ha.Action) error
  func (d *daemon) reconcile(ctx context.Context) // beim Start und alle 30 s, nicht wenn d.busy
  func standbyConninfo(e pgEnv) string           // host=a,b port=5432,5432 user=pdh_replica application_name=pdh_ha_<self> sslmode=verify-ca sslrootcert=<CAFile> passfile=/var/lib/postgresql/.pdh-ha-pgpass
  ```
  Aktor-Auswahl: `pgActor`, wenn `/etc/pdh/ha/replica.pass` existiert und `discoverCluster` gelingt; sonst `observeActor`. `vip_*` → nur Ereignis wie bisher. Abläufe laut Spec §3.2. `promote` lehnt mit „Datenbank ist kein Standby“ ab, wenn der Status nicht `standby` ist; ohne `Manual` zusätzlich mit „Datenstand nicht aktuell“, wenn `!DataCurrent`.
- [ ] **Step 1: Failing tests** (`fakeRunner`, Prüfung der Befehlsreihenfolge):
  - `TestDemoteOrder`: pushCfg → svc.Stop → Gleichstand abfragen → `pg_ctlcluster … stop -m fast` → `standby.signal` + Konfig → `pg_ctlcluster … start`.
  - `TestDemoteTimeoutRestartsPDH`: Partner-`replay_lsn` bleibt zurück → Fehler nach `wait`, kein `stop`, `svc.Start` aufgerufen.
  - `TestPromoteOrder`: `SELECT pg_promote(true, 60)` → `pg_is_in_recovery()` = `f` → Slot anlegen (nur falls fehlend) → Konfig mit `SyncNames` → reload → svc.Start.
  - `TestPromoteRefusesPrimary`, `TestPromoteRefusesStaleUnlessManual`, `TestFenceOrder` (svc.Stop → Konfig `ReadOnly` → reload → `pg_terminate_backend` aller Sitzungen außer `walsender` und eigener), `TestUnfence`.
  - `TestReconcilePassiveStopsPDH`, `TestReconcileWritableNotActiveFences` (Alarm „Nicht aktiver Server mit schreibbarer Datenbank – abgeschaltet“), `TestReconcileSkipsWhenBusy`, `TestReconcileNeverPromotes`, `TestReconcileStandbyFollowsActive` (`primary_conninfo` zeigt auf den aktiven Knoten).
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'Demote|Promote|Fence|Unfence|Reconcile'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): echte Datenbank-Aktionen und Abgleich`.

### Task 7: Wiedereingliederung und abgezweigte Daten

**Files:**
- Create: `cmd/pdh-ha/rejoin.go`, `cmd/pdh-ha/rejoin_test.go`
- Modify: `cmd/pdh-ha/pgactor.go` (`ActRejoin` → `rejoin`), `cmd/pdh-ha/api.go` (`POST /v1/diverged/delete`), `cmd/pdh-ha/daemon.go` (`recoverDataDir` beim Start)

**Interfaces:**
- Consumes: Task 4, Task 6.
- Produces:
  ```go
  type tableChange struct{ Table string; Insert, Update, Delete int }
  type divergedInfo struct {
      ID     string        `json:"id"` // Ordnername
      At     time.Time     `json:"at"`
      Term   uint64        `json:"term"`
      Bytes  int64         `json:"bytes"`
      Tables []tableChange `json:"tables"`
  }
  func (a *pgActor) rejoin(ctx context.Context, term uint64) error
  func parseRewindDryRun(out []byte) (forkLSN uint64, diverged bool, err error) // „servers diverged at WAL location X on timeline N“; „no rewind required“ → diverged = false
  func parseWaldump(out []byte, rel map[string]string) []tableChange           // „desc: INSERT|UPDATE|HOT_UPDATE|DELETE … rel A/B/C“, C = relfilenode → Tabellenname
  func listDiverged(dir string) ([]divergedInfo, error)
  func deleteDiverged(dir, id string) error                     // id nur [0-9a-z-]
  func (a *pgActor) recoverDataDir(ctx context.Context) error // <DataDir>.alt-* vorhanden und DataDir ohne PG_VERSION → zurückbenennen, Ereignis
  ```
  Ablauf und Pfade laut Spec §3.4. Abgeschotteter Start der alten Datenbank: `pg_ctlcluster <V> <N> start -o "-p <Port> -c listen_addresses=localhost -c archive_mode=off -c default_transaction_read_only=on"` mit erstem freien Port ab 55432; `pg_dumpall -p <Port>`; `pg_waldump` ab Trennpunkt; Relationen vor dem Stopp über `pg_class.relfilenode` je Datenbank. Platzprüfung: frei ≥ 1,2 × Summe `pg_database_size`, sonst Fehler „Zu wenig Platz für abgezweigte Daten“. Abgezweigte Daten → Alarm „Abgezweigte Daten gesichert (<ID>) – bitte prüfen“.
- [ ] **Step 1: Failing tests:** `TestParseRewindDryRun`, `TestParseWaldump`, `TestRejoinNoDivergence` (kein Abzug, `pg_rewind`, Standby-Konfig, Start), `TestRejoinDiverged` (Reihenfolge abgeschotteter Start → Relationen → `pg_dumpall` → `pg_waldump` → stop → `pg_rewind`; `summary.json` geschrieben), `TestRejoinDumpFailsKeepsData` (kein `pg_rewind`, kein Umbenennen, Fehler), `TestRejoinRewindFailsBasebackup` (umbenennen → `pg_basebackup -R -X stream -S <slot>` → alter Ordner gelöscht), `TestRejoinBasebackupFailsRestores` (zurückbenannt, Fehler), `TestRecoverDataDirAfterCrash`, `TestDeleteDivergedRejectsPath` (`../x` → Fehler), `TestAPIDivergedDelete`.
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'Rewind|Waldump|Rejoin|RecoverDataDir|Diverged'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): Wiedereingliederung mit pg_rewind und Sicherung abgezweigter Daten`.

### Task 8: Replikation im Takt, `stale`/`current`, echter Heartbeat

**Files:**
- Create: `cmd/pdh-ha/repl.go`, `cmd/pdh-ha/repl_test.go`
- Modify: `cmd/pdh-ha/transport.go` (`message`, `deliverTypes`), `cmd/pdh-ha/messages.go`, `cmd/pdh-ha/observe.go` (`obsFrom`, `inputLocked`, Heartbeat), `cmd/pdh-ha/daemon.go`, `cmd/pdh-ha/api.go` (Status)

**Interfaces:**
- Consumes: Task 3, Task 4, Task 7 (`listDiverged`).
- Produces:
  - `message` neu: `LSN uint64` (`lsn`), `Lag int64` (`lag`), `Sync bool` (`sync`), `TL uint32` (`tl`), `Stale bool` (`stale`), `Node string` (`node`), `Probe bool` (`probe`), `Diverged []divergedInfo` (`diverged`) – alle `omitempty`. `deliverTypes` neu: `stale`, `stale_ack`, `current`, `current_ack`, `vote_probe`. `obsFrom` füllt die neuen `PeerObs`-Felder.
  - `PG` im Heartbeat aus `pgStatus.State` (ohne Spiegel weiter `unknown`).
  - `func (d *daemon) replTick(ctx context.Context)` – nur mit `pgActor`; `pgStatus` im Hintergrund holen (Muster `pingRound`, höchstens eine Abfrage gleichzeitig). Aktiver Knoten: `SyncStep` → `SyncNames` schreiben und neu laden; `stale`/`current` an den Zeugen jeden Takt bis Ack; `MarkStale`/`ClearStale` im eigenen `state.json` vor dem Senden speichern. Passiver Knoten: `in.DataCurrent = ha.DataCurrent(…)`, `DataAssumed = false`.
  - Zeuge: `stale` → `MarkStale` speichern, dann `stale_ack`; `current` → `ClearStale` speichern, dann `current_ack`; `vote_probe` → `ha.Vote` rechnen, **nicht** speichern, `vote_resp` mit `Probe: true`. Knoten: `vote_resp` mit `Probe` → `d.probeGranted`.
  - Status-API `self`/`peers` neu: `pg`, `lsn`, `lag_bytes`, `sync`, `stale`, `timeline`, `data_current`, `diverged`; oben `auto_mode`, `swap_tested`, `busy`.
- [ ] **Step 1: Failing tests:** `TestReplStaysSyncWithoutAck` (Standby 30 s weg, Zeuge stumm → `SyncNames` bleibt, `stale` wiederholt), `TestReplAsyncAfterAck` (Konfig ohne `SyncNames`, `state.json` mit `Stale`), `TestReplDuoAsyncWithoutWitness`, `TestReplBackToSyncSendsCurrent`, `TestWitnessStalePersistsBeforeAck` (Speicherfehler → kein Ack), `TestWitnessProbeNotPersisted`, `TestHeartbeatPGFields`, `TestPassiveDataCurrentFromActive`.
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'Repl|WitnessStale|WitnessProbe|HeartbeatPG|PassiveDataCurrent'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** `go test ./cmd/pdh-ha ./internal/ha` → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): synchrone Replikation, Zeugen-Bestätigung für asynchron, echter Datenstand`.

### Task 9: Aktive Seite einrichten, Konfiguration übergeben, `replica-init`

**Files:**
- Create: `cmd/pdh-ha/primary.go`, `cmd/pdh-ha/primary_test.go`, `cmd/pdh-ha/replica.go`, `cmd/pdh-ha/replica_test.go`
- Modify: `cmd/pdh-ha/transport.go` (HTTPS-Route), `cmd/pdh-ha/main.go` (Befehle), `cmd/pdh-ha/store.go` (`clusterConfig.SwapTested bool` JSON `swap_tested`, `Swaps int` JSON `swaps`), `cmd/pdh-ha/api.go` (Settings: `real` nur mit `SwapTested`), `cmd/pdh-ha/messages.go` (`removedLocked` → Autostart `pdh` wieder an), `cmd/pdh-ha/system.go` (ufw 5432)

**Interfaces:**
- Consumes: Task 4, Task 6.
- Produces:
  ```go
  func (a *pgActor) ensurePrimary(ctx context.Context, standbyID string, standbyAddrs []string) error
  // Rolle pdh_replica anlegen bzw. Passwort setzen (SQL über stdin), GRANT EXECUTE für die vier pg_rewind-Funktionen (Spec §2.1),
  // pg_hba-Block, Konfig (Listen, Zertifikate, wal_log_hints …); bei pending_restart einmal Neustart mit Ereignis
  // „PostgreSQL neu gestartet (Einstellungen für den Spiegel)“; Slot anlegen.
  type pullResp struct {
      PGMajor     string `json:"pg_major"`
      DBBytes     int64  `json:"db_bytes"`
      ReplicaPass string `json:"replica_pass,omitempty"`
      Archive     []byte `json:"archive,omitempty"` // tar.gz: .env aus PDH_REPO und /etc/pdh ohne ha/
  }
  // POST /v1/config/pull {"dry":bool} über Port 7480 (ALPN http/1.1), Client-Zertifikat Pflicht, OU=node, in der Knotenliste,
  // nicht gesperrt; nur wenn dieser Knoten aktiv ist (sonst 409 „Nicht der aktive Server“). dry: nur PGMajor/DBBytes, kein ensurePrimary.
  func (d *daemon) pullConfig(ctx context.Context, dry bool) (pullResp, error) // Client-Seite, mTLS mit eigenem Knotenzertifikat
  func extractConfig(archive []byte, repo, etc string) error                   // nur .env und Dateien unter etc; absolute Pfade oder .. → Fehler
  type replicaOpts struct{ ConfirmHost, Repo string }
  func replicaInit(ctx context.Context, d *daemon, o replicaOpts) error
  ```
  Beide Seiten (`ensurePrimary` und `replicaInit`) legen lokal die PostgreSQL-Kopien von `node.crt`/`node.key`/`ca.crt` an (`<ConfDir>/pdh-ha/`, postgres, 0600) und schreiben den `pg_hba`-Block für die Adressen des Partners – damit nach einem Tausch die Gegenrichtung sofort funktioniert. Der Abgleich (Task 6) erneuert die Kopien, wenn `node.crt` neuer ist.
  Neue Befehle in `main.go`: `replica-init [--confirm-host NAME]`, `peer-pg-major` (druckt die Hauptversion des aktiven Knotens), `should-run-pdh` (Exit 0, wenn nicht eingerichtet, Einzel oder Rolle `aktiv`; sonst Exit 1). `replicaInit` nach Spec §4.2 Schritte 1, 2, 4, 5, 6. Meldungen: „Dieser Server ist aktiv – replica-init nur auf dem passiven Server“, „Andere PostgreSQL-Hauptversion (hier X, aktiv Y) – bitte postgresql-Y installieren“, „Vorhandene Datenbanken: … – zum Überschreiben --confirm-host <Hostname> angeben“. Danach `replica.pass` schreiben, Autostart `pdh` aus, `pdh-ha` neu starten lassen (Ausgabe-Hinweis), damit `pgActor` aktiv wird; auf dem aktiven Knoten schaltet `ensurePrimary` den Autostart ebenfalls aus.
  `Swaps`: Der CA-Halter erhöht nach jedem erfolgreichen Rollentausch (neue Amtszeit über `handover` gesehen) und verteilt die Knotenliste; ab 2 → `SwapTested = true`.
  ufw: `allow from <Partneradresse> to any port 5432 proto tcp comment pdh-ha-pg`, nur mit `pgActor`.
- [ ] **Step 1: Failing tests:** `TestEnsurePrimaryNoPasswordInArgv`, `TestEnsurePrimaryIdempotent`, `TestConfigPullRequiresNodeCert` (Zeugen-Zertifikat → 403, fremdes → TLS-Fehler, passiver Server → 409), `TestConfigPullDry`, `TestExtractConfigRejectsTraversal`, `TestReplicaInitRefusesActive`, `TestReplicaInitRefusesMajorMismatch`, `TestReplicaInitNeedsConfirm` (fremde DB, falscher Hostname → Fehler, kein `stop`), `TestReplicaInitOrder` (Vorher-Sicherung → stop → umbenennen → `pg_basebackup` → Konfig → start → streaming → Autostart aus), `TestSettingsRealNeedsSwapTested`, `TestSwapsCountToTested`, `TestShouldRunPDH`, `TestUfwRulesPG`.
- [ ] **Step 2:** `go test ./cmd/pdh-ha -run 'EnsurePrimary|ConfigPull|ExtractConfig|ReplicaInit|RealNeeds|SwapsCount|ShouldRun|UfwRulesPG'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** `go test ./cmd/pdh-ha` → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): Datenbank-Spiegel einrichten, Konfiguration übergeben, Pflicht-Tausch`.

### Task 10: Skripte – `--voll`, `pdh` nicht auf passivem Knoten starten

**Files:**
- Create: `scripts/install-pdh-program.sh`
- Modify: `scripts/pdh-ha-join.sh`, `scripts/pdh-vm-prepare.sh` (Programmteil aus `--neu` nach `install-pdh-program.sh` auslagern und aufrufen), `install-update-agent.sh`, `install_update.sh`
- Test: Skript-String-Tests im Stil der vorhandenen (`cmd/pdh-ha/system_test.go` bzw. neue `TestJoinScriptVoll`)

**Interfaces:**
- Consumes: Task 9 (`peer-pg-major`, `replica-init`, `should-run-pdh`).
- Produces:
  - `install-pdh-program.sh <Repo> <Commit>`: Benutzer `pdh`, Checkout auf Commit, Unit `pdh.service` (wie heute in `pdh-vm-prepare.sh`), Bau über `install-update-agent.sh`; ohne eigene Datenbank und ohne `.env`-Erzeugung.
  - `pdh-ha-join.sh --voll [<Code>]` und `--update <Commit> --voll`: nach Beitritt bzw. Update → `pdh-ha peer-pg-major` → `postgresql-<Major>` installieren (PGDG-Muster aus `pdh-restore.sh` Z. 125–134) → `install-pdh-program.sh /opt/pdh-server <Commit>` → fremde Datenbanken anzeigen, Hostnamen abfragen (`read -r`), `pdh-ha replica-init --confirm-host "<Eingabe>"` → `systemctl restart pdh-ha` → Ausgabe „Datenbank-Spiegel läuft – im PDH Rollen tauschen (hin und zurück) testen“.
  - `install-update-agent.sh` und `install_update.sh`: jedes `systemctl restart pdh`/`enable pdh` nur, wenn `! command -v pdh-ha >/dev/null || pdh-ha should-run-pdh`.
- [ ] **Step 1: Failing tests:** `--voll` in Hilfe und Optionen; `replica-init --confirm-host` aufgerufen; Neustart von `pdh` in beiden Update-Skripten an `should-run-pdh` gebunden; `install-pdh-program.sh` ohne unbedingtes `systemctl restart pdh`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Skripte ändern.
- [ ] **Step 4:** Tests → PASS; `bash -n` für jedes geänderte Skript ohne Ausgabe.
- [ ] **Step 5:** Commit `feat: pdh-ha-join.sh --voll, PDH startet nur auf dem aktiven Knoten`.

### Task 11: Update-Agent – Sicherung nur auf dem aktiven Knoten

**Files:**
- Modify: `cmd/update-agent/ha.go`, `cmd/update-agent/dr.go`, `cmd/update-agent/dr_files.go`, `cmd/update-agent/ha_test.go`, `cmd/update-agent/dr_test.go`, `docs/superpowers/specs/2026-10-25-pdh-ha-kern-design.md` (§4.4)

**Interfaces:**
- Consumes: Task 8 (Status `self.pg`, `self.timeline`).
- Produces: `haGateEnforced = true`. `drIsActive` liefert nur `true`, wenn Rolle `aktiv` **und** `self.pg` ∈ {`primary`, `unknown`} (`unknown` = Knoten ohne Spiegel). Das Verschieben des Zwischenspeichers läuft vor der Sperre (immer). Pause > 15 min → Alarm „Netzlaufwerk-Sicherung seit über 15 Minuten pausiert – pdh-ha prüfen“ (einmal je Pause). DR-Status neu `LastTimeline uint32`, `LastNode string`; ändert sich `self.timeline` auf dem aktiven Knoten → Grundsicherung sofort.
- [ ] **Step 1: Failing tests:** `TestDRGateEnforced` (passiv → keine Grundsicherung, `Paused` gesetzt), `TestDRGateStandbyPG` (aktiv, `pg=standby` → keine Sicherung), `TestDRSpoolAlwaysMoved`, `TestDRBaseBackupOnTimelineChange`, `TestDRPauseAlarmAfter15Min`. Bestehende Tests zur Beobachtungs-Sperre auf das neue Verhalten umstellen.
- [ ] **Step 2:** `go test ./cmd/update-agent -run 'DRGate|DRSpool|DRBaseBackupOnTimeline|DRPause'` → FAIL.
- [ ] **Step 3:** Umsetzen; Teil-2-Spec §4.4 um „ab Teil 3 scharf (R20), Zwischenspeicher immer“ ergänzen.
- [ ] **Step 4:** `go test ./cmd/update-agent` → PASS.
- [ ] **Step 5:** Commit `feat(update-agent): Netzlaufwerk-Sicherung nur auf dem aktiven Knoten`.

### Task 12: PDH-Reiter, Handbuch, Version

**Files:**
- Modify: `internal/web/ha.go`, `internal/web/ha_test.go`, `web/templates/widgets/ha.gohtml`, `web/templates/help.gohtml`, `web/templates/widgets/help_figures.gohtml`, `VERSION`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 7 (`POST /v1/diverged/delete`), Task 8 (Statusfelder), Task 9 (`swap_tested`, Settings).
- Produces:
  - `haNode` neu: `PG`, `Sync`, `Stale`, `DataCurrent`, `LSN`, `LagBytes`, `Timeline`, `Diverged []haDiverged`; Anzeige je Knoten: Datenbank (schreibend/Standby/gestoppt/Fehler/–), Verzögerung in MB mit einer Nachkommastelle, synchron/asynchron, „nicht aktuell“.
  - Einstellungen: `auto_mode` (Beobachtung/echt; „echt“ deaktiviert mit „Erst einmal Rollen tauschen – hin und zurück“, solange `!SwapTested`), „asynchron nach (s)“, „Verzögerungsgrenze (MB)“, „Höchstmenge zurückgehaltener Änderungen (GB)“.
  - Hinweis bei `real`: „Zugangsweg wird erst mit Teil 5 umgeschaltet – nach einer Übernahme ist das PDH nur unter der Adresse des neuen aktiven Knotens erreichbar“.
  - Schritt „Pflicht-Testumschaltung“ (offen/erledigt); Liste „Abgezweigte Daten“ (Zeit, Amtszeit, Größe, Tabellen) mit „Geprüft – löschen“ (Bestätigung, `POST /admin/server-config/ha/diverged-delete`, nur `canServerConfig`); Bestätigung bei „Rollen tauschen“: „Dateien werden erst mit Teil 6 gespiegelt – auf dem anderen Knoten fehlen neue Anhänge“.
  - Handbuch-Kapitel Ausfallschutz laut Spec §8 als „So geht's“-Schritte; Skizze der Übersicht um Datenbank/Verzögerung ergänzen. `VERSION` 0.76.0; `CHANGELOG.md` `## [0.76.0] – <Datum>`, `### Neu` (Datenbank-Spiegel, Rollen tauschen und Übernahme erzwingen echt, Wiedereingliederung, Abgezweigte Daten, Beobachtung/echt), `### Geändert` (Sicherung nur auf dem aktiven Server; Beobachtung ändert keine Rollen mehr).
- [ ] **Step 1: Failing tests:** `TestHANodeDBColumns` (Fake-Socket mit `pg: standby`, `lag_bytes: 1572864` → „Standby“, „1.5 MB“), `TestHASettingsRealDisabledUntilSwap`, `TestHADivergedDeleteNeedsPermission`, `TestHADivergedDeleteCallsSocket`, `TestHASwapConfirmText`.
- [ ] **Step 2:** `go test ./internal/web -run TestHA` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** `go test ./...` (inkl. `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`) → PASS.
- [ ] **Step 5:** Commit `feat: Ausfallschutz-Reiter mit Datenbank-Spiegel, Handbuch, Version 0.76.0`.

### Task 13: Testlabor mit PostgreSQL

**Files:**
- Modify: `deploy/ha-witness/Dockerfile` (neues Ziel `lab-pg`), `deploy/ha-lab/compose.yaml`, `deploy/ha-lab/ha-lab.sh`, `deploy/ha-lab/README.md`
- Create: `deploy/ha-lab/pdh-dummy.sh`, `deploy/ha-lab/writer.sh`

**Interfaces:**
- Consumes: alle vorigen Tasks.
- Produces:
  - Ziel `lab-pg`: `FROM ubuntu:26.04`, Pakete `postgresql postgresql-contrib iputils-ping curl`, `pdh-ha` aus dem Build-Ziel, Einstieg startet PostgreSQL (`pg_ctlcluster`) und dann `pdh-ha run`; `PDH_HA_SERVICE_CMD=/usr/local/bin/pdh-dummy`.
  - `node1` legt beim ersten Start die Datenbank `lab` mit Tabelle `t(n bigint primary key)` an; `node2` führt nach dem Beitritt `pdh-ha replica-init --confirm-host node2` aus.
  - `writer.sh`: schreibt fortlaufend `INSERT INTO t VALUES (n)` an die gerade schreibbare Datenbank, merkt bestätigte `n` in `/tmp/acked`.
  - `ha-lab.sh`: Wächter `watch_pg` (höchstens ein Knoten mit `pg_is_in_recovery() = f` und ohne `default_transaction_read_only`, sonst sofort „nicht bestanden“), `check_acked` nach jedem Szenario. Szenarien neu `sc_tausch`, `sc_erzwingen_abgezweigt`, `sc_rewind_fehler`, `sc_asynchron_zeuge`, `sc_asynchron_ohne_zeuge`, `sc_slot_grenze` (`slot_keep_gb=1`, Standby gestoppt, 1,2 GB schreiben → Neukopie), `sc_aktiv_weg_echt` (`auto_mode=real` nach `tausch`); bestehende Szenarien laufen mit `observe` und prüfen, dass sich Rollen und Amtszeit nicht ändern. `alle` führt alle aus.
- [ ] **Step 1:** Ziel, Compose, Hilfsskripte und Szenarien schreiben (Muster der vorhandenen `sc_*`).
- [ ] **Step 2:** Auf Linux (pdh-server): `bash deploy/ha-lab/ha-lab.sh alle` → jedes Szenario „bestanden“; Ausgabe in den Bericht.
- [ ] **Step 3:** `bash deploy/ha-lab/ha-lab.sh race` → bestanden.
- [ ] **Step 4:** README um die neuen Szenarien ergänzen.
- [ ] **Step 5:** Commit `test: Testlabor mit PostgreSQL und Datenverlust-Wächter`.

### Task 14: Abnahme im echten Netz (mit dem Nutzer)

Keine Code-Änderung; Ergebnisse im Ledger und im Gedächtnis festhalten.

- [ ] **Step 1:** Nach Freigabe pushen, `pdh` (10.10.0.14) über den Agenten aktualisieren, Zeuge neu bauen.
- [ ] **Step 2:** Auf `pdh-server` (10.10.0.17): `sudo bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update <Commit> --voll`, Hostnamen bestätigen; Reiter zeigt Knoten 2 als Standby, synchron.
- [ ] **Step 3:** Rollen tauschen hin und zurück; PDH jeweils unter der Adresse des aktiven Knotens prüfen (Ticket anlegen, nach dem Tausch vorhanden); „Pflicht-Testumschaltung“ erledigt.
- [ ] **Step 4:** `pdh-server` stoppen, auf `pdh` speichern (asynchron nach Zeugen-Bestätigung), `pdh` stoppen, auf `pdh-server` „Übernahme erzwingen“, `pdh` starten → Wiedereingliederung, „Abgezweigte Daten“ mit den Einträgen aus der Zwischenzeit.
- [ ] **Step 5:** Optional `auto_mode = real`, VM `pdh` anhalten → Übernahme nach 30 s, Rückkehr als Standby.
