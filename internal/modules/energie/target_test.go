package energie

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestDirTargetAtomic(t *testing.T) {
	dir := t.TempDir()
	tg := DirTarget{Dir: dir}
	final := filepath.Join(dir, "x.csv")

	// Während Put darf nie eine halbe x.csv existieren: Beobachter prüft laufend.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad string
	var mu sync.Mutex
	content := strings.Repeat("neu\n", 50000)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if runtime.GOOS == "windows" {
				return // dort blockiert jeder offene Zugriff das Umbenennen; die Prüfung gilt für Linux
			}
			if st, err := os.Stat(final); err == nil {
				if n := st.Size(); n != 3 && n != int64(len(content)) {
					mu.Lock()
					bad = fmt.Sprintf("%d Bytes", n)
					mu.Unlock()
				}
			}
		}
	}()
	if err := os.WriteFile(final, []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := tg.Put(context.Background(), "x.csv", []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if bad != "" {
		t.Fatalf("halbe Datei sichtbar: %q", bad)
	}
	b, _ := os.ReadFile(final)
	if string(b) != content {
		t.Error("Inhalt weicht ab")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) != 0 {
		t.Errorf("übrig: %v", left)
	}
	if msg, err := tg.Test(context.Background()); err != nil || !strings.Contains(msg, "Verbindung in Ordnung") {
		t.Errorf("Test: %q %v", msg, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".pdh-test*")); len(left) != 0 {
		t.Errorf("Testdatei übrig: %v", left)
	}
}

func TestDirTargetMissingDir(t *testing.T) {
	tg := DirTarget{Dir: filepath.Join(t.TempDir(), "gibt-es-nicht")}
	if err := tg.Put(context.Background(), "x.csv", []byte("a")); err == nil {
		t.Error("Fehler erwartet")
	}
	if _, err := tg.Test(context.Background()); err == nil {
		t.Error("Fehler erwartet")
	}
	if _, err := os.Stat(tg.Dir); err == nil {
		t.Error("Ordner darf nicht angelegt werden")
	}
	if err := (DirTarget{}).Put(context.Background(), "x.csv", nil); err == nil {
		t.Error("leerer Ordner: Fehler erwartet")
	}
	if err := (DirTarget{Dir: t.TempDir()}).Put(context.Background(), "../x.csv", nil); err == nil {
		t.Error("Pfad im Namen: Fehler erwartet")
	}
}

// ── SFTP-Fake ───────────────────────────────────────────────

type sftpFake struct {
	addr string
	dir  string
	key  ssh.PublicKey
}

func startSFTP(t *testing.T) *sftpFake {
	t.Helper()
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "pdh" && string(pw) == "geheim" {
			return nil, nil
		}
		return nil, errors.New("falsches Kennwort")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					if nch.ChannelType() != "session" {
						_ = nch.Reject(ssh.UnknownChannelType, "nur session")
						continue
					}
					ch, creqs, err := nch.Accept()
					if err != nil {
						return
					}
					go func() {
						for r := range creqs {
							ok := r.Type == "subsystem" && len(r.Payload) >= 4 && string(r.Payload[4:]) == "sftp"
							_ = r.Reply(ok, nil)
							if ok {
								srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(dir))
								if err == nil {
									_ = srv.Serve()
								}
								_ = ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return &sftpFake{addr: ln.Addr().String(), dir: dir, key: signer.PublicKey()}
}

func (f *sftpFake) target(hostKey string) SFTPTarget {
	host, port, _ := net.SplitHostPort(f.addr)
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return SFTPTarget{Host: host, Port: p, User: "pdh", Password: "geheim", HostKey: hostKey, Timeout: 5 * time.Second}
}

func TestSFTPTargetHostKey(t *testing.T) {
	f := startSFTP(t)
	ctx := context.Background()
	fp := HostKeyFingerprint(f.key)
	if !strings.HasPrefix(fp, "SHA256:") || strings.HasSuffix(fp, "=") {
		t.Fatalf("Fingerabdruck %q", fp)
	}

	// falscher Fingerabdruck
	err := f.target("SHA256:falsch").Put(ctx, "x.csv", []byte("a"))
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Mismatch erwartet, war %v", err)
	}
	// leer: nichts wird übertragen, Test nennt den Fingerabdruck
	if err := f.target("").Put(ctx, "x.csv", []byte("a")); !errors.Is(err, ErrHostKeyMissing) {
		t.Fatalf("Missing erwartet, war %v", err)
	}
	_, err = f.target("").Test(ctx)
	if !errors.Is(err, ErrHostKeyMissing) || !strings.Contains(err.Error(), fp) {
		t.Fatalf("Test ohne Schlüssel: %v", err)
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 0 {
		t.Fatalf("es wurde trotz fehlendem Schlüssel geschrieben: %v", entries)
	}
	// falsches Kennwort
	bad := f.target(fp)
	bad.Password = "x"
	if err := bad.Put(ctx, "x.csv", []byte("a")); err == nil || errors.Is(err, ErrHostKeyMismatch) {
		t.Errorf("Anmeldefehler erwartet, war %v", err)
	}

	// richtig
	tg := f.target(fp)
	if msg, err := tg.Test(ctx); err != nil || !strings.Contains(msg, "Verbindung in Ordnung") {
		t.Fatalf("Test: %q %v", msg, err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "x.csv"), []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tg.Put(ctx, "x.csv", []byte("neu")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "x.csv"))
	if err != nil || string(b) != "neu" {
		t.Fatalf("Datei: %q %v", b, err)
	}
	entries, _ := os.ReadDir(f.dir)
	if len(entries) != 1 {
		t.Errorf("außer x.csv darf nichts liegen: %v", entries)
	}
}

func TestTargetTimeoutDoesNotHang(t *testing.T) {
	// Ein Server, der annimmt, aber nie antwortet.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	for _, tg := range []Target{
		FTPTarget{Host: host, Port: p, User: "a", Password: "b", Timeout: time.Second},
		SFTPTarget{Host: host, Port: p, User: "a", Password: "b", HostKey: "SHA256:x", Timeout: time.Second},
	} {
		if err := tg.Put(ctx, "x.csv", []byte("a")); err == nil {
			t.Errorf("%s: Fehler erwartet", tg.Label())
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("hing %v", d)
	}
}

// ── Nachlegen verpasster Dateien ────────────────────────────

type flakyTarget struct {
	failRuns int // so viele Zustellversuche scheitern
	calls    int
	put      []string
}

func (f *flakyTarget) Label() string { return "Test" }
func (f *flakyTarget) Put(_ context.Context, name string, _ []byte) error {
	if f.calls < f.failRuns {
		return errors.New("Ziel nicht erreichbar")
	}
	f.put = append(f.put, name)
	return nil
}
func (f *flakyTarget) Test(context.Context) (string, error) { return "", nil }

func TestExporterRedeliversMissedFiles(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	m := env.instant(t)
	tg := &flakyTarget{failRuns: 2}
	env.cfg.Target = tg
	env.cfg.FileDays = 0 // eine Datei je Lauf
	putValue(t, env.pool, m, e0, 1)
	for i := 0; i < 3; i++ {
		putValue(t, env.pool, m, e0.Add(time.Duration(i+1)*15*time.Minute), float64(i+2))
		_, err := env.e.Run(ctx, env.cfg)
		if i < 2 && err == nil {
			t.Fatalf("Lauf %d: Fehler erwartet", i+1)
		}
		if i == 2 && err != nil {
			t.Fatalf("Lauf 3: %v", err)
		}
		tg.calls = i + 1
		env.now = env.now.Add(15 * time.Minute)
	}
	files := env.files(t)
	if len(files) != 3 || len(tg.put) != 3 {
		t.Fatalf("Dateien %d, abgelegt %v", len(files), tg.put)
	}
	for i := 1; i < 3; i++ {
		if tg.put[i-1] >= tg.put[i] {
			t.Errorf("Reihenfolge: %v", tg.put)
		}
	}
	var open int
	must(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM energy_export_files
		WHERE connection_id = $1::uuid AND delivered_version <> version`, env.conn).Scan(&open))
	if open != 0 {
		t.Errorf("%d Dateien nicht zugestellt", open)
	}
	rows, err := env.pool.Query(ctx, `SELECT result FROM energy_export_runs
		WHERE connection_id = $1::uuid ORDER BY started_at, id`, env.conn)
	must(t, err)
	defer rows.Close()
	var results []string
	for rows.Next() {
		var r string
		must(t, rows.Scan(&r))
		results = append(results, r)
	}
	if strings.Join(results, ",") != "error,error,ok" {
		t.Errorf("Protokoll %v", results)
	}
}

func TestDirTargetHangingWriterIsSingleFlight(t *testing.T) {
	dir := t.TempDir()
	tg := DirTarget{Dir: dir}
	release := make(chan struct{})
	lateLive := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Ein Schreibvorgang hängt über das Zeitlimit hinaus.
	err := within(ctx, dir, func(live func() bool) error {
		<-release
		lateLive <- live()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "Zeitlimit") {
		t.Fatalf("Zeitlimit erwartet, war %v", err)
	}
	// Solange er hängt, werden Put und Test sofort abgewiesen.
	start := time.Now()
	if err := tg.Put(context.Background(), "x.csv", []byte("a")); !errors.Is(err, ErrDirBusy) {
		t.Fatalf("ErrDirBusy erwartet, war %v", err)
	}
	if _, err := tg.Test(context.Background()); !errors.Is(err, ErrDirBusy) {
		t.Fatalf("Test: ErrDirBusy erwartet, war %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("Abweisung nicht sofort")
	}
	close(release)
	if <-lateLive {
		t.Error("der späte Schreiber gilt noch als aktuell und dürfte umbenennen")
	}
	// Danach funktioniert der nächste Lauf wieder.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := tg.Put(context.Background(), "x.csv", []byte("neu"))
		if err == nil {
			break
		}
		if !errors.Is(err, ErrDirBusy) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x.csv")); string(b) != "neu" {
		t.Errorf("Inhalt %q", b)
	}
}

func TestDirTargetExpiredAttemptDoesNotRename(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Versuch schon abgelaufen
	err := DirTarget{Dir: dir}.write("x.csv", []byte("alt"), func() bool { return ctx.Err() == nil })
	if err == nil {
		t.Fatal("Fehler erwartet")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("weder Datei noch Temp-Datei darf bleiben: %v", entries)
	}
}
