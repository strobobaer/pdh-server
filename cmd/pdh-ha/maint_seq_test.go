package main

import (
	"net/http"
	"strings"
	"testing"
)

// Der Status nennt den Stand der Wartung (maint_seq): Der Update-Agent schaltet eine von ihm
// eingeschaltete Wartung nur aus, wenn der Stand noch seiner ist (pdh-static-ip.sh, --maint-off).
func TestStatusMaintSeq(t *testing.T) {
	_, _, sock := apiDaemon(t)
	var raw map[string]any
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["maint_seq"]; !ok {
		t.Fatalf("Feld maint_seq fehlt: %v", raw)
	}
	var st apiStatus
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &st); err != nil {
		t.Fatal(err)
	}
	seq := st.MaintSeq
	for i, on := range []bool{true, false} {
		if err := apiDo(sock, http.MethodPost, "/v1/maintenance", map[string]bool{"on": on}, nil); err != nil {
			t.Fatal(err)
		}
		if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &st); err != nil {
			t.Fatal(err)
		}
		if st.Maint != on || st.MaintSeq != seq+uint64(i)+1 {
			t.Errorf("nach maintenance %v: maint=%v maint_seq=%d (vorher %d)", on, st.Maint, st.MaintSeq, seq)
		}
	}
	// die Kommandozeile gibt das Feld mit aus (das Skript liest es aus „status --json“)
	var out, errb strings.Builder
	t.Setenv("PDH_HA_SOCKET", sock)
	if rc := runCLI([]string{"status", "--json"}, &out, &errb); rc != 0 || !strings.Contains(out.String(), `"maint_seq":`) || !strings.Contains(out.String(), `"maint":false`) {
		t.Errorf("status --json: %d %q %q", rc, out.String(), errb.String())
	}
}
