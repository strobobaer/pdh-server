package web

import (
	"bufio"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// RFB (VNC-Protokoll) fuer den HMI-Fernzugriff.
//
// Der PDH-Server steht zwischen Browser (noVNC) und HMI:
//   - zum HMI meldet er sich selbst an (VNC-Passwort bleibt auf dem Server),
//   - dem Browser bietet er nur „keine Anmeldung“ an,
//   - im Modus „ansehen“ liest er die Nachrichten des Browsers mit und
//     verwirft Tastatur, Maus, Zwischenablage und Groessenaenderungen –
//     unabhaengig davon, was der Browser schickt.
// Danach werden die Daten nur noch durchgereicht.

var errRFBUnsupported = errors.New("das HMI verlangt ein nicht unterstütztes Anmeldeverfahren (nur „keine“ und VNC-Passwort)")

const (
	rfbSecNone = 1
	rfbSecVNC  = 2
)

// rfbServerHandshake meldet sich am HMI an; danach ist ClientInit dran.
func rfbServerHandshake(rw io.ReadWriter, password string) error {
	var ver [12]byte
	if _, err := io.ReadFull(rw, ver[:]); err != nil {
		return fmt.Errorf("keine Antwort vom HMI: %w", err)
	}
	if string(ver[:4]) != "RFB " || ver[7] != '.' || ver[11] != '\n' {
		return errors.New("das Gegenüber ist kein VNC-Server")
	}
	major, _ := strconv.Atoi(string(ver[4:7]))
	minor, _ := strconv.Atoi(string(ver[8:11]))
	if major < 3 {
		return errors.New("VNC-Version wird nicht unterstützt")
	}
	if major > 3 || minor >= 8 {
		minor = 8
	} else if minor >= 7 {
		minor = 7
	} else {
		minor = 3
	}
	if _, err := fmt.Fprintf(rw, "RFB 003.%03d\n", minor); err != nil {
		return err
	}

	var sec uint32
	if minor == 3 {
		// 3.3: der Server bestimmt das Verfahren
		if err := binary.Read(rw, binary.BigEndian, &sec); err != nil {
			return err
		}
		if sec == 0 {
			return rfbReason(rw, "HMI lehnt die Verbindung ab")
		}
	} else {
		var n [1]byte
		if _, err := io.ReadFull(rw, n[:]); err != nil {
			return err
		}
		if n[0] == 0 {
			return rfbReason(rw, "HMI lehnt die Verbindung ab")
		}
		types := make([]byte, n[0])
		if _, err := io.ReadFull(rw, types); err != nil {
			return err
		}
		has := func(t byte) bool {
			for _, x := range types {
				if x == t {
					return true
				}
			}
			return false
		}
		switch {
		case password != "" && has(rfbSecVNC):
			sec = rfbSecVNC
		case has(rfbSecNone):
			sec = rfbSecNone
		case has(rfbSecVNC):
			sec = rfbSecVNC
		default:
			return errRFBUnsupported
		}
		if _, err := rw.Write([]byte{byte(sec)}); err != nil {
			return err
		}
	}

	switch sec {
	case rfbSecNone:
		if minor < 8 {
			return nil // ohne Anmeldung kein SecurityResult vor 3.8
		}
	case rfbSecVNC:
		var challenge [16]byte
		if _, err := io.ReadFull(rw, challenge[:]); err != nil {
			return err
		}
		resp, err := vncAuthResponse(password, challenge[:])
		if err != nil {
			return err
		}
		if _, err := rw.Write(resp); err != nil {
			return err
		}
	default:
		return errRFBUnsupported
	}
	var result uint32
	if err := binary.Read(rw, binary.BigEndian, &result); err != nil {
		return err
	}
	if result != 0 {
		if minor >= 8 {
			return rfbReason(rw, "Anmeldung am HMI fehlgeschlagen (Passwort?)")
		}
		return errors.New("Anmeldung am HMI fehlgeschlagen (Passwort?)")
	}
	return nil
}

// rfbReason liest die Begruendung des Servers (Laenge + Text).
func rfbReason(r io.Reader, fallback string) error {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil || n == 0 || n > 4096 {
		return errors.New(fallback)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return errors.New(fallback)
	}
	return fmt.Errorf("%s: %s", fallback, msg)
}

// vncAuthResponse: DES mit dem Passwort (max. 8 Zeichen, Bits je Byte gespiegelt).
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	var key [8]byte
	copy(key[:], password)
	for i, b := range key {
		var r byte
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				r |= 1 << (7 - bit)
			}
		}
		key[i] = r
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:16])
	return out, nil
}

