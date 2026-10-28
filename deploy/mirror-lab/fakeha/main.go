// Command fakeha: Platzhalter für den pdh-ha-Statussocket im Spiegel-Labor
// (deploy/mirror-lab, nur Testumgebung). Antwortet auf GET /v1/status wie
// pdh-ha auf dem passiven Server: dieser Server passiv, Partner aktiv.
// Ob der aktive Partner „gesehen“ wird, steht in einer Datei (true/false) und
// lässt sich zur Laufzeit umschalten.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	sock := flag.String("socket", "/run/pdh-ha/api.sock", "Unix-Socket")
	seenFile := flag.String("seen", "/tmp/lab-seen", "Datei mit true/false: aktiver Partner gesehen")
	peer := flag.String("peer", "pdh1:7400", "Adresse des aktiven Partners")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*sock), 0o755); err != nil {
		log.Fatal(err)
	}
	_ = os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(*seenFile)
		seen := strings.TrimSpace(string(b)) != "false" // fehlt die Datei: gesehen
		writeJSON(w, map[string]any{
			"ok":         true,
			"configured": true,
			"mode":       "duo",
			"self":       map[string]any{"id": "pdh2", "kind": "node", "role": "passiv", "pg": "standby"},
			"peers": []map[string]any{{
				"id": "pdh1", "kind": "node", "role": "aktiv", "seen": seen, "pg": "primary",
				"paths": []map[string]any{{"addr": *peer, "alive": true}},
			}},
			"auto_mode": "observe",
		})
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"events": []any{}})
	})
	log.Printf("fakeha: %s (gesehen aus %s)", *sock, *seenFile)
	log.Fatal(http.Serve(ln, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
