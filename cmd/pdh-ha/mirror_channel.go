package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kanal der Datei-Spiegelung: eigene mTLS-Verbindung auf Port 7480 (ALPN
// "pdh-mirror/1"), über die rsync zwischen den Servern läuft. Auf der Leitung
// steht erst eine JSON-Zeile mirrorRequest, dann eine Antwortzeile
// {"ok":true} oder {"error":"…"}, danach der rohe rsync-Datenstrom.

const (
	mirrorFirstLine  = 10 * time.Second // Frist nur für die erste Zeile und die Antwort
	mirrorStderrMax  = 300              // so viele Bytes der rsync-Fehlerausgabe gehen ins Protokoll
	mirrorStderrKeep = 4096             // so viel Fehlerausgabe wird überhaupt gemerkt
	mirrorChildWait  = 3 * time.Second  // Nachlauf, bis Ausgabekanäle eines abgebrochenen Kindes zu sind
)

// mirrorRequest ist die erste Zeile des Absenders. Argv sind die Wörter des
// rsync-Aufrufs nach dem Rechnernamen („rsync --server …“).
type mirrorRequest struct {
	Label string   `json:"label"`
	Term  uint64   `json:"term"`
	Argv  []string `json:"argv"`
}

type mirrorReply struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// mirrorReceiver entscheidet auf dem Empfänger über einen Aufruf. Accept
// bekommt die Kennung und Art des Absenders aus dem geprüften Zertifikat (nie
// aus der Anfrage) und liefert entweder die auszuführenden Argumente (argv[0]
// ist das Programm) oder einen Ablehnungsgrund. done wird genau einmal nach dem
// Ende des Prozesses aufgerufen (nil bei Erfolg); nur dort steht die gekürzte
// Fehlerausgabe von rsync, zum Protokollieren – sie geht nie an den Absender.
type mirrorReceiver interface {
	Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) (argv []string, done func(error), refuse string)
}

// readMirrorLine liest eine Zeile bis maxLine Bytes aus br.
func readMirrorLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, bufio.ErrTooLong
	}
	if err != nil {
		return nil, err
	}
	return line, nil
}

