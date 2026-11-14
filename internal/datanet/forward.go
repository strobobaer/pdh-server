package datanet

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

const (
	dialTimeout     = 10 * time.Second
	keepAlivePeriod = 30 * time.Second
	acceptPause     = 100 * time.Millisecond
)

// Forward nimmt Verbindungen vom Listener an und leitet sie an target weiter,
// bis ctx endet. Mit ctx enden auch alle laufenden Verbindungen. allow
// (optional) entscheidet je Gegenstelle; abgelehnte Verbindungen werden sofort
// geschlossen.
func Forward(ctx context.Context, ln net.Listener, target string, allow func(remote net.Addr) bool) {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := acceptRetry(ln)
		if err != nil {
			return
		}
		if allow != nil && !allow(c.RemoteAddr()) {
			c.Close()
			continue
		}
		go forwardConn(ctx, c, target)
	}
}

// acceptRetry nimmt eine Verbindung an; bei vorübergehenden Fehlern (z. B. zu
// viele offene Dateien) wird kurz pausiert und erneut versucht. Bei allen
// anderen Fehlern wird der Fehler zurückgegeben.
func acceptRetry(ln net.Listener) (net.Conn, error) {
	for {
		c, err := ln.Accept()
		if err == nil {
			return c, nil
		}
		if !isTemporary(err) {
			return nil, err
		}
		time.Sleep(acceptPause)
	}
}

func isTemporary(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return false
	}
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
		return true
	}
	var ne interface {
		Timeout() bool
		Temporary() bool
	}
	return errors.As(err, &ne) && (ne.Timeout() || ne.Temporary())
}

func forwardConn(ctx context.Context, c net.Conn, target string) {
	defer c.Close()
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	var d net.Dialer
	t, err := d.DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		return
	}
	defer t.Close()
	stop := context.AfterFunc(ctx, func() { c.Close(); t.Close() })
	defer stop()
	pipe(c, t)
}

func setKeepAlive(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(keepAlivePeriod)
	}
}

// pipe kopiert in beide Richtungen. Sauberes Ende (EOF) schließt nur die
// Schreibseite der Gegenseite (Halbschließen); endet eine Richtung mit Fehler,
// werden beide Verbindungen geschlossen. Kehrt zurück, wenn beide Richtungen
// beendet sind.
func pipe(a, b net.Conn) {
	setKeepAlive(a)
	setKeepAlive(b)
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
			cw.CloseWrite()
		} else {
			a.Close()
			b.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
