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
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
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