// serveMirror bedient eine angenommene Verbindung mit ALPN pdh-mirror/1.
// Der Handshake-Platz ist schon frei (handleConn); der lange Datenstrom blockiert
// ihn nicht. Ohne gültiges Zertifikat, bei Zeugen oder ohne Empfänger wird nur
// geschlossen, Accept läuft dann nie.
func (t *transport) serveMirror(ctx context.Context, c *tls.Conn, raw net.Conn) {
	defer raw.Close()
	id, _, kind, err := t.identifyKind(c.ConnectionState())
	if err != nil {
		t.reject(raw.RemoteAddr())
		return
	}
	rcv := t.mirrorRecv
	if kind != "node" || rcv == nil {
		return
	}

	// Frist nur für die erste Zeile
	raw.SetReadDeadline(time.Now().Add(mirrorFirstLine))
	br := bufio.NewReaderSize(c, maxLine+1)
	line, err := readMirrorLine(br)
	if err != nil {
		return
	}
	var req mirrorRequest
	if json.Unmarshal(line, &req) != nil {
		return
	}
	raw.SetReadDeadline(time.Time{})

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	argv, done, refuse := rcv.Accept(lctx, id, kind, req)
	reply := func(r mirrorReply) bool {
		b, _ := json.Marshal(r)
		raw.SetWriteDeadline(time.Now().Add(mirrorFirstLine))
		_, err := c.Write(append(b, '\n'))
		raw.SetWriteDeadline(time.Time{})
		return err == nil
	}
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	if refuse != "" {
		reply(mirrorReply{Error: refuse})
		return
	}
	if len(argv) == 0 {
		reply(mirrorReply{Error: "Kein Aufruf zum Ausführen"})
		return
	}

	cmd := exec.CommandContext(lctx, argv[0], argv[1:]...)
	hookProcGroup(cmd)
	cmd.WaitDelay = mirrorChildWait
	errBuf := &mirrorStderr{}
	cmd.Stderr = errBuf // nur fürs Protokoll, nie an die Gegenstelle
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		reply(mirrorReply{Error: "Prozess nicht startbar"})
		finish(err)
		return
	}
	if err := cmd.Start(); err != nil {
		reply(mirrorReply{Error: "Prozess nicht startbar"})
		finish(fmt.Errorf("Start: %w", err))
		return
	}
	if !reply(mirrorReply{OK: true}) {
		cancel()
		cmd.Wait()
		hookKillGroup(cmd)
		finish(errors.New("Antwort an die Gegenstelle nicht zustellbar"))
		return
	}

	// Abbruch (Kontext oder Fehler) schließt die Verbindung, damit beide
	// Kopierschleifen enden; der Kontext beendet das Kind.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-lctx.Done():
			raw.Close()
		case <-stop:
		}
	}()
	// Gegenstelle -> Kind
	var inErr error
	var exited atomic.Bool // Kind ist fertig: ein Fehler beim Lesen danach zählt nicht
	wg.Add(1)
	go func() {
		defer wg.Done()
		var werr bool
		_, e := io.Copy(writeFlag{stdin, &werr}, br)
		stdin.Close()
		if e != nil && !werr && lctx.Err() == nil && !exited.Load() {
			inErr = e
			cancel() // Verbindung gestört: Kind beenden
		}
	}()
	// Kind -> Gegenstelle
	_, outErr := io.Copy(c, stdout)
	if outErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	exited.Store(true)
	hookKillGroup(cmd)

	// Kind ist fertig: Gegenstelle bekommt close_notify und kurz Zeit, ihre Seite
	// zu schließen (kein RST über ungelesene Daten); dann endet auch Kopierer 1.
	if lctx.Err() == nil {
		c.CloseWrite()
		raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	}
	close(stop)
	if lctx.Err() != nil {
		raw.Close()
	}
	// Leser beenden, falls die Gegenstelle nicht schließt
	wg.Wait()
	raw.Close()

	var res error
	switch {
	case waitErr != nil:
		res = fmt.Errorf("rsync endete mit Fehler: %v: %s", waitErr, errBuf.snippet())
	case outErr != nil:
		res = fmt.Errorf("Senden an die Gegenstelle: %w", outErr)
	case inErr != nil:
		res = fmt.Errorf("Lesen von der Gegenstelle: %w", inErr)
	case ctx.Err() != nil:
		res = errors.New("abgebrochen")
	}
	finish(res)
}

// writeFlag merkt sich, dass ein Fehler vom Schreiben kam (Kind hat die Eingabe
// geschlossen), nicht vom Lesen der Verbindung.
type writeFlag struct {
	w      io.Writer
	failed *bool
}

func (f writeFlag) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		*f.failed = true
	}
	return n, err
}

// mirrorStderr merkt sich den Anfang der Fehlerausgabe des Kindes.
type mirrorStderr struct {
	mu  sync.Mutex
	buf []byte
}

