package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRecv ist die Attrappe des Empfängers (der echte kommt mit Teil 6, Aufgabe 6).
type fakeRecv struct {
	mu     sync.Mutex
	calls  []mirrorAcceptCall
	argv   []string
	refuse string
	done   chan error
}

type mirrorAcceptCall struct {
	peerID, peerKind string
	req              mirrorRequest
}

func newFakeRecv() *fakeRecv {
	return &fakeRecv{
		argv: []string{os.Args[0], "-test.run=^TestMirrorHelperProcess$"},
		done: make(chan error, 4),
	}
}

func (f *fakeRecv) Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) ([]string, func(error), string) {
	f.mu.Lock()
	f.calls = append(f.calls, mirrorAcceptCall{peerID, peerKind, req})
	f.mu.Unlock()
	if f.refuse != "" {
		return nil, nil, f.refuse
	}
	return f.argv, func(err error) { f.done <- err }, ""
}

func (f *fakeRecv) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestMirrorHelperProcess ist der Hilfsprozess, der rsync --server vertritt.
// Betriebsart aus PDH_TEST_MIRROR: echo (Eingabe zurückgeben), fail (wie echo,
// schreibt ein Geheimnis nach stderr und endet mit 1), hang (wartet ewig).
func TestMirrorHelperProcess(t *testing.T) {
	mode := os.Getenv("PDH_TEST_MIRROR")
	if mode == "" {
		t.Skip("nur als Hilfsprozess")
	}
	switch mode {
	case "echo":
		io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "fail":
		os.Stderr.WriteString("rsync: Zeile aus fremder Datei: GEHEIM-INHALT\nzweite Zeile\n")
		io.Copy(os.Stdout, os.Stdin)
		os.Exit(1)
	case "hang":
		os.Stdout.WriteString("los\n")
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(3)
}

type mirrorEnv struct {
	ca     testCA
	ln     net.Listener
	srv    *transport
	recv   mirrorReceiver
	st     store // Ablage des Clients
	cancel context.CancelFunc
	ended  chan struct{}
}

// newMirrorEnv startet den Empfänger-Transport "b" und legt die Ablage des
// Clients (Kennung clientID, Art clientKind) an.
func newMirrorEnv(t *testing.T, mode, clientID, clientKind string, recv mirrorReceiver) *mirrorEnv {
	t.Helper()
	t.Setenv("PDH_TEST_MIRROR", mode)
	ca := newTestCA(t, "c1")
	ln := listenLocal(t)
	certB, _ := ca.tlsCert(t, "b", "node")
	cl := clusterConfig{ClusterID: "c1", Version: 1, Settings: testSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"127.0.0.1:1"}},
		{ID: "b", Kind: "node", Addrs: []string{ln.Addr().String()}},
		{ID: "w", Kind: "witness", Addrs: []string{"127.0.0.1:1"}},
	}}
	st := store{etc: t.TempDir(), state: t.TempDir()}
	certPEM, keyPEM := issueNode(t, ca.cert, ca.key, clientID, clientKind)
	for name, b := range map[string][]byte{"ca.crt": ca.cert, "node.crt": certPEM, "node.key": keyPEM} {
		if err := st.writeEtc(name, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	tb := newTransport(nodeConfig{ID: "b", Kind: "node"}, cl, certB, ca.pool, testMono(), testEvents(t))
	if recv != nil {
		tb.mirrorRecv = recv
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &mirrorEnv{ca: ca, ln: ln, srv: tb, recv: recv, st: st, cancel: cancel, ended: make(chan struct{})}
	go func() { tb.serve(ctx, ln, nil); close(e.ended) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-e.ended:
		case <-time.After(10 * time.Second):
			t.Error("serve endete nicht")
		}
	})
	return e
}

func (e *mirrorEnv) pipeArgs() []string {
	return []string{"--label", "L", "--term", "3", "--node", "b", "host", "rsync", "--server", "-a", ".", "x"}
}

func doneWithin(t *testing.T, f *fakeRecv) error {
	t.Helper()
	select {
	case err := <-f.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("done nicht aufgerufen")
		return nil
	}
}

