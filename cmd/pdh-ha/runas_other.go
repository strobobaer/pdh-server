//go:build !unix

package main

import (
	"context"
	"errors"
	"os/exec"
)

func commandAs(context.Context, string, []string) (*exec.Cmd, error) {
	return nil, errors.New("nur unter Unix")
}