func (m *mirrorStderr) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if room := mirrorStderrKeep - len(m.buf); room > 0 {
		m.buf = append(m.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (m *mirrorStderr) snippet() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return mirrorStderrLine(m.buf)
}

// mirrorStderrLine macht aus der Fehlerausgabe eine Zeile von höchstens
// mirrorStderrMax Bytes: Zeilenumbrüche werden Leerzeichen, andere
// Steuerzeichen entfallen.
func mirrorStderrLine(b []byte) string {
	s := strings.ToValidUTF8(string(b), "?")
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			sb.WriteByte(' ')
		case unicode.IsControl(r):
		default:
			sb.WriteRune(r)
		}
	}
	out := strings.TrimSpace(sb.String())
	if len(out) > mirrorStderrMax {
		cut := mirrorStderrMax
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

// --- Absender: pdh-ha mirror-pipe (Ersatz für ssh bei rsync -e) ---

// runMirrorPipe wird von rsync wie ssh aufgerufen:
// pdh-ha mirror-pipe --label L --term N --node ID <host> rsync --server …
// Der Rechnername wird nicht benutzt; verbunden wird mit den Adressen des
// Knotens ID aus cluster.json. Rückgabe 1 mit Meldung nach errw bei Ablehnung.
func runMirrorPipe(args []string, in io.Reader, out, errw io.Writer) int {
	return runMirrorPipeWith(newStore(), args, in, out, errw)
}

func runMirrorPipeWith(st store, args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("pdh-ha mirror-pipe", flag.ContinueOnError)
	fs.SetOutput(errw)
	label := fs.String("label", "", "Quelle")
	term := fs.Uint64("term", 0, "Amtszeit des Absenders")
	node := fs.String("node", "", "Kennung der Gegenstelle")
	if fs.Parse(args) != nil {
		return 2
	}
	rest := fs.Args() // <host> rsync --server …
	if *node == "" || len(rest) < 2 {
		fmt.Fprintln(errw, "pdh-ha mirror-pipe: --node und ein Aufruf nach dem Rechnernamen nötig")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(errw, "pdh-ha mirror-pipe:", err)
		return 1
	}
	c, err := dialMirror(st, *node)
	if err != nil {
		return fail(err)
	}
	defer c.raw.Close()
	br := bufio.NewReaderSize(c.tls, maxLine+1)

	b, _ := json.Marshal(mirrorRequest{Label: *label, Term: *term, Argv: rest[1:]})
	c.raw.SetWriteDeadline(time.Now().Add(mirrorFirstLine))
	if _, err := c.tls.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	c.raw.SetWriteDeadline(time.Time{})
	c.raw.SetReadDeadline(time.Now().Add(mirrorFirstLine))
	line, err := readMirrorLine(br)
	if err != nil {
		return fail(fmt.Errorf("keine Antwort der Gegenstelle: %w", err))
	}
	c.raw.SetReadDeadline(time.Time{})
	var rep mirrorReply
	if json.Unmarshal(line, &rep) != nil {
		return fail(errors.New("Antwort der Gegenstelle ist unlesbar"))
	}
	if !rep.OK {
		if rep.Error == "" {
			rep.Error = "abgelehnt"
		}
		return fail(errors.New(rep.Error))
	}

	// rsync -> Gegenstelle; Ende der Eingabe heißt: Schreibseite schließen
	go func() {
		io.Copy(c.tls, in)
		c.tls.CloseWrite()
	}()
	if _, err := io.Copy(out, br); err != nil {
		return fail(err)
	}
	return 0
}

type mirrorConn struct {
	tls *tls.Conn
	raw net.Conn
}

// dialMirror verbindet zu einer Adresse des Knotens id (Zertifikate aus der
// Ablage wie pdh-ha selbst) und nimmt die erste, die antwortet.
func dialMirror(st store, id string) (*mirrorConn, error) {
	cl, err := st.loadCluster()
	if err != nil {
		return nil, fmt.Errorf("Nicht im Ausfallschutz eingerichtet: %w", err)
	}
	var peer *clusterNode
	for i := range cl.Nodes {
		if cl.Nodes[i].ID == id {
			peer = &cl.Nodes[i]
		}
	}
	if peer == nil || peer.Kind != "node" || len(peer.Addrs) == 0 {
		return nil, fmt.Errorf("Gegenstelle %q ist kein Server mit Adresse", id)
	}
	caPEM, err1 := os.ReadFile(st.etcPath("ca.crt"))
	certPEM, err2 := os.ReadFile(st.etcPath("node.crt"))
	keyPEM, err3 := os.ReadFile(st.etcPath("node.key"))
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	cert, err := loadTLSCert(certPEM, keyPEM, caPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca.crt ist unlesbar")
	}
	var last error
	for _, addr := range peer.Addrs {
		d := net.Dialer{Timeout: 5 * time.Second}
		raw, err := d.Dial("tcp", addr)
		if err != nil {
			last = err
			continue
		}
		c := tls.Client(raw, &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
			ServerName:   id,
			NextProtos:   []string{alpnMirror},
			MinVersion:   tls.VersionTLS13,
		})
		hctx, cancel := context.WithTimeout(context.Background(), handshakeMax)
		err = c.HandshakeContext(hctx)
		cancel()
		if err == nil && c.ConnectionState().NegotiatedProtocol != alpnMirror {
			err = errors.New("Gegenstelle spricht kein " + alpnMirror)
		}
		if err != nil {
			raw.Close()
			last = err
			continue
		}
		return &mirrorConn{tls: c, raw: raw}, nil
	}
	return nil, fmt.Errorf("Gegenstelle %s nicht erreichbar: %v", id, last)
}
