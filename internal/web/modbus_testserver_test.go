package web

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
)

// testModbusServer ist ein kleiner Modbus-TCP-Server im Prozess (unabhängig von der Client-
// Bibliothek geschrieben): Funktionscodes 1, 2, 3, 4, 5, 6, 15 und 16. Adressen außerhalb
// 0–999 beantwortet er mit Ausnahme 0x02. Jede Antwort gibt die Transaktions-ID zurück.
type testModbusServer struct {
	Addr string

	mu       sync.Mutex
	Holding  [1000]uint16
	Input    [1000]uint16
	Coils    [1000]bool
	Discrete [1000]bool
	LastUnit byte
	DropNext int  // die nächsten Anfragen: Verbindung ohne Antwort schließen
	Stall    bool // Anfragen annehmen, aber nie antworten
	Requests int
}

func startTestModbusServer(t *testing.T) *testModbusServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testModbusServer{Addr: l.Addr().String()}
	var conns sync.Map
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns.Store(c, true)
			go func() {
				defer conns.Delete(c)
				s.serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
	})
	return s
}

// Set trägt Werte unter dem Schloss des Servers ein (Tests laufen neben dem Server-Goroutine).
func (s *testModbusServer) Set(f func(s *testModbusServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *testModbusServer) serve(c net.Conn) {
	defer c.Close()
	for {
		head := make([]byte, 7)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(head[4:6]))
		if n < 2 || n > 260 {
			return
		}
		pdu := make([]byte, n-1)
		if _, err := io.ReadFull(c, pdu); err != nil {
			return
		}
		s.mu.Lock()
		drop, stall := s.DropNext > 0, s.Stall
		if drop {
			s.DropNext--
		}
		s.mu.Unlock()
		if drop {
			return
		}
		if stall {
			io.Copy(io.Discard, c)
			return
		}
		resp := s.handle(head[6], pdu)
		out := make([]byte, 7, 7+len(resp))
		copy(out, head[:4])
		binary.BigEndian.PutUint16(out[4:6], uint16(len(resp)+1))
		out[6] = head[6]
		out = append(out, resp...)
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

func modbusException(fc, code byte) []byte { return []byte{fc | 0x80, code} }

func (s *testModbusServer) handle(unit byte, pdu []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastUnit = unit
	s.Requests++
	fc := pdu[0]
	if len(pdu) < 5 {
		return modbusException(fc, 0x03)
	}
	addr := int(binary.BigEndian.Uint16(pdu[1:3]))
	arg := int(binary.BigEndian.Uint16(pdu[3:5]))
	switch fc {
	case 1, 2:
		bits := s.Coils[:]
		if fc == 2 {
			bits = s.Discrete[:]
		}
		if arg < 1 || arg > 2000 || addr+arg > len(bits) {
			return modbusException(fc, 0x02)
		}
		out := make([]byte, 2+(arg+7)/8)
		out[0], out[1] = fc, byte((arg+7)/8)
		for i := 0; i < arg; i++ {
			if bits[addr+i] {
				out[2+i/8] |= 1 << (i % 8)
			}
		}
		return out
	case 3, 4:
		regs := s.Holding[:]
		if fc == 4 {
			regs = s.Input[:]
		}
		if arg < 1 || arg > 125 || addr+arg > len(regs) {
			return modbusException(fc, 0x02)
		}
		out := []byte{fc, byte(2 * arg)}
		for i := 0; i < arg; i++ {
			out = binary.BigEndian.AppendUint16(out, regs[addr+i])
		}
		return out
	case 5:
		if addr >= len(s.Coils) {
			return modbusException(fc, 0x02)
		}
		s.Coils[addr] = arg == 0xFF00
		return append([]byte(nil), pdu[:5]...)
	case 6:
		if addr >= len(s.Holding) {
			return modbusException(fc, 0x02)
		}
		s.Holding[addr] = uint16(arg)
		return append([]byte(nil), pdu[:5]...)
	case 15:
		if len(pdu) < 6 || arg < 1 || addr+arg > len(s.Coils) || len(pdu) < 6+(arg+7)/8 {
			return modbusException(fc, 0x02)
		}
		for i := 0; i < arg; i++ {
			s.Coils[addr+i] = pdu[6+i/8]&(1<<(i%8)) != 0
		}
		return append([]byte(nil), pdu[:5]...)
	case 16:
		if len(pdu) < 6 || arg < 1 || arg > 123 || addr+arg > len(s.Holding) || len(pdu) < 6+2*arg {
			return modbusException(fc, 0x02)
		}
		for i := 0; i < arg; i++ {
			s.Holding[addr+i] = binary.BigEndian.Uint16(pdu[6+2*i:])
		}
		return append([]byte(nil), pdu[:5]...)
	}
	return modbusException(fc, 0x01)
}
