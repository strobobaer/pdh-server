package energie

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ftpFake ist ein kleiner FTP-Server im Prozess: Dateien liegen in dir, die
// Befehle STOR, RNFR/RNTO und DELE werden in Reihenfolge protokolliert.
type ftpFake struct {
	addr string
	dir  string
	mu   sync.Mutex
	log  []string
}

func (f *ftpFake) record(s string) {
	f.mu.Lock()
	f.log = append(f.log, s)
	f.mu.Unlock()
}

func (f *ftpFake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func startFTP(t *testing.T, refuseOverwrite bool) *ftpFake {
	t.Helper()
	f := &ftpFake{dir: t.TempDir()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, refuseOverwrite)
		}
	}()
	return f
}

func (f *ftpFake) serve(c net.Conn, refuseOverwrite bool) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(format string, a ...any) { fmt.Fprintf(c, format+"\r\n", a...) }
	say("220 PDH-Test")
	var pasv net.Listener
	var from string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "FEAT":
			say("211-Features:\r\n EPSV\r\n211 End")
		case "USER":
			say("331 Kennwort")
		case "PASS":
			if arg == "geheim" {
				say("230 ok")
			} else {
				say("530 falsch")
			}
		case "TYPE", "OPTS", "NOOP":
			say("200 ok")
		case "CWD":
			f.record("CWD " + arg)
			say("250 ok")
		case "EPSV", "PASV":
			pasv, _ = net.Listen("tcp", "127.0.0.1:0")
			port := pasv.Addr().(*net.TCPAddr).Port
			if strings.ToUpper(cmd) == "EPSV" {
				say("229 Entering Extended Passive Mode (|||%d|)", port)
			} else {
				say("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256)
			}
		case "STOR":
			f.record("STOR " + arg)
			say("150 los")
			dc, err := pasv.Accept()
			if err != nil {
				return
			}
			b, _ := io.ReadAll(dc)
			dc.Close()
			pasv.Close()
			_ = os.WriteFile(filepath.Join(f.dir, arg), b, 0o644)
			say("226 fertig")
		case "RNFR":
			f.record("RNFR " + arg)
			from = arg
			say("350 weiter")
		case "RNTO":
			f.record("RNTO " + arg)
			if _, err := os.Stat(filepath.Join(f.dir, arg)); err == nil && refuseOverwrite {
				say("550 Datei existiert")
				continue
			}
			if err := os.Rename(filepath.Join(f.dir, from), filepath.Join(f.dir, arg)); err != nil {
				say("550 %v", err)
				continue
			}
			say("250 ok")
		case "DELE":
			f.record("DELE " + arg)
			if err := os.Remove(filepath.Join(f.dir, arg)); err != nil {
				say("550 nicht da")
				continue
			}
			say("250 gelöscht")
		case "QUIT":
			say("221 tschüss")
			return
		default:
			say("502 nicht unterstützt")
		}
	}
}

func (f *ftpFake) target() FTPTarget {
	host, port, _ := net.SplitHostPort(f.addr)
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return FTPTarget{Host: host, Port: p, User: "pdh", Password: "geheim", Timeout: 5 * time.Second}
}

func TestFTPTargetPutRename(t *testing.T) {
	f := startFTP(t, false)
	tg := f.target()
	if err := tg.Put(context.Background(), "x", []byte("inhalt")); err != nil {
		t.Fatal(err)
	}
	got := f.commands()
	want := []string{"STOR .x.tmp", "RNFR .x.tmp", "RNTO x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Befehle %v, erwartet %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "x")); string(b) != "inhalt" {
		t.Errorf("Inhalt %q", b)
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 1 {
		t.Errorf("nur x darf liegen: %v", entries)
	}
}

func TestFTPTargetReplacesExisting(t *testing.T) {
	f := startFTP(t, true) // Server überschreibt beim Umbenennen nicht
	_ = os.WriteFile(filepath.Join(f.dir, "x"), []byte("alt"), 0o644)
	if err := f.target().Put(context.Background(), "x", []byte("neu")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "x")); string(b) != "neu" {
		t.Errorf("Inhalt %q", b)
	}
	got := strings.Join(f.commands(), "|")
	if got != "STOR .x.tmp|RNFR .x.tmp|RNTO x|DELE x|RNFR .x.tmp|RNTO x" {
		t.Errorf("Befehle %s", got)
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 1 {
		t.Errorf("nur x darf liegen: %v", entries)
	}
}

func TestFTPTargetLoginAndTest(t *testing.T) {
	f := startFTP(t, false)
	tg := f.target()
	msg, err := tg.Test(context.Background())
	if err != nil || !strings.Contains(msg, "Verbindung in Ordnung") {
		t.Fatalf("Test: %q %v", msg, err)
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 0 {
		t.Errorf("Testdatei übrig: %v", entries)
	}
	tg.Password = "falsch"
	if err := tg.Put(context.Background(), "x", []byte("a")); err == nil {
		t.Error("Anmeldefehler erwartet")
	}
	tg.Password = "geheim"
	tg.Folder = "ordner"
	if err := tg.Put(context.Background(), "x", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if c := f.commands(); !strings.Contains(strings.Join(c, "|"), "CWD ordner") {
		t.Errorf("CWD fehlt: %v", c)
	}
}
