package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runner führt Systembefehle aus. Alles, was PostgreSQL und Dienste steuert,
// läuft darüber – in Tests ersetzt ein fakeRunner die echten Befehle.
type runner interface {
	Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// „runuser -u NAME -- befehl …“ ohne PAM ausführen (Abfragen laufen
	// mehrmals je Sekunde und füllten sonst das Journal)
	if len(argv) > 4 && argv[0] == "runuser" && argv[1] == "-u" && argv[3] == "--" {
		if as, err := commandAs(ctx, argv[2], argv[4:]); err == nil {
			c = as
		}
	}
	if len(env) > 0 {
		base := c.Env
		if base == nil {
			base = os.Environ()
		}
		c.Env = append(base, env...)
	}
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