// rfbClientHandshake spielt gegenueber dem Browser den Server: Version 3.8,
// einzig angebotenes Verfahren „keine Anmeldung“.
func rfbClientHandshake(rw io.ReadWriter) error {
	if _, err := io.WriteString(rw, "RFB 003.008\n"); err != nil {
		return err
	}
	var ver [12]byte
	if _, err := io.ReadFull(rw, ver[:]); err != nil {
		return err
	}
	if string(ver[:4]) != "RFB " {
		return errors.New("Browser spricht kein RFB")
	}
	if _, err := rw.Write([]byte{1, rfbSecNone}); err != nil {
		return err
	}
	var choice [1]byte
	if _, err := io.ReadFull(rw, choice[:]); err != nil {
		return err
	}
	if choice[0] != rfbSecNone {
		return errors.New("Browser wählt ein unbekanntes Verfahren")
	}
	_, err := rw.Write([]byte{0, 0, 0, 0}) // SecurityResult OK
	return err
}

// rfbForwardClient reicht die Nachrichten des Browsers an das HMI weiter.
// ClientInit wird immer als „geteilt“ gesendet, damit eine laufende
// Sitzung am HMI (z. B. ein anderer Viewer) nicht getrennt wird.
// viewOnly: Eingaben werden verworfen; dafuer wird jede Nachricht gelesen.
func rfbForwardClient(dst io.Writer, src io.Reader, viewOnly bool) error {
	br := bufio.NewReaderSize(src, 32<<10)
	if _, err := br.ReadByte(); err != nil { // ClientInit (shared-flag)
		return err
	}
	if _, err := dst.Write([]byte{1}); err != nil {
		return err
	}
	if !viewOnly {
		_, err := io.Copy(dst, br)
		return err
	}
	for {
		msg, forward, err := rfbReadClientMessage(br)
		if err != nil {
			return err
		}
		if forward {
			if _, err := dst.Write(msg); err != nil {
				return err
			}
		}
	}
}

// rfbReadClientMessage liest eine vollstaendige Client-Nachricht. forward
// ist false fuer alles, was das HMI bedienen oder veraendern wuerde.
func rfbReadClientMessage(r *bufio.Reader) (msg []byte, forward bool, err error) {
	typ, err := r.ReadByte()
	if err != nil {
		return nil, false, err
	}
	msg = []byte{typ}
	read := func(n int) error {
		if n < 0 || n > 16<<20 {
			return errors.New("ungültige Nachrichtenlänge")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		msg = append(msg, buf...)
		return nil
	}
	switch typ {
	case 0: // SetPixelFormat
		return msg, true, read(19)
	case 2: // SetEncodings
		if err := read(3); err != nil {
			return nil, false, err
		}
		n := int(binary.BigEndian.Uint16(msg[2:4]))
		return msg, true, read(4 * n)
	case 3: // FramebufferUpdateRequest
		return msg, true, read(9)
	case 4: // KeyEvent
		return msg, false, read(7)
	case 5: // PointerEvent
		return msg, false, read(5)
	case 6: // ClientCutText (auch erweiterte Zwischenablage mit negativer Laenge)
		if err := read(7); err != nil {
			return nil, false, err
		}
		n := int32(binary.BigEndian.Uint32(msg[4:8]))
		if n < 0 {
			n = -n
		}
		return msg, false, read(int(n))
	case 150: // EnableContinuousUpdates
		return msg, true, read(9)
	case 248: // ClientFence
		if err := read(8); err != nil {
			return nil, false, err
		}
		return msg, true, read(int(msg[8]))
	case 250: // XVP (Neustart/Herunterfahren)
		return msg, false, read(3)
	case 251: // SetDesktopSize
		if err := read(7); err != nil {
			return nil, false, err
		}
		return msg, false, read(16 * int(msg[6]))
	case 255: // QEMU
		if err := read(1); err != nil {
			return nil, false, err
		}
		if msg[1] == 0 { // erweiterte Taste
			return msg, false, read(10)
		}
	}
	return nil, false, fmt.Errorf("unbekannte Nachricht %d vom Browser", typ)
}
