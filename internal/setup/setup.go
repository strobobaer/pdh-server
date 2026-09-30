// Package setup stellt den Einrichtungsassistenten fuer die Erstinstallation
// bereit. Er laeuft, bevor der eigentliche PDH-Server startet:
//
//   - Datenbank nicht erreichbar: Zugangsdaten abfragen oder Datenbank
//     (samt eigenem Benutzer) automatisch anlegen, Migrationen ausfuehren,
//   - noch kein Benutzer vorhanden: ersten Administrator anlegen.
//
// Zugriff nur mit dem Einrichtungscode, der beim Start im Protokoll steht
// und in setup-code.txt abgelegt wird - so kann niemand im Netz die
// Installation "kapern", bevor der Administrator sie abschliesst.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"pdh"
	"pdh/pkg/config"
	"pdh/pkg/database"
)

type Options struct {
	Addr          string
	NeedDB        bool          // Datenbank einrichten
	DBError       string        // letzter Verbindungsfehler (Anzeige)
	Pool          *pgxpool.Pool // vorhandene Datenbank (nur Administrator anlegen)
	MigrationsDir string
	TemplatePath  string
}

type wizard struct {
	opts     Options
	code     string
	codeFile string
	tmpl     *template.Template
	done     chan struct{}
	once     sync.Once

	mu       sync.Mutex
	fails    int
	lockedAt time.Time
	pool     *pgxpool.Pool
	dbWait   string // Hintergrund-Pruefung der konfigurierten Datenbank
}

// Run blockiert, bis die Einrichtung abgeschlossen ist.
func Run(ctx context.Context, o Options) error {
	if o.MigrationsDir == "" {
		o.MigrationsDir = "migrations"
	}
	if o.TemplatePath == "" {
		o.TemplatePath = filepath.Join("web", "templates", "setup.gohtml")
	}
	tmpl, err := template.New(filepath.Base(o.TemplatePath)).Funcs(template.FuncMap{
		"list": func(v ...string) []string { return v },
	}).ParseFiles(o.TemplatePath)
	if err != nil {
		return fmt.Errorf("einrichtungsassistent: %w", err)
	}
	w := &wizard{opts: o, code: newCode(), tmpl: tmpl, done: make(chan struct{}), pool: o.Pool}
	w.codeFile = "setup-code.txt"
	_ = os.WriteFile(w.codeFile, []byte(w.code+"\n"), 0o600)
	defer os.Remove(w.codeFile)

	mux := http.NewServeMux()
	mux.HandleFunc("/setup", w.page)
	mux.HandleFunc("/setup/test-db", w.testDB)
	mux.HandleFunc("/setup/finish", w.finish)
	if icons, err := fs.Sub(pdh.Static, "web/static"); err == nil {
		mux.Handle("/favicon.ico", http.FileServer(http.FS(icons)))
		mux.Handle("/favicon.svg", http.FileServer(http.FS(icons)))
	}
	mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
		w.mu.Lock()
		need := w.pool == nil
		w.mu.Unlock()
		fmt.Fprintf(rw, `{"status":"setup","needdb":%t}`, need)
	})
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/setup", http.StatusSeeOther)
	})
	srv := &http.Server{Addr: o.Addr, Handler: securityHeaders(mux), ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	banner := strings.Repeat("=", 64)
	setupLog().Warn().Msg(banner)
	setupLog().Warn().Str("adresse", "http://<server>:"+portOf(o.Addr)+"/setup").Msg("PDH-EINRICHTUNGSASSISTENT aktiv")
	setupLog().Warn().Str("einrichtungscode", w.code).Str("datei", w.codeFile).Msg("Einrichtungscode (wird im Assistenten abgefragt)")
	setupLog().Warn().Msg(banner)
	fmt.Fprintf(os.Stderr, "\n%s\n  PDH-Einrichtung: http://<server>:%s/setup\n  Einrichtungscode: %s\n%s\n\n", banner, portOf(o.Addr), w.code, banner)

	if o.NeedDB {
		go w.watchConfiguredDB(ctx)
	}
	select {
	case <-w.done:
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("einrichtungsassistent: %w", err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// Abschlussseite noch ausliefern lassen, dann Port freigeben.
	time.Sleep(800 * time.Millisecond)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	if w.pool != nil && w.pool != o.Pool {
		w.pool.Close()
	}
	return nil
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return "8090"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func newCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // ohne 0/O, 1/I
	b := make([]byte, 8)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// checkCode vergleicht den Einrichtungscode (Bindestrich/Gross-Klein egal)
// und sperrt nach 10 Fehlversuchen fuer eine Minute.
func (w *wizard) checkCode(in string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fails >= 10 && time.Since(w.lockedAt) < time.Minute {
		return errors.New("Zu viele falsche Versuche – bitte eine Minute warten.")
	}
	norm := func(s string) string { return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "")) }
	if subtle.ConstantTimeCompare([]byte(norm(in)), []byte(norm(w.code))) == 1 {
		w.fails = 0
		return nil
	}
	w.fails++
	if w.fails >= 10 {
		w.lockedAt = time.Now()
	}
	return errors.New("Der Einrichtungscode stimmt nicht. Er steht im Server-Protokoll und in der Datei setup-code.txt.")
}

// watchConfiguredDB: startet die Datenbank nur etwas spaeter (Neustart des
// Servers, Docker), geht es ohne Eingabe weiter.
func (w *wizard) watchConfiguredDB(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-t.C:
		}
		w.mu.Lock()
		busy := w.pool != nil
		w.mu.Unlock()
		if busy {
			return
		}
		cfg, err := config.Load()
		if err != nil || cfg.Database.User == "" {
			continue
		}
		if database.TestConnection(ctx, &cfg.Database) != nil {
			continue
		}
		db, err := database.New(&cfg.Database)
		if err != nil {
			continue
		}
		if n, err := humanUsers(ctx, db.Pool); err == nil && n > 0 {
			setupLog().Info().Msg("einrichtung: konfigurierte datenbank jetzt erreichbar - normaler start")
			db.Close()
			w.finishOnce()
			return
		}
		// Datenbank da, aber noch ohne Benutzer: nur noch Administrator abfragen.
		if err := database.RunMigrations(ctx, db.Pool, w.opts.MigrationsDir); err != nil {
			db.Close()
			continue
		}
		w.mu.Lock()
		w.pool = db.Pool
		w.dbWait = "Die Datenbank ist jetzt erreichbar – es fehlt nur noch der Administrator."
		w.mu.Unlock()
		return
	}
}

