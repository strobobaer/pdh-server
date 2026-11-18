//go:build e2e

// Gegenstelle für den Ende-zu-Ende-Test der Terminal-Kopplung (Gegenstück:
// hmi-kiosk internal/product/pdhterminal/pdh_e2e_test.go, Ablauf: hmi-kiosk
// test/pdh-e2e/run.sh): frische Datenbank, echte Kopplungs-API, Geräteanmeldung und
// Sitzungsprüfung über TLS mit selbst signiertem Zertifikat. Läuft bis SIGTERM.
// Nur in diesem Testprogramm gibt es POST /e2e/revoke (sperrt das Gerät wie die Admin-Seite).
//
//	PDH_E2E_DSN=postgres://… PDH_E2E_HOST=pdh-e2e go test -tags e2e -run TestTerminalPairingE2EServe -timeout 0 ./internal/web/
package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

func TestTerminalPairingE2EServe(t *testing.T) {
	dsn, host := os.Getenv("PDH_E2E_DSN"), os.Getenv("PDH_E2E_HOST")
	if dsn == "" || host == "" {
		t.Skip("PDH_E2E_DSN und PDH_E2E_HOST fehlen (hmi-kiosk test/pdh-e2e/run.sh)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	terminalDevices = newDeviceCache(30 * time.Second)
	terminalTemplatePath = "../../web/templates/terminal.gohtml"

	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, is_system_user)
		VALUES ('e2e-terminal', 'e2e-terminal@x', 'x', 'Prüfstand', 'Halle 3', 'viewer', '', '', true) RETURNING id::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	// Kopplungscode wie TerminalPairCodeWeb ihn anlegt (gleiche Hilfsfunktionen)
	code, err := newPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO terminal_pairing_codes (code_sha256, user_id, expires_at)
		VALUES ($1, $2::uuid, NOW() + interval '10 minutes')`, hashPairingCode(code), uid); err != nil {
		t.Fatal(err)
	}

	rb := rbac.NewService(rbac.NewRepository(pool))
	h := &Handler{db: pool, rbac: rb, users: users.NewService(users.NewRepository(pool), "e2e-secret", 1), jwtSecret: "e2e-secret", tmpl: loadTestTemplates(t)}

	r := chi.NewRouter()
	r.Post("/api/terminal/v1/pair", h.TerminalPairAPI)
	r.Get("/api/terminal/v1/device", h.TerminalDeviceAPI)
	r.Post("/e2e/revoke", func(w http.ResponseWriter, req *http.Request) {
		var devID string
		err := pool.QueryRow(req.Context(), `UPDATE terminal_devices SET revoked_at = NOW() WHERE user_id = $1::uuid AND revoked_at IS NULL RETURNING id::text`, uid).Scan(&devID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		terminalDevices.forget(devID)
		fmt.Println("E2E_REVOKED", devID)
		w.WriteHeader(http.StatusNoContent)
	})
	r.Mount("/", h.Routes())

	cert, caPEM := e2eSelfSigned(t, host)
	ln, err := tls.Listen("tcp", "0.0.0.0:8444", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Println("E2E_CODE=" + formatPairingCode(code))
	fmt.Println("E2E_CA=" + base64.StdEncoding.EncodeToString(caPEM))
	fmt.Println("E2E_READY")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}

// e2eSelfSigned: selbst signiertes Zertifikat (zugleich Vertrauensanker) für host.
func e2eSelfSigned(t *testing.T, host string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{host},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
