//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// commandAs startet den Befehl direkt mit uid/gid des Benutzers – wie
// „runuser -u“, aber ohne PAM-Sitzung (sonst zwei Journal-Zeilen je Abfrage).
func commandAs(ctx context.Context, name string, argv []string) (*exec.Cmd, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, err
	}
	var groups []uint32
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil {
				groups = append(groups, uint32(n))
			}
		}
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
	c.Dir = "/"
	env := []string{"HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "USER=") && !strings.HasPrefix(e, "LOGNAME=") {
			env = append(env, e)
		}
	}
	c.Env = env
	return c, nil
}
