package energie

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// putLimit ist das feste Zeitlimit für eine Zustellung. Sie läuft unter einer
// Advisory-Sperre in einer offenen Transaktion, ein hängendes Ziel darf den
// Export deshalb nie dauerhaft blockieren.
const putLimit = 60 * time.Second

const (
	defaultTargetTimeout = 30 * time.Second
	testFileName         = ".pdh-test.tmp"
)

var (
	// ErrHostKeyMismatch: der Server-Schlüssel passt nicht zum hinterlegten Fingerabdruck.
	ErrHostKeyMismatch = errors.New("der Server-Schlüssel stimmt nicht mit dem hinterlegten Fingerabdruck überein")
	// ErrHostKeyMissing: es ist noch kein Fingerabdruck des Servers bestätigt.
	ErrHostKeyMissing = errors.New("der Fingerabdruck des Servers ist noch nicht bestätigt")
)

// HostKeyFingerprint liefert den Fingerabdruck "SHA256:…" wie ssh-keygen.
func HostKeyFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func checkFileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("ungültiger Dateiname %q", name)
	}
	return nil
}

func timeoutOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultTargetTimeout
	}
	return d
}

// limited gibt dem Kontext das feste Zeitlimit (nie länger als putLimit).
func limited(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, putLimit)
}

// ── Netzlaufwerk / Ordner ───────────────────────────────────

// DirTarget legt die Dateien in einem Ordner ab, auch auf einem im PDH
// eingebundenen Netzlaufwerk. Der Ordner wird nie angelegt: fehlt er (zum
// Beispiel weil das Laufwerk nicht eingebunden ist), ist das ein Fehler.
type DirTarget struct{ Dir string }

func (d DirTarget) Label() string { return "Ordner " + d.Dir }

func (d DirTarget) check() error {
	if strings.TrimSpace(d.Dir) == "" {
		return errors.New("kein Ordner eingetragen")
	}
	st, err := os.Stat(d.Dir)
	if err != nil {
		return fmt.Errorf("Ordner nicht erreichbar: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s ist kein Ordner", d.Dir)
	}
	return nil
}

// dirBusy merkt sich je Ordner, ob noch ein Schreibvorgang läuft (auch einer,
// dessen Zeitlimit schon abgelaufen ist). So überholt nie ein später Schreiber
// einen neueren Stand.
var dirBusy sync.Map // Ordner -> *atomic.Bool

// ErrDirBusy: ein früherer Schreibvorgang auf dem Ordner hängt noch.
var ErrDirBusy = errors.New("Netzlaufwerk antwortet nicht (ein früherer Schreibvorgang hängt noch)")

// within führt f aus und gibt nach Ablauf des Kontexts auf (ein hängendes
// Netzlaufwerk lässt sich nicht abbrechen, der Lauf wartet aber nicht mehr).
// Je Ordner läuft höchstens ein Vorgang; f erhält live, das nach dem
// Zeitlimit false liefert und dann nichts mehr umbenennen oder löschen darf.
func within(ctx context.Context, dir string, f func(live func() bool) error) error {
	v, _ := dirBusy.LoadOrStore(filepath.Clean(dir), new(atomic.Bool))
	busy := v.(*atomic.Bool)
	if !busy.CompareAndSwap(false, true) {
		return ErrDirBusy
	}
	done := make(chan error, 1)
	go func() {
		defer busy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("interner Fehler: %v", r)
			}
		}()
		done <- f(func() bool { return ctx.Err() == nil })
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("Zeitlimit überschritten: %w", ctx.Err())
	}
}

func (d DirTarget) Put(ctx context.Context, name string, content []byte) error {
	if err := checkFileName(name); err != nil {
		return err
	}
	ctx, cancel := limited(ctx)
	defer cancel()
	return within(ctx, d.Dir, func(live func() bool) error { return d.write(name, content, live) })
}

