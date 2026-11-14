package extsync

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Last des Servers (Entwurf 4.9): Der Abgleich hat immer Nachrang vor den
// Benutzern. Vor jedem hereinkommenden Lauf und vor jedem Durchgang der
// Warteliste prüft der Taktgeber die Last; ist der Server überlastet, geht der
// Abgleich in Warteposition.

// Schwellen der Überlastung. Vorgaben, die nach der Lastprüfung bestätigt oder
// angepasst werden.
const (
	LoadMaxCPUPerCore   = 0.85 // Last der letzten Minute je Kern
	LoadMinMemAvailable = 0.10 // Anteil verfügbarer Arbeitsspeicher
	LoadMaxDBUsed       = 0.80 // Anteil belegter Datenbankverbindungen des PDH
)

// Gründe der Warteposition, wie sie im Cockpit und im Lauf stehen.
const (
	LoadReasonCPU = "Prozessor"
	LoadReasonMem = "Arbeitsspeicher"
	LoadReasonDB  = "Datenbank"
)

// Load ist eine Messung, alle Werte als Anteil 0..1 (CPUPerCore kann über 1
// liegen). Known = false heißt: kein Messwert – dann gilt der Server nie als
// überlastet.
type Load struct {
	CPUPerCore   float64
	MemAvailable float64
	DBUsed       float64
	Known        bool
}

// LoadProbe misst die Last des Servers.
type LoadProbe func(ctx context.Context) Load

// Overloaded sagt, ob der Server überlastet ist, und nennt den Grund
// (Prozessor, Arbeitsspeicher oder Datenbank). Die Schwellen selbst gelten noch
// als in Ordnung.
func (l Load) Overloaded() (bool, string) {
	switch {
	case !l.Known:
		return false, ""
	case l.CPUPerCore > LoadMaxCPUPerCore:
		return true, LoadReasonCPU
	case l.MemAvailable < LoadMinMemAvailable:
		return true, LoadReasonMem
	case l.DBUsed > LoadMaxDBUsed:
		return true, LoadReasonDB
	}
	return false, ""
}

// SystemLoad misst Prozessor und Arbeitsspeicher über /proc (Linux) und die
// belegten Datenbankverbindungen über den Pool. Was nicht messbar ist, zählt
// als unauffällig; gibt es gar keinen Messwert (kein /proc, kein Pool), ist
// Known = false.
func SystemLoad(pool *pgxpool.Pool) LoadProbe {
	return func(context.Context) Load {
		l := Load{MemAvailable: 1}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if v, err := parseLoadAvg(string(b)); err == nil {
				l.CPUPerCore = v / float64(max(runtime.NumCPU(), 1))
				l.Known = true
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			if v, err := parseMemInfo(string(b)); err == nil {
				l.MemAvailable = v
				l.Known = true
			}
		}
		if pool != nil {
			if st := pool.Stat(); st.MaxConns() > 0 {
				l.DBUsed = float64(st.AcquiredConns()) / float64(st.MaxConns())
				l.Known = true
			}
		}
		return l
	}
}

// parseLoadAvg liest die Last der letzten Minute aus /proc/loadavg.
func parseLoadAvg(s string) (float64, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, errors.New("loadavg ist leer")
	}
	return strconv.ParseFloat(f[0], 64)
}

// parseMemInfo liefert den Anteil des verfügbaren Arbeitsspeichers
// (MemAvailable / MemTotal) aus /proc/meminfo.
func parseMemInfo(s string) (float64, error) {
	var total, avail float64
	var hasTotal, hasAvail bool
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		switch name {
		case "MemTotal":
			total, hasTotal = v, true
		case "MemAvailable":
			avail, hasAvail = v, true
		}
	}
	if !hasTotal || !hasAvail || total <= 0 {
		return 0, errors.New("meminfo ohne MemTotal oder MemAvailable")
	}
	return avail / total, nil
}
