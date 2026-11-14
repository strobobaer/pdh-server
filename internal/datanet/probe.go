package datanet

import (
	"context"
	"net"
	"time"
)

// Probe prüft, ob addr eine TCP-Verbindung annimmt (auf und gleich wieder zu).
func Probe(ctx context.Context, addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