func (d DirTarget) write(name string, content []byte, live func() bool) error {
	if err := d.check(); err != nil {
		return err
	}
	final := filepath.Join(d.Dir, name)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(d.Dir, fmt.Sprintf(".%s.%x.tmp", name, rnd))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !live() {
		err = errors.New("Zeitlimit überschritten, Datei nicht mehr abgelegt")
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (d DirTarget) Test(ctx context.Context) (string, error) {
	ctx, cancel := limited(ctx)
	defer cancel()
	err := within(ctx, d.Dir, func(live func() bool) error {
		if err := d.check(); err != nil {
			return err
		}
		if err := d.write(testFileName, []byte("PDH"), live); err != nil {
			return err
		}
		return os.Remove(filepath.Join(d.Dir, testFileName))
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Verbindung in Ordnung (Ordner %s)", d.Dir), nil
}

// ── FTP ─────────────────────────────────────────────────────

// FTPTarget legt die Dateien per FTP (optional mit explizitem TLS) ab.
type FTPTarget struct {
	Host        string
	Port        int
	User        string
	Password    string
	Folder      string
	ExplicitTLS bool
	Timeout     time.Duration
}

func (t FTPTarget) Label() string {
	return "FTP " + net.JoinHostPort(t.Host, strconv.Itoa(t.port()))
}

func (t FTPTarget) port() int {
	if t.Port <= 0 {
		return 21
	}
	return t.Port
}

// connect baut die Verbindung auf. Jede Verbindung (Steuerung und Daten)
// bekommt die Frist des Kontexts; so hängt auch ein stehender Server nicht ewig.
func (t FTPTarget) connect(ctx context.Context) (*ftp.ServerConn, error) {
	if strings.TrimSpace(t.Host) == "" {
		return nil, errors.New("kein Server eingetragen")
	}
	timeout := timeoutOrDefault(t.Timeout)
	deadline, _ := ctx.Deadline()
	dialer := net.Dialer{Timeout: timeout}
	tlsCfg := &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12}
	opts := []ftp.DialOption{ftp.DialWithShutTimeout(5 * time.Second)}
	first := true
	opts = append(opts, ftp.DialWithDialFunc(func(network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(deadline)
		isControl := first
		first = false
		if t.ExplicitTLS {
			if isControl {
				return conn, nil // die Bibliothek schaltet nach AUTH TLS selbst um
			}
			return tls.Client(conn, tlsCfg), nil
		}
		return conn, nil
	}))
	if t.ExplicitTLS {
		opts = append(opts, ftp.DialWithExplicitTLS(tlsCfg))
	}
	c, err := ftp.Dial(net.JoinHostPort(t.Host, strconv.Itoa(t.port())), opts...)
	if err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	if err := c.Login(t.User, t.Password); err != nil {
		_ = c.Quit()
		return nil, fmt.Errorf("Anmeldung fehlgeschlagen: %w", err)
	}
	if f := strings.TrimSpace(t.Folder); f != "" && f != "/" {
		if err := c.ChangeDir(f); err != nil {
			_ = c.Quit()
			return nil, fmt.Errorf("Ordner %s nicht erreichbar: %w", f, err)
		}
	}
	return c, nil
}

func (t FTPTarget) Put(ctx context.Context, name string, content []byte) error {
	if err := checkFileName(name); err != nil {
		return err
	}
	ctx, cancel := limited(ctx)
	defer cancel()
	c, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Quit()
	return ftpPut(c, name, content)
}

func ftpPut(c *ftp.ServerConn, name string, content []byte) error {
	tmp := "." + name + ".tmp"
	if err := c.Stor(tmp, bytes.NewReader(content)); err != nil {
		_ = c.Delete(tmp)
		return fmt.Errorf("Hochladen fehlgeschlagen: %w", err)
	}
	if err := c.Rename(tmp, name); err != nil {
		// Manche Server überschreiben beim Umbenennen nicht: alte Datei löschen, dann erneut.
		_ = c.Delete(name)
		if err2 := c.Rename(tmp, name); err2 != nil {
			_ = c.Delete(tmp)
			return fmt.Errorf("Umbenennen fehlgeschlagen: %w", err2)
		}
	}
	return nil
}

func (t FTPTarget) Test(ctx context.Context) (string, error) {
	ctx, cancel := limited(ctx)
	defer cancel()
	c, err := t.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Quit()
	if err := c.Stor(testFileName, bytes.NewReader([]byte("PDH"))); err != nil {
		return "", fmt.Errorf("Schreiben im Ordner fehlgeschlagen: %w", err)
	}
	if err := c.Delete(testFileName); err != nil {
		return "", fmt.Errorf("Testdatei konnte nicht gelöscht werden: %w", err)
	}
	return fmt.Sprintf("Verbindung in Ordnung (Ordner %s)", folderLabel(t.Folder)), nil
}

func folderLabel(f string) string {
	if strings.TrimSpace(f) == "" {
		return "/"
	}
	return f
}

// ── SFTP ────────────────────────────────────────────────────

// SFTPTarget legt die Dateien per SFTP ab. Der Server wird nur akzeptiert,
// wenn sein Schlüssel zum bestätigten Fingerabdruck HostKey ("SHA256:…") passt.
type SFTPTarget struct {
	Host     string
	Port     int
	User     string
	Password string
	// PrivateKey: eigener Schlüssel des PDH im OpenSSH-Format. Ist er gesetzt,
	// meldet sich das PDH nur damit an – das Kennwort wird dann nicht gesendet.
	PrivateKey string
	// KeyAuth: Anmeldung mit Schlüssel ist gewählt (auch wenn noch keiner erzeugt ist).
	KeyAuth bool
	Folder  string
	HostKey string
	Timeout time.Duration
}

// ErrSFTPNoKey: Anmeldung mit Schlüssel gewählt, aber noch kein Schlüssel erzeugt.
var ErrSFTPNoKey = errors.New("für die Anmeldung mit Schlüssel ist noch kein Schlüssel erzeugt")

// GenerateSFTPKey erzeugt ein neues Schlüsselpaar (Ed25519) und liefert den
// privaten Schlüssel im OpenSSH-Format.
func GenerateSFTPKey() (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "pdh-efficio")
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(block)), nil
}

// SFTPPublicKey liefert zum privaten Schlüssel die Zeile für authorized_keys
// und den Fingerabdruck ("SHA256:…").
func SFTPPublicKey(private string) (line, fingerprint string, err error) {
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return "", "", errors.New("der gespeicherte Schlüssel ist nicht lesbar – bitte einen neuen erzeugen")
	}
	pub := signer.PublicKey()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " pdh-efficio", HostKeyFingerprint(pub), nil
}

