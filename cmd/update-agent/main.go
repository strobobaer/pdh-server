package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
}

type updateAgent struct {
	mu      sync.RWMutex
	status  jobStatus
	token   string
	repoDir string
	mode    string
	project string
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /v1/status", agent.authorized(http.HandlerFunc(agent.statusHandler)))
	mux.Handle("POST /v1/update", agent.authorized(http.HandlerFunc(agent.updateHandler)))
	mux.Handle("GET /v1/mounts", agent.authorized(http.HandlerFunc(agent.mountsHandler)))
	mux.Handle("POST /v1/mounts/mount", agent.authorized(http.HandlerFunc(agent.mountHandler)))
	mux.Handle("POST /v1/mounts/unmount", agent.authorized(http.HandlerFunc(agent.unmountHandler)))

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

func (a *updateAgent) updateHandler(w http.ResponseWriter, _ *http.Request) {
	if a.mode == "docker" {
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
		a.mu.Lock()
		if a.status.Running {
			a.mu.Unlock()
			http.Error(w, "update already running", http.StatusConflict)
			return
		}
		a.status = jobStatus{Running: true, StartedAt: time.Now().UTC()}
		a.mu.Unlock()
		go a.runUpdate()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

func (a *updateAgent) runUpdate() {
	output := &limitedBuffer{}
	script := filepath.Join(a.repoDir, "install_update.sh")
	command := exec.Command("bash", script)
	command.Dir = a.repoDir
	command.Env = append(os.Environ(), "PDH_UPDATE_MODE="+a.mode)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if err != nil {
		fmt.Fprintf(output, "\nUpdater failed: %v\n", err)
	}

	a.mu.Lock()
	a.status.Running = false
	a.status.Success = err == nil
	a.status.Output = output.String()
	a.status.FinishedAt = time.Now().UTC()
	a.mu.Unlock()
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