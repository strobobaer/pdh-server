package web

import (
	"bufio"
	"net"
	"net/smtp"
	"strings"
	"testing"

	"pdh/pkg/config"
)

// fakeSMTP: minimaler Server, der nur die angegebenen AUTH-Verfahren anbietet
// und das gewaehlte Verfahren zurueckmeldet.
func fakeSMTP(t *testing.T, mechs string) (*smtp.Client, chan string) {
	t.Helper()
	srv, cli := net.Pipe()
	used := make(chan string, 1)
	go func() {
		defer srv.Close()
		r := bufio.NewReader(srv)
		srv.Write([]byte("220 test\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				srv.Write([]byte("250-test\r\n250 AUTH " + mechs + "\r\n"))
			case strings.HasPrefix(cmd, "AUTH LOGIN"):
				srv.Write([]byte("334 VXNlcm5hbWU6\r\n")) // Username:
				u, _ := r.ReadString('\n')
				srv.Write([]byte("334 UGFzc3dvcmQ6\r\n")) // Password:
				p, _ := r.ReadString('\n')
				used <- "LOGIN " + strings.TrimSpace(u) + " " + strings.TrimSpace(p)
				srv.Write([]byte("235 ok\r\n"))
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				used <- "PLAIN"
				srv.Write([]byte("235 ok\r\n"))
			default:
				srv.Write([]byte("250 ok\r\n"))
			}
		}
	}()
	c, err := smtp.NewClient(cli, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Hello("localhost"); err != nil {
		t.Fatal(err)
	}
	return c, used
}

func TestSMTPAuthLoginForMicrosoft365(t *testing.T) {
	c, used := fakeSMTP(t, "LOGIN XOAUTH2")
	defer c.Close()
	cfg := config.MailConfig{Host: "localhost", User: "pdh@example.com", Password: "geheim"}
	if err := c.Auth(smtpAuthFor(c, cfg)); err != nil {
		t.Fatal(err)
	}
	// base64("pdh@example.com"), base64("geheim")
	if got, want := <-used, "LOGIN cGRoQGV4YW1wbGUuY29t Z2VoZWlt"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSMTPAuthPrefersPlain(t *testing.T) {
	c, used := fakeSMTP(t, "PLAIN LOGIN")
	defer c.Close()
	cfg := config.MailConfig{Host: "localhost", User: "u", Password: "p"}
	if err := c.Auth(smtpAuthFor(c, cfg)); err != nil {
		t.Fatal(err)
	}
	if got := <-used; got != "PLAIN" {
		t.Fatalf("got %q, want PLAIN", got)
	}
}