// auth: Anmeldung mit Schlüssel oder Kennwort.
func (t SFTPTarget) auth() ([]ssh.AuthMethod, error) {
	if !t.KeyAuth && t.PrivateKey == "" {
		return []ssh.AuthMethod{ssh.Password(t.Password)}, nil
	}
	if t.PrivateKey == "" {
		return nil, ErrSFTPNoKey
	}
	signer, err := ssh.ParsePrivateKey([]byte(t.PrivateKey))
	if err != nil {
		return nil, errors.New("der gespeicherte Schlüssel ist nicht lesbar – bitte einen neuen erzeugen")
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

func (t SFTPTarget) Label() string {
	return "SFTP " + net.JoinHostPort(t.Host, strconv.Itoa(t.port()))
}

func (t SFTPTarget) port() int {
	if t.Port <= 0 {
		return 22
	}
	return t.Port
}

// connect baut Verbindung und SFTP-Sitzung auf. seen enthält danach den
// Fingerabdruck, den der Server gezeigt hat (auch bei abgelehntem Schlüssel).
func (t SFTPTarget) connect(ctx context.Context) (cl *sftpConn, seen string, err error) {
	if strings.TrimSpace(t.Host) == "" {
		return nil, "", errors.New("kein Server eingetragen")
	}
	want := strings.TrimSpace(t.HostKey)
	auth, aerr := t.auth()
	if aerr != nil {
		return nil, "", aerr
	}
	var hostErr error
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = HostKeyFingerprint(key)
			switch {
			case want == "":
				hostErr = ErrHostKeyMissing
			case want != seen:
				hostErr = ErrHostKeyMismatch
			}
			return hostErr
		},
		Timeout: timeoutOrDefault(t.Timeout),
	}
	dialer := net.Dialer{Timeout: cfg.Timeout}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.port()))
	conn, derr := dialer.DialContext(ctx, "tcp", addr)
	if derr != nil {
		return nil, "", fmt.Errorf("Verbindung fehlgeschlagen: %w", derr)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl) // gilt für die ganze Sitzung
	}
	sc, chans, reqs, cerr := ssh.NewClientConn(conn, addr, cfg)
	if cerr != nil {
		_ = conn.Close()
		if hostErr != nil {
			return nil, seen, hostErr
		}
		return nil, seen, fmt.Errorf("Anmeldung fehlgeschlagen: %w", cerr)
	}
	sshc := ssh.NewClient(sc, chans, reqs)
	client, serr := sftp.NewClient(sshc)
	if serr != nil {
		_ = sshc.Close()
		return nil, seen, fmt.Errorf("SFTP nicht verfügbar: %w", serr)
	}
	return &sftpConn{Client: client, ssh: sshc}, seen, nil
}

