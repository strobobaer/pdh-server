package datanet

import (
	"context"
	"io"
	"net"
	"time"
)

const dialTimeout = 10 * time.Second

// Forward nimmt Verbindungen vom Listener an und leitet sie an target weiter,
// bis ctx endet. allow (optional) entscheidet je Gegenstelle; abgelehnte
// Verbindungen werden sofort geschlossen.
func Forward(ctx context.Context, ln net.Listener, target string, allow func(remote net.Addr) bool) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if allow != nil && !allow(c.RemoteAddr()) {
			c.Close()
			continue
		}
		go forwardConn(ctx, c, target)
	}
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
	pipe(c, t)
}

// pipe kopiert in beide Richtungen mit Halbschließen und kehrt zurück, wenn
// beide Richtungen beendet sind.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
