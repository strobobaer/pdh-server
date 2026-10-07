package main

// Netzlaufwerke einbinden (SMB/CIFS, NFS) – im Auftrag von PDH.
//
// PDH selbst laeuft ohne Root-Rechte; Mounten braucht sie. Der Agent bindet
// daher unter <PDH_MOUNT_ROOT>/<key> ein:
//   - systemd: im Mount-Namespace des Hosts (nsenter auf PID 1), weil der
//     Dienst selbst in einem eigenen Namespace laeuft (PrivateTmp)
//   - docker:  im (privilegierten) Updater-Container; <PDH_MOUNT_ROOT> ist mit
//     rshared eingebunden und im App-Container mit rslave sichtbar
// Zugangsdaten gehen nur ueber eine kurzlebige Datei (0600) an mount.cifs und
// erscheinen nie in der Prozessliste.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	mountKeyRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	smbSourceRe    = regexp.MustCompile(`^//[A-Za-z0-9._-]+(/[^\s,'"\\]+)+$`)
	nfsSourceRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+:/[^\s,'"\\]*$`)
	mountOptionsRe = regexp.MustCompile(`^[A-Za-z0-9=._,:-]*$`)
)

// selbst gesetzte bzw. gefaehrliche Optionen duerfen nicht ueberschrieben werden
var blockedMountOptions = map[string]bool{
	"credentials": true, "cred": true, "username": true, "user": true, "password": true, "pass": true,
	"uid": true, "gid": true, "forceuid": true, "forcegid": true, "suid": true, "dev": true, "exec": true, "setuids": true,
}

type mountRequest struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"` // smb | nfs
	Source   string `json:"source"`
	Username string `json:"username"`
	Password string `json:"password"`
	Domain   string `json:"domain"`
	Options  string `json:"options"`
	ReadOnly bool   `json:"read_only"`
}

func mountRoot() string {
	if r := strings.TrimSpace(os.Getenv("PDH_MOUNT_ROOT")); r != "" {
		return r
	}
	return "/mnt/pdh"
}

// mountOwner: Benutzer, unter dem PDH laeuft (Dateirechte bei SMB).
func (a *updateAgent) mountOwner() (string, string) {
	uid, gid := strings.TrimSpace(os.Getenv("PDH_MOUNT_UID")), strings.TrimSpace(os.Getenv("PDH_MOUNT_GID"))
	if uid == "" && a.mode == "systemd" {
		if u, err := user.Lookup("pdh"); err == nil {
			uid, gid = u.Uid, u.Gid
		}
	}
	if uid == "" {
		uid = "10001"
	}
	if gid == "" {
		gid = uid
	}
	return uid, gid
}

func validateMount(req *mountRequest) error {
	req.Key, req.Source, req.Options = strings.TrimSpace(req.Key), strings.TrimSpace(req.Source), strings.TrimSpace(req.Options)
	if !mountKeyRe.MatchString(req.Key) {
		return fmt.Errorf("ungültiger Laufwerksschlüssel")
	}
	switch req.Kind {
	case "smb":
		if !smbSourceRe.MatchString(req.Source) {
			return fmt.Errorf("Freigabe bitte als //server/freigabe angeben")
		}
	case "nfs":
		if !nfsSourceRe.MatchString(req.Source) {
			return fmt.Errorf("NFS-Export bitte als server:/pfad angeben")
		}
	default:
		return fmt.Errorf("unbekannte Laufwerksart")
	}
	if !mountOptionsRe.MatchString(req.Options) || len(req.Options) > 300 {
		return fmt.Errorf("ungültige Mount-Optionen")
	}
	for _, o := range strings.Split(req.Options, ",") {
		name := strings.ToLower(strings.SplitN(o, "=", 2)[0])
		if blockedMountOptions[name] {
			return fmt.Errorf("Mount-Option %q ist nicht erlaubt", name)
		}
	}
	for _, s := range []string{req.Username, req.Password, req.Domain} {
		if strings.ContainsAny(s, "\r\n\x00") || len(s) > 256 {
			return fmt.Errorf("ungültige Zugangsdaten")
		}
	}
	return nil
}

