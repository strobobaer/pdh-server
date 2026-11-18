package web

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPKIProbeEmptyCAList(t *testing.T) {
	// RequestClientCert ohne ClientCAs: der Server nennt keine CA und nimmt damit jede an
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	if st := pkiProbe(context.Background(), srv.Listener.Addr().String(), pkiTestCA(t).Cert.RawSubject); st.State != pkiEnforceActive {
		t.Errorf("leere CA-Liste: %+v", st)
	}
}

func TestPKICacheRules(t *testing.T) {
	if pkiCacheTTL(pkiEnforceActive) != 5*time.Minute || pkiCacheTTL(pkiEnforceOff) > 20*time.Second || pkiCacheTTL(pkiEnforceUnknown) > 20*time.Second {
		t.Error("Zwischenspeicher: aktiv 5 Minuten, sonst höchstens 20 Sekunden")
	}
	ca := pkiTestCA(t)
	subj := func() ([]byte, error) { return ca.Cert.RawSubject, nil }
	base := "https://" + pkiTestServer(t, ca, true)
	pkiProbeCache.Lock()
	pkiProbeCache.key = ""
	pkiProbeCache.Unlock()
	// abgebrochene Anfrage: nichts wird gemerkt
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if st := pkiEnforcementFor(cctx, base, subj); st.State == pkiEnforceActive {
		t.Fatalf("abgebrochen und trotzdem aktiv: %+v", st)
	}
	if st := pkiEnforcementFor(context.Background(), base, subj); st.State != pkiEnforceActive {
		t.Errorf("Ergebnis der abgebrochenen Anfrage wurde gemerkt: %+v", st)
	}
}

func TestPKIServerHintWithoutAddress(t *testing.T) {
	got := pkiServerHint("", nil)
	if !strings.HasPrefix(got, "Für die öffentliche Nextcloud-Adresse gilt das Let's-Encrypt-Zertifikat") {
		t.Errorf("Hinweis ohne Adresse: %s", got)
	}
}

func TestPKIEnforcementWebForbidden(t *testing.T) {
	pool := workTestPool(t)
	ctx := context.Background()
	var old string
	had := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, pkiSelfServiceSetting).Scan(&old) == nil
	if _, err := pool.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1,'0',NOW()) ON CONFLICT (key) DO UPDATE SET value='0'`, pkiSelfServiceSetting); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_, _ = pool.Exec(ctx, `UPDATE app_settings SET value=$2 WHERE key=$1`, pkiSelfServiceSetting, old)
		} else {
			_, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key=$1`, pkiSelfServiceSetting)
		}
	})
	h := &Handler{db: pool, hasPermHook: func(*http.Request, string) bool { return false }}
	w := httptest.NewRecorder()
	h.PKIEnforcementWeb(w, httptest.NewRequest(http.MethodGet, "/account/certificates/enforcement", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("Status %d, erwartet 403", w.Code)
	}
}