// sftpConn hält SFTP-Sitzung und SSH-Verbindung, damit beide geschlossen werden.
type sftpConn struct {
	*sftp.Client
	ssh *ssh.Client
}

func closeSFTP(c *sftpConn) {
	_ = c.Client.Close()
	_ = c.ssh.Close()
}

func (t SFTPTarget) remote(name string) string {
	f := strings.TrimSpace(t.Folder)
	if f == "" {
		return name
	}
	return path.Join(f, name)
}

func (t SFTPTarget) Put(ctx context.Context, name string, content []byte) error {
	if err := checkFileName(name); err != nil {
		return err
	}
	ctx, cancel := limited(ctx)
	defer cancel()
	c, _, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer closeSFTP(c)
	return t.put(c, name, content)
}

func (t SFTPTarget) put(c *sftpConn, name string, content []byte) (err error) {
	final := t.remote(name)
	tmp := t.remote("." + name + ".tmp")
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("Datei anlegen fehlgeschlagen: %w", err)
	}
	if _, err = f.Write(content); err == nil {
		_ = f.Sync() // fsync-Erweiterung ist optional
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = c.Remove(tmp)
		return fmt.Errorf("Hochladen fehlgeschlagen: %w", err)
	}
	if _, ok := c.HasExtension("posix-rename@openssh.com"); ok {
		if err = c.PosixRename(tmp, final); err != nil {
			_ = c.Remove(tmp)
			return fmt.Errorf("Umbenennen fehlgeschlagen: %w", err)
		}
		return nil
	}
	// Ohne die Erweiterung: alte Datei entfernen, dann normal umbenennen.
	_ = c.Remove(final)
	if err = c.Rename(tmp, final); err != nil {
		_ = c.Remove(tmp)
		return fmt.Errorf("Umbenennen fehlgeschlagen: %w", err)
	}
	return nil
}

// Test prüft Anmeldung und Schreibrecht. Fehlt der bestätigte Fingerabdruck,
// nennt die Meldung den gesehenen, damit er bestätigt werden kann.
func (t SFTPTarget) Test(ctx context.Context) (string, error) {
	ctx, cancel := limited(ctx)
	defer cancel()
	c, seen, err := t.connect(ctx)
	if err != nil {
		switch {
		case errors.Is(err, ErrHostKeyMissing):
			return "", fmt.Errorf("%w – Fingerabdruck des Servers: %s", err, seen)
		case errors.Is(err, ErrHostKeyMismatch):
			return "", fmt.Errorf("%w – der Server zeigt jetzt: %s", err, seen)
		}
		return "", err
	}
	defer closeSFTP(c)
	test := t.remote(testFileName)
	f, err := c.OpenFile(test, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", fmt.Errorf("Schreiben im Ordner fehlgeschlagen: %w", err)
	}
	_, werr := f.Write([]byte("PDH"))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = c.Remove(test)
		return "", fmt.Errorf("Schreiben im Ordner fehlgeschlagen: %w", werr)
	}
	if err := c.Remove(test); err != nil {
		return "", fmt.Errorf("Testdatei konnte nicht gelöscht werden: %w", err)
	}
	return fmt.Sprintf("Verbindung in Ordnung (Ordner %s)", folderLabel(t.Folder)), nil
}