func (w *wizard) finishOnce() { w.once.Do(func() { close(w.done) }) }

func humanUsers(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE NOT COALESCE(is_bot, false)`).Scan(&n)
	if err != nil {
		// Tabelle fehlt (leere Datenbank) oder aeltere Struktur
		err2 := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
		if err2 != nil {
			return 0, err
		}
	}
	return n, nil
}

// ── Seiten ───────────────────────────────────────────────────

type pageData struct {
	NeedDB          bool
	DBError, Notice string
	Err             string
	Done            bool
	DoneNotes       []string
	EnvFile         string
	DBFromEnv       bool
	Form            map[string]string
	PublicURL       string
	DefaultDBHost   string
}

func (w *wizard) render(rw http.ResponseWriter, d pageData) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := w.tmpl.ExecuteTemplate(rw, "setup", d); err != nil {
		http.Error(rw, err.Error(), 500)
	}
}

func (w *wizard) baseData(r *http.Request) pageData {
	w.mu.Lock()
	needDB := w.pool == nil
	notice := w.dbWait
	w.mu.Unlock()
	d := pageData{
		NeedDB: needDB, DBError: w.opts.DBError, Notice: notice, EnvFile: config.EnvFilePath(),
		DBFromEnv: config.FromProcessEnv("PDH_DATABASE_HOST") || config.FromProcessEnv("PDH_DATABASE_PASSWORD"),
		Form:      map[string]string{},
	}
	if !needDB {
		d.DBError = ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	d.PublicURL = os.Getenv("PDH_PUBLIC_URL")
	if d.PublicURL == "" {
		d.PublicURL = scheme + "://" + r.Host
	}
	d.DefaultDBHost = os.Getenv("PDH_DATABASE_HOST")
	if d.DefaultDBHost == "" {
		d.DefaultDBHost = "localhost"
	}
	for _, k := range []string{"PDH_DATABASE_PORT", "PDH_DATABASE_USER", "PDH_DATABASE_NAME", "PDH_DATABASE_SSLMODE"} {
		d.Form[k] = os.Getenv(k)
	}
	return d
}

func (w *wizard) page(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, w.baseData(r))
}

// testDB: Verbindungstest per fetch (JSON), verlangt den Code.
func (w *wizard) testDB(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	reply := func(ok bool, msg string) {
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{"ok": ok, "message": msg})
	}
	if r.Method != http.MethodPost {
		reply(false, "POST erwartet")
		return
	}
	_ = r.ParseForm()
	if err := w.checkCode(r.FormValue("code")); err != nil {
		reply(false, err.Error())
		return
	}
	if r.FormValue("db_mode") == "auto" {
		admin := formDB(r, "admin_user", "admin_password", "postgres")
		if err := database.TestConnection(r.Context(), admin); err != nil {
			reply(false, "Anmeldung als "+admin.User+" fehlgeschlagen: "+err.Error())
			return
		}
		reply(true, "Anmeldung als "+admin.User+" erfolgreich – PDH kann Benutzer und Datenbank anlegen.")
		return
	}
	cfg := formDB(r, "db_user", "db_password", r.FormValue("db_name"))
	err := database.TestConnection(r.Context(), cfg)
	switch {
	case err == nil:
		reply(true, "Verbindung erfolgreich.")
	case database.IsMissingDatabase(err):
		reply(true, "Anmeldung ok – die Datenbank „"+cfg.Name+"“ gibt es noch nicht; PDH legt sie beim Abschluss an.")
	default:
		reply(false, "Keine Verbindung: "+err.Error())
	}
}

func formDB(r *http.Request, userField, pwField, dbName string) *config.DatabaseConfig {
	port, _ := strconv.Atoi(r.FormValue("db_port"))
	if port == 0 {
		port = 5432
	}
	ssl := r.FormValue("db_sslmode")
	if ssl == "" {
		ssl = "disable"
	}
	return &config.DatabaseConfig{
		Host: strings.TrimSpace(r.FormValue("db_host")), Port: port,
		User: strings.TrimSpace(r.FormValue(userField)), Password: r.FormValue(pwField),
		Name: strings.TrimSpace(dbName), SSLMode: ssl,
	}
}

func (w *wizard) finish(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(rw, r, "/setup", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	d := w.baseData(r)
	for k, v := range r.Form {
		if !strings.Contains(k, "password") && len(v) > 0 {
			d.Form[k] = v[0]
		}
	}
	fail := func(err error) {
		d.Err = err.Error()
		w.render(rw, d)
	}
	if err := w.checkCode(r.FormValue("code")); err != nil {
		fail(err)
		return
	}
	adm, err := adminFromForm(r)
	if err != nil {
		fail(err)
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	if publicURL != "" {
		if u, err := url.Parse(publicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail(errors.New("Öffentliche Adresse: bitte vollständig mit http:// oder https:// angeben."))
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var notes []string
	updates := map[string]string{}
	if publicURL != "" && !config.FromProcessEnv("PDH_PUBLIC_URL") {
		updates["PDH_PUBLIC_URL"] = publicURL
	}
	w.mu.Lock()
	pool := w.pool
	w.mu.Unlock()

	if pool == nil {
		// ── Datenbank einrichten ──
		for _, k := range []string{"PDH_DATABASE_HOST", "PDH_DATABASE_PORT", "PDH_DATABASE_USER", "PDH_DATABASE_PASSWORD", "PDH_DATABASE_NAME", "PDH_DATABASE_SSLMODE"} {
			if config.FromProcessEnv(k) {
				fail(fmt.Errorf("%s ist von außen (Docker/systemd) vorgegeben und kann hier nicht geändert werden – bitte dort anpassen und den Dienst neu starten.", k))
				return
			}
		}
		var cfg *config.DatabaseConfig
		if r.FormValue("db_mode") == "auto" {
			admin := formDB(r, "admin_user", "admin_password", "postgres")
			role := strings.TrimSpace(r.FormValue("new_role"))
			name := strings.TrimSpace(r.FormValue("new_db"))
			pw := config.RandomSecret(18)
			cfg, notes, err = database.Provision(ctx, admin, role, pw, name)
			if err != nil {
				fail(err)
				return
			}
		} else {
			cfg = formDB(r, "db_user", "db_password", r.FormValue("db_name"))
			if cfg.User == "" || cfg.Name == "" {
				fail(errors.New("Bitte Benutzer und Datenbankname angeben."))
				return
			}
			err = database.TestConnection(ctx, cfg)
			if database.IsMissingDatabase(err) {
				if err = database.CreateDatabaseAsOwner(ctx, cfg); err == nil {
					notes = append(notes, "Datenbank "+cfg.Name+" angelegt")
					err = database.TestConnection(ctx, cfg)
				}
			}
			if err != nil {
				fail(fmt.Errorf("Datenbank: %v", err))
				return
			}
		}
		updates["PDH_DATABASE_HOST"] = cfg.Host
		updates["PDH_DATABASE_PORT"] = strconv.Itoa(cfg.Port)
		updates["PDH_DATABASE_USER"] = cfg.User
		updates["PDH_DATABASE_PASSWORD"] = cfg.Password
		updates["PDH_DATABASE_NAME"] = cfg.Name
		updates["PDH_DATABASE_SSLMODE"] = cfg.SSLMode
		db, err := database.New(cfg)
		if err != nil {
			fail(err)
			return
		}
		if err := database.RunMigrations(ctx, db.Pool, w.opts.MigrationsDir); err != nil {
			db.Close()
			fail(fmt.Errorf("Datenbankstruktur anlegen: %v", err))
			return
		}
		notes = append(notes, "Datenbankstruktur angelegt")
		pool = db.Pool
		w.mu.Lock()
		w.pool = pool
		w.mu.Unlock()
	}

	if n, err := humanUsers(ctx, pool); err == nil && n > 0 {
		notes = append(notes, "Es gibt bereits Benutzer – kein weiterer Administrator angelegt")
	} else if err := createAdmin(ctx, pool, adm); err != nil {
		fail(fmt.Errorf("Administrator anlegen: %v", err))
		return
	} else {
		notes = append(notes, "Administrator "+adm.username+" angelegt")
	}

	if len(updates) > 0 {
		if err := config.WriteEnvFile(config.EnvFilePath(), updates); err != nil {
			fail(fmt.Errorf("Einstellungsdatei %s schreiben: %v", config.EnvFilePath(), err))
			return
		}
		if err := config.ApplyEnvFile(config.EnvFilePath()); err != nil {
			fail(err)
			return
		}
		notes = append(notes, "Einstellungen in "+config.EnvFilePath()+" gespeichert")
	}
	setupLog().Info().Strs("schritte", notes).Msg("einrichtung abgeschlossen")
	d.Done, d.DoneNotes = true, notes
	w.render(rw, d)
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
	w.finishOnce()
}

type adminInput struct {
	first, last, username, email, password string
}

func adminFromForm(r *http.Request) (adminInput, error) {
	a := adminInput{
		first: strings.TrimSpace(r.FormValue("first_name")), last: strings.TrimSpace(r.FormValue("last_name")),
		username: strings.ToLower(strings.TrimSpace(r.FormValue("username"))), email: strings.TrimSpace(r.FormValue("email")),
		password: r.FormValue("password"),
	}
	switch {
	case a.first == "" || a.last == "":
		return a, errors.New("Bitte Vor- und Nachnamen des Administrators angeben.")
	case len(a.username) < 3 || strings.ContainsAny(a.username, " \t"):
		return a, errors.New("Benutzername: mindestens 3 Zeichen, keine Leerzeichen.")
	case a.username == "pdh-system":
		return a, errors.New("Dieser Benutzername ist reserviert.")
	}
	if _, err := mail.ParseAddress(a.email); err != nil {
		return a, errors.New("Bitte eine gültige E-Mail-Adresse angeben.")
	}
	if len([]rune(a.password)) < 10 {
		return a, errors.New("Das Passwort muss mindestens 10 Zeichen lang sein.")
	}
	if a.password != r.FormValue("password2") {
		return a, errors.New("Die beiden Passwörter stimmen nicht überein.")
	}
	return a, nil
}

func createAdmin(ctx context.Context, pool *pgxpool.Pool, a adminInput) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(a.password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO users (username, email, password_hash, first_name, last_name, role, active)
		VALUES ($1, $2, $3, $4, $5, 'admin', true)`, a.username, a.email, string(hash), a.first, a.last)
	return err
}

func setupLog() *zerolog.Logger {
	l := log.With().Str("bereich", "einrichtung").Logger()
	return &l
}