func TestMirrorChannelRefused(t *testing.T) {
	f := newFakeRecv()
	f.refuse = "Spiegelung ist aus"
	e := newMirrorEnv(t, "echo", "a", "node", f)
	var out, errw bytes.Buffer
	if code := runMirrorPipeWith(e.st, e.pipeArgs(), strings.NewReader("x"), &out, &errw); code != 1 {
		t.Fatalf("Rückgabe %d, erwartet 1", code)
	}
	if !strings.Contains(errw.String(), "Spiegelung ist aus") {
		t.Fatalf("stderr: %q", errw.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout sollte leer sein: %q", out.String())
	}
	if f.callCount() != 1 {
		t.Fatalf("Accept %d-mal", f.callCount())
	}
	c := f.calls[0]
	if c.peerID != "a" || c.peerKind != "node" || c.req.Label != "L" || c.req.Term != 3 ||
		strings.Join(c.req.Argv, " ") != "rsync --server -a . x" {
		t.Fatalf("Aufruf: %+v", c)
	}
	select {
	case <-f.done:
		t.Fatal("done darf bei Ablehnung nicht laufen")
	default:
	}
}

func TestMirrorChannelPipes(t *testing.T) {
	f := newFakeRecv()
	e := newMirrorEnv(t, "echo", "a", "node", f)
	var out, errw bytes.Buffer
	in := strings.NewReader(strings.Repeat("daten\n", 20000)) // mehr als ein TLS-Datensatz
	if code := runMirrorPipeWith(e.st, e.pipeArgs(), in, &out, &errw); code != 0 {
		t.Fatalf("Rückgabe %d: %s", code, errw.String())
	}
	if out.String() != strings.Repeat("daten\n", 20000) {
		t.Fatalf("Ausgabe weicht ab (%d Bytes)", out.Len())
	}
	if err := doneWithin(t, f); err != nil {
		t.Fatalf("done: %v", err)
	}
}

// Ruling R7: die Fehlerausgabe von rsync geht nie an den Absender, nur an done.
func TestMirrorChannelStderrStaysLocal(t *testing.T) {
	f := newFakeRecv()
	e := newMirrorEnv(t, "fail", "a", "node", f)
	var out, errw bytes.Buffer
	runMirrorPipeWith(e.st, e.pipeArgs(), strings.NewReader("nutzdaten"), &out, &errw)
	if strings.Contains(out.String()+errw.String(), "GEHEIM") || strings.Contains(out.String()+errw.String(), "fremder Datei") {
		t.Fatalf("stderr des Kindes beim Absender: out=%q err=%q", out.String(), errw.String())
	}
	if out.String() != "nutzdaten" {
		t.Fatalf("Ausgabe: %q", out.String())
	}
	err := doneWithin(t, f)
	if err == nil || !strings.Contains(err.Error(), "GEHEIM-INHALT zweite Zeile") || strings.ContainsAny(err.Error(), "\r\n") {
		t.Fatalf("done sollte die Fehlerausgabe in einer Zeile tragen: %v", err)
	}
}

func TestMirrorStderrLine(t *testing.T) {
	got := mirrorStderrLine([]byte("a\r\nb\tc\x00\x1b[31md\x07"))
	if got != "a  b c[31md" {
		t.Fatalf("Steuerzeichen: %q", got)
	}
	long := mirrorStderrLine([]byte(strings.Repeat("ä", 400)))
	if len(long) > mirrorStderrMax || strings.ContainsRune(long, '�') {
		t.Fatalf("Länge %d: %q", len(long), long)
	}
	if got := mirrorStderrLine([]byte("\xff\xfeok")); got != "?ok" {
		t.Fatalf("ungültiges UTF-8: %q", got)
	}
}

func TestMirrorChannelWitnessRejected(t *testing.T) {
	f := newFakeRecv()
	e := newMirrorEnv(t, "echo", "w", "witness", f)
	var out, errw bytes.Buffer
	if code := runMirrorPipeWith(e.st, e.pipeArgs(), strings.NewReader("x"), &out, &errw); code == 0 {
		t.Fatal("Zeuge darf nicht durchkommen")
	}
	if f.callCount() != 0 {
		t.Fatal("Accept darf für einen Zeugen nie laufen")
	}
}

func TestMirrorChannelNoCert(t *testing.T) {
	f := newFakeRecv()
	e := newMirrorEnv(t, "echo", "a", "node", f)
	raw, err := net.Dial("tcp", e.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	c := tls.Client(raw, &tls.Config{RootCAs: e.ca.pool, ServerName: "b", NextProtos: []string{alpnMirror}, MinVersion: tls.VersionTLS13})
	if err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"label":"L","term":3,"argv":["rsync","--server"]}` + "\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 64)); err == nil || n != 0 {
		t.Fatalf("Verbindung sollte geschlossen sein, gelesen %d Bytes", n)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("Verbindung wurde nicht geschlossen")
	}
	if f.callCount() != 0 {
		t.Fatal("Accept ohne Zertifikat aufgerufen")
	}
}

func TestMirrorChannelNoReceiver(t *testing.T) {
	e := newMirrorEnv(t, "echo", "a", "node", nil)
	var out, errw bytes.Buffer
	if code := runMirrorPipeWith(e.st, e.pipeArgs(), strings.NewReader("x"), &out, &errw); code == 0 {
		t.Fatal("ohne Empfänger muss die Verbindung scheitern")
	}
}

// Ein Abbruch (Kontext) beendet Kind und beide Kopierschleifen.
func TestMirrorChannelCancelStopsChild(t *testing.T) {
	f := newFakeRecv()
	e := newMirrorEnv(t, "hang", "a", "node", f)
	pr, pw := io.Pipe()
	defer pw.Close()
	res := make(chan int, 1)
	go func() {
		var out, errw bytes.Buffer
		res <- runMirrorPipeWith(e.st, e.pipeArgs(), pr, &out, &errw)
	}()
	waitFor(t, 10*time.Second, "Kind gestartet", func() bool { return f.callCount() == 1 })
	time.Sleep(200 * time.Millisecond)
	e.cancel()
	err := doneWithin(t, f) // done läuft erst nach cmd.Wait: das Kind ist weg
	if err == nil {
		t.Fatal("abgebrochener Aufruf sollte einen Fehler melden")
	}
	select {
	case <-e.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("serve endete nicht, Kopierer hängen")
	}
	select {
	case <-res:
	case <-time.After(10 * time.Second):
		t.Fatal("Client wurde nicht freigegeben")
	}
}

// Ein laufender Datenstrom stört den Heartbeat nicht (und umgekehrt).
func TestMirrorHeartbeatUnaffected(t *testing.T) {
	t.Setenv("PDH_TEST_MIRROR", "echo")
	ca := newTestCA(t, "c1")
	certA, _ := ca.tlsCert(t, "a", "node")
	certB, _ := ca.tlsCert(t, "b", "node")
	lnA, lnB := listenLocal(t), listenLocal(t)
	cl := clusterConfig{ClusterID: "c1", Version: 1, Settings: testSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{lnA.Addr().String()}},
		{ID: "b", Kind: "node", Addrs: []string{lnB.Addr().String()}},
	}}
	ta := newTransport(nodeConfig{ID: "a", Kind: "node", Addrs: []string{lnA.Addr().String()}}, cl, certA, ca.pool, testMono(), testEvents(t))
	tb := newTransport(nodeConfig{ID: "b", Kind: "node", Addrs: []string{lnB.Addr().String()}}, cl, certB, ca.pool, testMono(), testEvents(t))
	f := newFakeRecv()
	tb.mirrorRecv = f
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ta.serve(ctx, lnA, nil)
	go tb.serve(ctx, lnB, nil)
	go beat(ctx, ta, "a")
	go beat(ctx, tb, "b")
	waitFor(t, 5*time.Second, "Heartbeat lebendig", func() bool {
		return pathAlive(ta.peers(testTick)["b"], lnB.Addr().String()) && pathAlive(tb.peers(testTick)["a"], lnB.Addr().String())
	})

	// Ablage des Clients a
	st := store{etc: t.TempDir(), state: t.TempDir()}
	certPEM, keyPEM := issueNode(t, ca.cert, ca.key, "a", "node")
	for name, b := range map[string][]byte{"ca.crt": ca.cert, "node.crt": certPEM, "node.key": keyPEM} {
		if err := st.writeEtc(name, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	outR, outW := io.Pipe()
	res := make(chan int, 1)
	go func() {
		var errw bytes.Buffer
		res <- runMirrorPipeWith(st, []string{"--label", "L", "--node", "b", "h", "rsync", "--server"}, pr, outW, &errw)
		outW.Close()
	}()
	buf := make([]byte, 5)
	pw.Write([]byte("hallo"))
	if _, err := io.ReadFull(outR, buf); err != nil || string(buf) != "hallo" {
		t.Fatalf("Datenstrom: %q %v", buf, err)
	}
	// Strom bleibt offen: mehrere Takte lang bleiben die Heartbeats lebendig
	end := time.Now().Add(6 * testTick)
	for time.Now().Before(end) {
		if !pathAlive(ta.peers(testTick)["b"], lnB.Addr().String()) || !pathAlive(tb.peers(testTick)["a"], lnB.Addr().String()) {
			t.Fatal("Heartbeat ausgefallen, während der Datenstrom läuft")
		}
		time.Sleep(testTick / 2)
	}
	pw.Close()
	if _, err := io.ReadAll(outR); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if code := <-res; code != 0 {
		t.Fatalf("Rückgabe %d", code)
	}
	if err := doneWithin(t, f); err != nil {
		t.Fatalf("done: %v", err)
	}
}
