package main

// Steuerung des PostgreSQL-Clusters aus dem Ubuntu-Paket (pg_lsclusters,
// pg_ctlcluster, psql als Benutzer postgres). Einstellungen stehen nur in
// conf.d/pdh-ha.conf, Zugänge im markierten Block der pg_hba.conf.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	pgPort      = 5432
	replicaUser = "pdh_replica"
	hbaBegin    = "# pdh-ha begin – von pdh-ha verwaltet, nicht von Hand ändern"
	hbaEnd      = "# pdh-ha end"
)

type pgCluster struct {
	Version, Name, DataDir, ConfDir string
	Port                            int
}

// discoverCluster liest pg_lsclusters; genau ein Cluster wird unterstützt.
func discoverCluster(ctx context.Context, r runner) (pgCluster, error) {
	out, err := r.Run(ctx, []string{"pg_lsclusters", "--no-header"}, nil, nil)
	if err != nil {
		return pgCluster{}, err
	}
	var found []pgCluster
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		port, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		found = append(found, pgCluster{Version: f[0], Name: f[1], Port: port, DataDir: f[5],
			ConfDir: path.Join("/etc/postgresql", f[0], f[1])})
	}
	switch len(found) {
	case 0:
		return pgCluster{}, errors.New("Kein PostgreSQL-Cluster gefunden")
	case 1:
		return found[0], nil
	}
	return pgCluster{}, errors.New("Mehrere PostgreSQL-Cluster – nicht unterstützt")
}

type pgCtl struct {
	r runner
	c pgCluster
}

// query führt SQL als postgres aus; das SQL geht über stdin, nie in argv.
func (p *pgCtl) query(ctx context.Context, sql string) (string, error) {
	out, err := p.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", "psql", "-XAtq",
		"-p", strconv.Itoa(p.c.Port), "-v", "ON_ERROR_STOP=1"}, nil, []byte(sql))
	return strings.TrimSpace(string(out)), err
}

// ctl startet, stoppt (schnell), lädt oder startet den Cluster neu.
func (p *pgCtl) ctl(ctx context.Context, op string) error {
	argv := []string{"pg_ctlcluster", p.c.Version, p.c.Name, op}
	if op == "stop" {
		argv = append(argv, "-m", "fast")
	}
	_, err := p.r.Run(ctx, argv, nil, nil)
	return err
}

type standbyStat struct {
	App, State          string
	FlushLSN, ReplayLSN uint64
	Sync                bool
}

type pgStatus struct {
	State     string // primary | standby | stopped | error
	LSN       uint64 // primary: pg_current_wal_lsn; standby: pg_last_wal_replay_lsn
	Timeline  uint32
	Streaming bool // standby: pg_stat_wal_receiver.status = 'streaming'
	Standbys  []standbyStat
	ReadOnly  bool
	SyncNames string
	LostSlots []string // primary: ungültige Slots (wal_status lost)
	Err       string
}

const pgStatusSQL = `SELECT pg_is_in_recovery(),
 COALESCE(CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn() ELSE pg_current_wal_lsn() END::text, ''),
 (SELECT timeline_id FROM pg_control_checkpoint()),
 current_setting('default_transaction_read_only'),
 current_setting('synchronous_standby_names'),
 COALESCE((SELECT status FROM pg_stat_wal_receiver), '')`

const pgStandbysSQL = `SELECT application_name, state, COALESCE(flush_lsn::text, ''), COALESCE(replay_lsn::text, ''), sync_state
 FROM pg_stat_replication`

// status fragt den Zustand ab; läuft PostgreSQL nicht, ist State "stopped".
func (p *pgCtl) status(ctx context.Context) pgStatus {
	out, err := p.query(ctx, pgStatusSQL)
	if err != nil {
		if p.stopped(ctx) {
			return pgStatus{State: "stopped"}
		}
		return pgStatus{State: "error", Err: err.Error()}
	}
	f := strings.Split(out, "|")
	if len(f) != 6 {
		return pgStatus{State: "error", Err: "unerwartete Antwort: " + out}
	}
	st := pgStatus{State: "primary", ReadOnly: f[3] == "on", SyncNames: f[4]}
	if f[0] == "t" {
		st.State, st.Streaming = "standby", f[5] == "streaming"
	}
	if f[1] != "" {
		st.LSN, _ = parseLSN(f[1])
	}
	if tl, err := strconv.ParseUint(f[2], 10, 32); err == nil {
		st.Timeline = uint32(tl)
	}
	if st.State != "primary" {
		return st
	}
	rows, err := p.query(ctx, pgStandbysSQL)
	if err != nil {
		return pgStatus{State: "error", Err: err.Error()}
	}
	for _, row := range strings.Split(rows, "\n") {
		c := strings.Split(row, "|")
		if len(c) != 5 {
			continue
		}
		s := standbyStat{App: c[0], State: c[1], Sync: c[4] == "sync"}
		s.FlushLSN, _ = parseLSN(c[2])
		s.ReplayLSN, _ = parseLSN(c[3])
		st.Standbys = append(st.Standbys, s)
	}
	if lost, err := p.query(ctx, pgLostSlotsSQL); err == nil {
		for _, s := range strings.Split(lost, "\n") {
			if s = strings.TrimSpace(s); s != "" {
				st.LostSlots = append(st.LostSlots, s)
			}
		}
	}
	return st
}

// pgLostSlotsSQL: Slots, für die zu viel zurückgehalten wurde – der Standby
// dahinter braucht eine Neukopie.
const pgLostSlotsSQL = `SELECT slot_name FROM pg_replication_slots WHERE wal_status = 'lost'`

