package web

import (
	"bytes"
	"crypto/des"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

// fakeVNC spielt einen VNC-Server (3.8, VNC-Passwort) auf einer Pipe.
func fakeVNC(t *testing.T, conn net.Conn, password string, offer []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)
		io.WriteString(conn, "RFB 003.008\n")
		ver := make([]byte, 12)
		if _, err := io.ReadFull(conn, ver); err != nil {
			done <- err
			return
		}
		conn.Write(append([]byte{byte(len(offer))}, offer...))
		choice := make([]byte, 1)
		io.ReadFull(conn, choice)
		if choice[0] == rfbSecVNC {
			challenge := []byte("0123456789abcdef")
			conn.Write(challenge)
			resp := make([]byte, 16)
			io.ReadFull(conn, resp)
			want, _ := vncAuthResponse(password, challenge)
			if !bytes.Equal(resp, want) {
				binary.Write(conn, binary.BigEndian, uint32(1))
				msg := "falsches Passwort"
				binary.Write(conn, binary.BigEndian, uint32(len(msg)))
				io.WriteString(conn, msg)
				return
			}
		}
		binary.Write(conn, binary.BigEndian, uint32(0))
	}()
	return done
}

func TestRFBServerHandshake(t *testing.T) {
	a, b := net.Pipe()
	fakeVNC(t, b, "geheim", []byte{rfbSecVNC})
	if err := rfbServerHandshake(a, "geheim"); err != nil {
		t.Fatalf("richtiges Passwort: %v", err)
	}
	a.Close()

	a, b = net.Pipe()
	fakeVNC(t, b, "geheim", []byte{rfbSecVNC})
	if err := rfbServerHandshake(a, "falsch"); err == nil || !strings.Contains(err.Error(), "falsches Passwort") {
		t.Fatalf("falsches Passwort nicht erkannt: %v", err)
	}
	a.Close()

	a, b = net.Pipe()
	fakeVNC(t, b, "", []byte{19}) // nur VeNCrypt
	if err := rfbServerHandshake(a, ""); err != errRFBUnsupported {
		t.Fatalf("nicht unterstütztes Verfahren: %v", err)
	}
	a.Close()
}

// Unabhaengige Pruefung der DES-Antwort: Schluessel bitweise gespiegelt.
func TestVNCAuthResponse(t *testing.T) {
	challenge := []byte("ABCDEFGHIJKLMNOP")
	got, err := vncAuthResponse("pw", challenge)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte{0x0E, 0xEE, 0, 0, 0, 0, 0, 0} // 'p'=0x70→0x0E, 'w'=0x77→0xEE
	block, _ := des.NewCipher(key)
	want := make([]byte, 16)
	block.Encrypt(want[:8], challenge[:8])
	block.Encrypt(want[8:], challenge[8:])
	if !bytes.Equal(got, want) {
		t.Fatalf("DES-Antwort falsch")
	}
}

func TestRFBClientHandshakeOffersNoAuth(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		ver := make([]byte, 12)
		io.ReadFull(b, ver)
		io.WriteString(b, "RFB 003.008\n")
		types := make([]byte, 2)
		io.ReadFull(b, types)
		if types[0] != 1 || types[1] != rfbSecNone {
			b.Close()
			return
		}
		b.Write([]byte{rfbSecNone})
		res := make([]byte, 4)
		io.ReadFull(b, res)
		b.Close()
	}()
	if err := rfbClientHandshake(a); err != nil {
		t.Fatal(err)
	}
}

// Nur ansehen: Tastatur, Maus, Zwischenablage und Groessenaenderung kommen nie am HMI an.
func TestRFBForwardViewOnlyDropsInput(t *testing.T) {
	var in bytes.Buffer
	in.WriteByte(0)                                                                            // ClientInit exklusiv – wird zu geteilt
	in.Write([]byte{3, 0, 0, 0, 0, 0, 0, 10, 0, 10})                                           // FramebufferUpdateRequest
	in.Write([]byte{4, 1, 0, 0, 0, 0, 0, 0x41})                                                // KeyEvent
	in.Write([]byte{5, 1, 0, 10, 0, 10})                                                       // PointerEvent
	in.Write([]byte{6, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'})                                    // ClientCutText
	in.Write([]byte{2, 0, 0, 1, 0, 0, 0, 7})                                                   // SetEncodings (1)
	in.Write([]byte{251, 0, 0, 1, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) // SetDesktopSize (1 Screen)
	in.Write([]byte{255, 0, 0, 1, 0, 0, 0, 0x41, 0, 0, 0, 30})                                 // QEMU-Taste
	var out bytes.Buffer
	err := rfbForwardClient(&out, &in, true)
	if err != io.EOF {
		t.Fatalf("Ende erwartet: %v", err)
	}
	want := []byte{1, 3, 0, 0, 0, 0, 0, 0, 10, 0, 10, 2, 0, 0, 1, 0, 0, 0, 7}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("weitergereicht:\n%v\nerwartet:\n%v", out.Bytes(), want)
	}

	// Bedienen: alles durch (ausser ClientInit wird geteilt)
	out.Reset()
	if err := rfbForwardClient(&out, bytes.NewReader([]byte{0, 4, 1, 0, 0, 0, 0, 0, 0x41}), false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), []byte{1, 4, 1, 0, 0, 0, 0, 0, 0x41}) {
		t.Fatalf("Bedienen: %v", out.Bytes())
	}

	// Unbekannte Nachricht im Ansehen-Modus: Verbindung beenden statt raten
	if err := rfbForwardClient(io.Discard, bytes.NewReader([]byte{1, 77, 1, 2}), true); err == nil || !strings.Contains(err.Error(), "unbekannte Nachricht") {
		t.Fatalf("unbekannte Nachricht: %v", err)
	}
}

func TestHMIModeRespectsPermissionAndHMI(t *testing.T) {
	open := &hmiTarget{AllowControl: true}
	locked := &hmiTarget{AllowControl: false}
	cases := []struct {
		req    string
		p      hmiPerms
		t      *hmiTarget
		expect string
	}{
		{"control", hmiPerms{View: true}, open, "view"},
		{"control", hmiPerms{View: true, Control: true}, open, "control"},
		{"control", hmiPerms{View: true, Control: true}, locked, "view"},
		{"", hmiPerms{View: true, Control: true}, open, "view"},
	}
	for _, c := range cases {
		if got := hmiMode(c.req, c.p, c.t); got != c.expect {
			t.Errorf("%+v → %s, erwartet %s", c, got, c.expect)
		}
	}
}
