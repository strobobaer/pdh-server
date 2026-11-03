package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type jobStatus struct {
	Running    bool      `json:"running"`
	Success    bool      `json:"success"`
	Output     string    `json:"output"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Commit     string    `json:"commit"` // Ziel des Laufs (--commit/--rollback), sonst leer
	Op         string    `json:"op"`     // Art: install[_norestart], rollback[_norestart], finish; leer = ohne Schalter
}

type updateAgent struct {
	dr        drState                                                           // Ausfallschutz (dr.go), eigener Mutex
	drApply   func(context.Context, drConfig) (bool, error)                     // nil = drApplyPostgres
	drSysID   func(context.Context) (string, error)                             // nil = psql (dr_owner.go)
	drTLI     func(context.Context) uint32                                      // nil = psql (dr_timeline.go)
	drPartner func(context.Context) bool                                        // nil = pdh-ha-Status (dr_timeline.go)
	ha        haJob                                                             // pdh-ha-Installation (ha.go)
	haRun     func(context.Context, string, map[string]string, io.Writer) error // nil = hostCommandEnv
	mu        sync.RWMutex
	status    jobStatus
	token     string
	repoDir   string
	mode      string
	project   string

	updateRun func(args []string) // nil = runUpdate (install_update.sh); Test-Haken

	win       winRuntime                                                // Wartungsfenster (window.go)
	sj        jobRuntime                                                // Update auf dem Einzelserver (job.go)
	gitRun    func(ctx context.Context, args ...string) (string, error) // nil = git im repoDir; Test-Haken
	clock     func() time.Time                                          // nil = time.Now; Test-Haken
	freeSpace func(path string) (int64, bool)                           // nil = freeSpaceOS; Test-Haken
	sleep     func(time.Duration)                                       // nil = time.Sleep; Test-Haken
	jobManual bool                                                      // Tests: Schritte des Auftrags von Hand
}

type limitedBuffer struct {
	bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLength := len(p)
	const outputLimit = 64 * 1024
	remaining := outputLimit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return originalLength, nil
}

func main() {
	token := strings.TrimSpace(os.Getenv("PDH_UPDATE_AGENT_TOKEN"))
	if len(token) < 32 {
		log.Fatal("PDH_UPDATE_AGENT_TOKEN must be at least 32 characters")
	}
	repoDir := strings.TrimSpace(os.Getenv("PDH_UPDATE_REPO_DIR"))
	if repoDir == "" {
		repoDir = "/repo"
	}
	mode := strings.TrimSpace(os.Getenv("PDH_UPDATE_MODE"))
	if mode != "docker" && mode != "systemd" {
		log.Fatal("PDH_UPDATE_MODE must be docker or systemd")
	}
	listen := strings.TrimSpace(os.Getenv("PDH_UPDATE_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8091"
	}

	project := strings.TrimSpace(os.Getenv("PDH_COMPOSE_PROJECT_NAME"))
	if project == "" {
		project = "pdh-server"
	}
	agent := &updateAgent{token: token, repoDir: repoDir, mode: mode, project: project}
	if mode == "systemd" {
		agent.loadUpdateStatus() // Ergebnis des letzten Laufs überlebt den Neustart des Agenten
		agent.windowLoad()
		agent.loadSingleJob() // offener Auftrag (Einzelserver) läuft weiter
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /v1/status", agent.authorized(http.HandlerFunc(agent.statusHandler)))
	mux.Handle("POST /v1/update", agent.authorized(http.HandlerFunc(agent.updateHandler)))
	mux.Handle("GET /v1/mounts", agent.authorized(http.HandlerFunc(agent.mountsHandler)))
	mux.Handle("POST /v1/mounts/mount", agent.authorized(http.HandlerFunc(agent.mountHandler)))
	mux.Handle("POST /v1/mounts/unmount", agent.authorized(http.HandlerFunc(agent.unmountHandler)))
	mux.Handle("POST /v1/env/remove", agent.authorized(http.HandlerFunc(agent.envRemoveHandler)))
	mux.Handle("POST /v1/nextcloud/check", agent.authorized(http.HandlerFunc(agent.ncCheckHandler)))
	mux.Handle("POST /v1/nextcloud/update", agent.authorized(http.HandlerFunc(agent.ncUpdateHandler)))
	mux.Handle("GET /v1/nextcloud/status", agent.authorized(http.HandlerFunc(agent.ncStatusHandler)))
	mux.Handle("POST /v1/nextcloud/oidc", agent.authorized(http.HandlerFunc(agent.ncOIDCHandler)))
	mux.Handle("POST /v1/nextcloud/service-account", agent.authorized(http.HandlerFunc(agent.ncServiceHandler)))
	// Ausfallschutz – Einzelbetrieb (dr.go)
	if err := agent.drLoad(); err != nil {
		log.Printf("ausfallschutz: state.json nicht lesbar: %v", err)
		agent.drFail("state", fmt.Errorf("Zustand nicht lesbar (Einstellungen im PDH erneut speichern): %w", err))
	}
	mux.Handle("GET /v1/dr/status", agent.authorized(http.HandlerFunc(agent.drStatusHandler)))
	mux.Handle("POST /v1/dr/config", agent.authorized(http.HandlerFunc(agent.drConfigHandler)))
	mux.Handle("POST /v1/dr/run", agent.authorized(http.HandlerFunc(agent.drRunHandler)))
	go agent.drLoop(context.Background())
	// Ausfallschutz mit zwei Servern – pdh-ha (ha.go)
	mux.Handle("POST /v1/ha/install", agent.authorized(http.HandlerFunc(agent.haInstallHandler)))
	mux.Handle("GET /v1/ha/install", agent.authorized(http.HandlerFunc(agent.haInstallStatusHandler)))
	// Update auf dem Einzelserver und Wartungsfenster (job.go, window.go)
	mux.Handle("GET /v1/update/job", agent.authorized(http.HandlerFunc(agent.jobHandler)))
	mux.Handle("POST /v1/update/job/start", agent.authorized(http.HandlerFunc(agent.jobStartHandler)))
	mux.Handle("POST /v1/update/job/abort", agent.authorized(http.HandlerFunc(agent.jobAbortHandler)))
	mux.Handle("GET /v1/window", agent.authorized(http.HandlerFunc(agent.windowGetHandler)))
	mux.Handle("POST /v1/window", agent.authorized(http.HandlerFunc(agent.windowPostHandler)))
	mux.Handle("DELETE /v1/window/faulty/{commit}", agent.authorized(http.HandlerFunc(agent.windowFaultyDeleteHandler)))

	server := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second, // Einbinden eines Netzlaufwerks kann dauern
	}
	log.Printf("update agent listening on %s in %s mode", listen, mode)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *updateAgent) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(a.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *updateAgent) statusHandler(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	status := a.status
	a.mu.RUnlock()
	if a.mode == "docker" {
		if dockerStatus, err := a.dockerJobStatus(); err == nil {
			status = dockerStatus
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(status)
}

func (a *updateAgent) updateHandler(w http.ResponseWriter, r *http.Request) {
	// Rumpf ist optional: leer = bisheriges Verhalten (aktueller Stand des Update-Zweigs).
	var req updateRequest
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeUpdateJSON(w, http.StatusBadRequest, map[string]string{"error": "ungültiger Rumpf"})
				return
			}
		}
	}
	args, err := req.args()
	if err != nil {
		writeUpdateJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if a.mode == "docker" {
		// Docker-Betrieb nur für Testsysteme: gezielter Stand, Zurückdrehen und
		// Trennen von Bau und Neustart gibt es dort nicht.
		if !req.isEmpty() {
			writeUpdateJSON(w, http.StatusBadRequest, map[string]string{"error": "Gezielte Updates gibt es nur bei systemd-Installationen"})
			return
		}
		if status, err := a.dockerJobStatus(); err == nil && status.Running {
			http.Error(w, "update already running", http.StatusConflict)
			return
		}
		a.mu.Lock()
		if a.status.Running {
			a.mu.Unlock()
			http.Error(w, "update already running", http.StatusConflict)
			return
		}
		a.status = jobStatus{Running: true, StartedAt: time.Now().UTC()}
		a.mu.Unlock()
		if err := a.startDockerJob(); err != nil {
			a.mu.Lock()
			a.status = jobStatus{Success: false, Output: err.Error(), FinishedAt: time.Now().UTC()}
			a.mu.Unlock()
			http.Error(w, "could not start update job", http.StatusInternalServerError)
			return
		}
		a.mu.Lock()
		a.status = jobStatus{}
		a.mu.Unlock()
	} else {
		if a.singleJobActive() {
			// Einzelserver-Auftrag (job.go) führt install_update.sh selbst aus
			writeUpdateJSON(w, http.StatusConflict, map[string]string{"error": "Ein Update-Auftrag läuft bereits"})
			return
		}
		if running, ok := a.startUpdate(req.target(), req.op()); !ok {
			// Art und Ziel des laufenden Laufs: pdh-ha wartet nur auf das eigene Ergebnis
			writeUpdateJSON(w, http.StatusConflict, map[string]string{"error": "Update läuft bereits", "commit": running.Commit, "op": running.Op})
			return
		}
		go a.runUpdateHook(args)
	}
	writeUpdateJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *updateAgent) startDockerJob() error {
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	_ = exec.Command("docker", "rm", "-f", "pdh-update-job").Run()
	command := exec.Command("docker", "run", "--detach", "--name", "pdh-update-job",
		"--volumes-from", hostname,
		"--env", "PDH_UPDATE_MODE=docker",
		"--env", "PDH_COMPOSE_PROJECT_NAME="+a.project,
		"--entrypoint", "/bin/bash", "pdh-updater:local", "/repo/install_update.sh")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker job start failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (a *updateAgent) dockerJobStatus() (jobStatus, error) {
	status := jobStatus{}
	command := exec.Command("docker", "inspect", "--format",
		"{{.State.Status}}|{{.State.ExitCode}}|{{.State.StartedAt}}|{{.State.FinishedAt}}", "pdh-update-job")
	output, err := command.Output()
	if err != nil {
		return status, err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 4 {
		return status, fmt.Errorf("unexpected docker job status")
	}
	status.Running = fields[0] == "running" || fields[0] == "created"
	status.Success = fields[0] == "exited" && fields[1] == "0"
	status.StartedAt, _ = time.Parse(time.RFC3339Nano, fields[2])
	status.FinishedAt, _ = time.Parse(time.RFC3339Nano, fields[3])
	logs := exec.Command("docker", "logs", "--tail", "200", "pdh-update-job")
	logOutput := &limitedBuffer{}
	logs.Stdout = logOutput
	logs.Stderr = logOutput
	_ = logs.Run()
	status.Output = logOutput.String()
	return status, nil
}
