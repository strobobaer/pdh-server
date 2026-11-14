package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Serverlast: Alle -sample-Sekunden liest ein einziger ssh-Aufruf (mit -n, damit
// ssh nichts von der Eingabe verschluckt) Last, CPU, RAM und den Speicher der
// Prozesse pdh und postgres. Der Befehl auf dem Server ist fest eingebaut.
const remoteCmd = `cat /proc/loadavg; vmstat 1 2 | tail -1; free -m | awk '/^Mem:/{print $2, $3, $7}'; ` +
	`ps -C pdh -o rss= | awk '{s+=$1} END{print s+0}'; ps -C postgres -o rss= | awk '{s+=$1} END{print s+0}'`

// srvSample: eine Messung.
type srvSample struct {
	Load1, Load5  float64
	CPUBusy       float64 // 100 - Leerlauf (vmstat)
	MemTotalMB    int
	MemUsedMB     int
	MemAvailMB    int
	PDHRSSMB      float64
	PostgresRSSMB float64
}

// parseServerSample liest die Ausgabe von remoteCmd (fünf Zeilen).
func parseServerSample(out string) (srvSample, error) {
	var s srvSample
	lines := nonEmptyLines(out)
	if len(lines) < 5 {
		return s, fmt.Errorf("erwartet 5 Zeilen, erhalten %d", len(lines))
	}
	lf := strings.Fields(lines[0])
	if len(lf) < 2 {
		return s, fmt.Errorf("loadavg nicht lesbar: %q", lines[0])
	}
	var err error
	if s.Load1, err = strconv.ParseFloat(lf[0], 64); err != nil {
		return s, fmt.Errorf("loadavg: %w", err)
	}
	if s.Load5, err = strconv.ParseFloat(lf[1], 64); err != nil {
		return s, fmt.Errorf("loadavg: %w", err)
	}
	// vmstat: r b swpd free buff cache si so bi bo in cs us sy id wa st → id ist Spalte 15
	vf := strings.Fields(lines[1])
	if len(vf) < 15 {
		return s, fmt.Errorf("vmstat nicht lesbar: %q", lines[1])
	}
	idle, err := strconv.ParseFloat(vf[14], 64)
	if err != nil {
		return s, fmt.Errorf("vmstat: %w", err)
	}
	s.CPUBusy = 100 - idle
	mf := strings.Fields(lines[2])
	if len(mf) < 3 {
		return s, fmt.Errorf("free nicht lesbar: %q", lines[2])
	}
	for i, dst := range []*int{&s.MemTotalMB, &s.MemUsedMB, &s.MemAvailMB} {
		if *dst, err = strconv.Atoi(mf[i]); err != nil {
			return s, fmt.Errorf("free: %w", err)
		}
	}
	pdhKB, err := strconv.ParseFloat(strings.TrimSpace(lines[3]), 64)
	if err != nil {
		return s, fmt.Errorf("pdh-rss: %w", err)
	}
	pgKB, err := strconv.ParseFloat(strings.TrimSpace(lines[4]), 64)
	if err != nil {
		return s, fmt.Errorf("postgres-rss: %w", err)
	}
	s.PDHRSSMB, s.PostgresRSSMB = pdhKB/1024, pgKB/1024
	return s, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// srvStats: Mittel und Höchstwerte über alle Messungen.
type srvStats struct {
	N, Failed                    int
	Load1Sum, Load1Max           float64
	CPUSum, CPUMax               float64
	MemUsedSum                   float64
	MemUsedMax, MemTotalMB       int
	PDHSum, PDHMax, PGSum, PGMax float64
}

func (s *srvStats) add(x srvSample) {
	s.N++
	s.Load1Sum += x.Load1
	s.Load1Max = max(s.Load1Max, x.Load1)
	s.CPUSum += x.CPUBusy
	s.CPUMax = max(s.CPUMax, x.CPUBusy)
	s.MemUsedSum += float64(x.MemUsedMB)
	s.MemUsedMax = max(s.MemUsedMax, x.MemUsedMB)
	s.MemTotalMB = x.MemTotalMB
	s.PDHSum += x.PDHRSSMB
	s.PDHMax = max(s.PDHMax, x.PDHRSSMB)
	s.PGSum += x.PostgresRSSMB
	s.PGMax = max(s.PGMax, x.PostgresRSSMB)
}

func (s *srvStats) mean(sum float64) float64 {
	if s.N == 0 {
		return 0
	}
	return sum / float64(s.N)
}

func (s *srvStats) text() string {
	return fmt.Sprintf("Serverlast (%d Messungen, %d fehlgeschlagen): Last(1 min) Mittel %.2f / Höchst %.2f · CPU belegt Mittel %.0f %% / Höchst %.0f %% · RAM belegt Mittel %.0f MB / Höchst %d MB von %d MB · pdh RSS Mittel %.0f MB / Höchst %.0f MB · postgres RSS (Summe) Mittel %.0f MB / Höchst %.0f MB\n",
		s.N, s.Failed, s.mean(s.Load1Sum), s.Load1Max, s.mean(s.CPUSum), s.CPUMax, s.mean(s.MemUsedSum), s.MemUsedMax, s.MemTotalMB,
		s.mean(s.PDHSum), s.PDHMax, s.mean(s.PGSum), s.PGMax)
}

func (s *srvStats) markdown() string {
	return fmt.Sprintf("| | Mittel | Höchstwert |\n|---|---:|---:|\n| Last (1 min) | %.2f | %.2f |\n| CPU belegt (%%) | %.0f | %.0f |\n| RAM belegt (MB von %d) | %.0f | %d |\n| Prozess pdh, RSS (MB) | %.0f | %.0f |\n| Prozesse postgres, RSS-Summe (MB) | %.0f | %.0f |\n\n%d Messungen, %d fehlgeschlagen. Die postgres-Summe zählt gemeinsam genutzten Speicher mehrfach und überschätzt den echten Verbrauch.\n",
		s.mean(s.Load1Sum), s.Load1Max, s.mean(s.CPUSum), s.CPUMax, s.MemTotalMB, s.mean(s.MemUsedSum), s.MemUsedMax,
		s.mean(s.PDHSum), s.PDHMax, s.mean(s.PGSum), s.PGMax, s.N, s.Failed)
}

// readServer ruft den Server einmal ab.
func readServer(ctx context.Context, host string) (srvSample, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", "-n", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, remoteCmd)
	out, err := cmd.Output()
	if err != nil {
		return srvSample{}, fmt.Errorf("ssh: %w", err)
	}
	return parseServerSample(string(out))
}

// monitorServer misst bis ctx endet alle every einmal; liefert die Auswertung.
func monitorServer(ctx context.Context, host string, every time.Duration, out *srvStats, warn func(string)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s, err := readServer(ctx, host)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				out.Failed++
				if out.Failed <= 3 {
					warn("Serverlast: " + err.Error())
				}
				continue
			}
			out.add(s)
		}
	}
}
