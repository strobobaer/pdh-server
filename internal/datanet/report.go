package datanet

import "time"

// Meldeprotokoll zwischen Daten-Client und PDH-Server.
const (
	ReportPath      = "/v1/report"
	MaxReportBytes  = 64 << 10
	DefaultInterval = 60 * time.Second
	minInterval     = 15 * time.Second
	maxInterval     = 600 * time.Second
)

// TargetStatus ist das Prüfergebnis eines freigegebenen Ziels.
type TargetStatus struct {
	Port      int       `json:"port"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Report ist die Meldung eines Daten-Clients.
type Report struct {
	ClientID string         `json:"client_id"`
	Version  string         `json:"version"`
	OS       string         `json:"os"`
	Hostname string         `json:"hostname"`
	Targets  []TargetStatus `json:"targets"`
}

// ReportResponse ist die Antwort des PDH-Servers.
type ReportResponse struct {
	IntervalSeconds int    `json:"interval_seconds"`
	PDHVersion      string `json:"pdh_version"`
	// PDH sind alle bekannten PDH-Ziele (Host:Port, zuerst der antwortende
	// Server). Der Client merkt sie sich, damit er nach einem Rollentausch den
	// anderen Server findet.
	PDH []string `json:"pdh,omitempty"`
}

// ClampInterval begrenzt ein Meldeintervall auf 15–600 s; 0 ergibt DefaultInterval.
func ClampInterval(sec int) time.Duration {
	if sec == 0 {
		return DefaultInterval
	}
	d := time.Duration(sec) * time.Second
	if d < minInterval {
		return minInterval
	}
	if d > maxInterval {
		return maxInterval
	}
	return d
}