// stopped: pg_lsclusters meldet den Cluster nicht als online.
func (p *pgCtl) stopped(ctx context.Context) bool {
	out, err := p.r.Run(ctx, []string{"pg_lsclusters", "--no-header", p.c.Version, p.c.Name}, nil, nil)
	if err != nil {
		return false
	}
	f := strings.Fields(string(out))
	return len(f) >= 4 && f[3] != "online"
}

type pgConf struct {
	Listen                    []string
	SyncNames                 string
	ReadOnly                  bool
	PrimaryConninfo           string // leer = keiner
	SlotName                  string // Standby: primary_slot_name
	SlotKeepMB                int64
	CertFile, KeyFile, CAFile string
}

// pgQuote setzt einen Wert in einfache Anführungszeichen (' verdoppelt).
func pgQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func renderPGConf(c pgConf) string {
	var b strings.Builder
	w := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, v) }
	b.WriteString("# PDH Ausfallschutz (pdh-ha) – von pdh-ha verwaltet, nicht von Hand ändern\n")
	w("wal_level", "replica")
	w("hot_standby", "on")
	w("wal_log_hints", "on")
	w("max_wal_senders", "10")
	w("max_replication_slots", "10")
	w("synchronous_commit", "on")
	w("recovery_target_timeline", pgQuote("latest"))
	// Eine hart getrennte Leitung bemerkt PostgreSQL sonst erst nach 60 s –
	// so lange hingen Speicherungen, bevor „asynchron nach“ überhaupt zählt.
	w("wal_sender_timeout", pgQuote("10s"))
	w("wal_receiver_timeout", pgQuote("10s"))
	w("wal_receiver_status_interval", pgQuote("1s"))
	// pg_rewind braucht auf dem abgelösten Server das WAL ab dem letzten
	// gemeinsamen Prüfpunkt; ohne Vorrat ist es nach einem Neustart weg und
	// es bleibt nur die vollständige Neukopie.
	w("wal_keep_size", pgQuote("1GB"))
	w("ssl", "on")
	if c.CertFile != "" {
		w("ssl_cert_file", pgQuote(c.CertFile))
		w("ssl_key_file", pgQuote(c.KeyFile))
	}
	if c.CAFile != "" {
		w("ssl_ca_file", pgQuote(c.CAFile))
	}
	if len(c.Listen) > 0 {
		w("listen_addresses", pgQuote(strings.Join(append([]string{"localhost"}, c.Listen...), ",")))
	}
	w("synchronous_standby_names", pgQuote(c.SyncNames))
	if c.ReadOnly {
		w("default_transaction_read_only", "on")
	}
	if c.PrimaryConninfo != "" {
		w("primary_conninfo", pgQuote(c.PrimaryConninfo))
	}
	if c.SlotName != "" {
		w("primary_slot_name", pgQuote(c.SlotName))
	}
	if c.SlotKeepMB > 0 {
		w("max_slot_wal_keep_size", pgQuote(strconv.FormatInt(c.SlotKeepMB, 10)+"MB"))
	}
	return b.String()
}

// writeConf schreibt conf.d/pdh-ha.conf atomar (tmp + rename), Eigentümer
// postgres über den runner, Rechte 0600.
func (p *pgCtl) writeConf(ctx context.Context, c pgConf) error {
	return p.writeOwned(ctx, filepath.Join(filepath.FromSlash(p.c.ConfDir), "conf.d", "pdh-ha.conf"), []byte(renderPGConf(c)))
}

// writeOwned schreibt eine Datei atomar und übergibt sie an postgres.
func (p *pgCtl) writeOwned(ctx context.Context, name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if _, err := p.r.Run(ctx, []string{"chown", "postgres:postgres", tmp}, nil, nil); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// renderHBABlock: Replikation und pg_rewind für die Adressen des Partners.
func renderHBABlock(addrs []string) string {
	var b strings.Builder
	b.WriteString(hbaBegin + "\n")
	for _, a := range addrs {
		mask := "/32"
		if ip := net.ParseIP(a); ip != nil && ip.To4() == nil {
			mask = "/128"
		}
		fmt.Fprintf(&b, "hostssl replication %s %s%s scram-sha-256\n", replicaUser, a, mask)
		fmt.Fprintf(&b, "hostssl all %s %s%s scram-sha-256\n", replicaUser, a, mask)
	}
	b.WriteString(hbaEnd + "\n")
	return b.String()
}

// replaceHBABlock ersetzt den markierten Block oder fügt ihn vor der ersten
// Regel ein (pg_hba gilt von oben nach unten).
func replaceHBABlock(hba, block string) string {
	lines := strings.SplitAfter(hba, "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case hbaBegin:
			if start < 0 {
				start = i
			}
		case hbaEnd:
			if start >= 0 && end < 0 {
				end = i
			}
		}
	}
	if start >= 0 && end >= start {
		return strings.Join(lines[:start], "") + block + strings.Join(lines[end+1:], "")
	}
	for i, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return strings.Join(lines[:i], "") + block + strings.Join(lines[i:], "")
		}
	}
	if hba != "" && !strings.HasSuffix(hba, "\n") {
		hba += "\n"
	}
	return hba + block
}

// parseLSN liest "16/B374D848".
func parseLSN(s string) (uint64, error) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("ungültige LSN %q", s)
	}
	h, err1 := strconv.ParseUint(hi, 16, 32)
	l, err2 := strconv.ParseUint(lo, 16, 32)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("ungültige LSN %q", s)
	}
	return h<<32 | l, nil
}

// slotName: Slot und application_name eines Knotens.
func slotName(id string) string { return "pdh_ha_" + strings.ReplaceAll(id, "-", "_") }