// run fuehrt mount/umount im richtigen Namespace aus.
func (a *updateAgent) run(ctx context.Context, name string, args ...string) (string, error) {
	if a.mode == "systemd" {
		args = append([]string{"-t", "1", "-m", "--", name}, args...)
		name = "nsenter"
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// mountedTargets liest die eingebundenen Ziele unter mountRoot().
func (a *updateAgent) mountedTargets() map[string]bool {
	file := "/proc/self/mountinfo"
	if a.mode == "systemd" {
		file = "/proc/1/mountinfo"
	}
	out := map[string]bool{}
	f, err := os.Open(file)
	if err != nil {
		return out
	}
	defer f.Close()
	root := filepath.Clean(mountRoot()) + "/"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 && strings.HasPrefix(fields[4], root) {
			out[strings.TrimPrefix(fields[4], root)] = true
		}
	}
	return out
}

func writeMountJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *updateAgent) mountsHandler(w http.ResponseWriter, _ *http.Request) {
	keys := []string{}
	for k := range a.mountedTargets() {
		keys = append(keys, k)
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"root": mountRoot(), "mounted": keys})
}

func (a *updateAgent) mountHandler(w http.ResponseWriter, r *http.Request) {
	var req mountRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if err := validateMount(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	target := filepath.Join(mountRoot(), req.Key)
	if _, err := a.run(ctx, "mkdir", "-p", target); err != nil {
		writeMountJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Mount-Verzeichnis: " + err.Error()})
		return
	}
	if a.mountedTargets()[req.Key] {
		_, _ = a.run(ctx, "umount", target) // neu einbinden, damit geaenderte Einstellungen greifen
	}
	opts := []string{}
	if req.ReadOnly {
		opts = append(opts, "ro")
	} else {
		opts = append(opts, "rw")
	}
	opts = append(opts, "nosuid", "nodev", "noexec")
	var fsType string
	switch req.Kind {
	case "smb":
		fsType = "cifs"
		dir := "/run/pdh-mounts"
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeMountJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		cred := filepath.Join(dir, req.Key+".cred")
		content := "username=" + req.Username + "\npassword=" + req.Password + "\n"
		if req.Domain != "" {
			content += "domain=" + req.Domain + "\n"
		}
		if req.Username == "" {
			content = "username=guest\npassword=\n"
		}
		if err := os.WriteFile(cred, []byte(content), 0o600); err != nil {
			writeMountJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		defer os.Remove(cred)
		uid, gid := a.mountOwner()
		opts = append(opts, "credentials="+cred, "uid="+uid, "gid="+gid, "forceuid", "forcegid", "file_mode=0660", "dir_mode=0770", "iocharset=utf8")
	case "nfs":
		fsType = "nfs"
		opts = append(opts, "soft", "timeo=150", "retrans=3")
	}
	if req.Options != "" {
		opts = append(opts, req.Options)
	}
	out, err := a.run(ctx, "mount", "-t", fsType, req.Source, target, "-o", strings.Join(opts, ","))
	if err != nil {
		writeMountJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": mountError(out, err)})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "path": target})
}

func (a *updateAgent) unmountHandler(w http.ResponseWriter, r *http.Request) {
	var req mountRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || !mountKeyRe.MatchString(req.Key) {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	target := filepath.Join(mountRoot(), req.Key)
	if !a.mountedTargets()[req.Key] {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if out, err := a.run(ctx, "umount", target); err != nil {
		// Dateien noch geoeffnet: verzoegert aushaengen
		if out2, err2 := a.run(ctx, "umount", "-l", target); err2 != nil {
			writeMountJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": mountError(out+" "+out2, err2)})
			return
		}
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mountError uebersetzt die haeufigsten mount-Fehler.
func mountError(out string, err error) string {
	l := strings.ToLower(out)
	switch {
	case strings.Contains(l, "permission denied") || strings.Contains(l, "error(13)"):
		return "Zugriff verweigert – Benutzername, Passwort oder Domäne prüfen"
	case strings.Contains(l, "no such file") || strings.Contains(l, "error(2)"):
		return "Freigabe nicht gefunden – Server und Freigabename prüfen"
	case strings.Contains(l, "host is down") || strings.Contains(l, "error(112)") || strings.Contains(l, "timed out") || strings.Contains(l, "no route"):
		return "Server nicht erreichbar"
	case strings.Contains(l, "wrong fs type") || strings.Contains(l, "bad option"):
		return "Mount-Hilfsprogramm fehlt (cifs-utils / nfs-common) oder Option ungültig: " + out
	case strings.Contains(l, "error(95)") || strings.Contains(l, "operation not supported"):
		return "SMB-Version passt nicht – unter Optionen z. B. vers=2.1 oder vers=3.0 angeben"
	}
	if out == "" {
		out = err.Error()
	}
	return "Einbinden fehlgeschlagen: " + out
}
