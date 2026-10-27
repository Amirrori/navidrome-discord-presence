// Package worker runs the per-user worker process that holds the Discord
// Social SDK connection. The two sides exchange one JSON object per line:
// commands on the worker's stdin, events on its stdout. Access tokens only
// travel over stdin, never on the command line or in the environment.
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"navidrome-discord-status/bridge/internal/presence"
)

const (
	shutdownTimeout = 5 * time.Second
	maxLineSize     = 64 << 10
)

type command struct {
	Op       string             `json:"op"`
	Token    string             `json:"token,omitempty"`
	Activity *presence.Activity `json:"activity,omitempty"`
}

type event struct {
	Event   string `json:"event"`
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Process is a running worker. It implements presence.Worker.
type Process struct {
	cmd   *exec.Cmd
	log   *slog.Logger
	mu    sync.Mutex
	stdin io.WriteCloser
	done  chan struct{}
	// closing is set once Close was called, so the expected disconnect isn't
	// reported as a problem.
	closing atomic.Bool
}

// Start launches the worker binary for one user and hands it the access token.
func Start(path, appID, accessToken string, log *slog.Logger) (*Process, error) {
	cmd := exec.Command(path, appID)
	cmd.Env = []string{"LANG=C.UTF-8"}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting worker: %w", err)
	}

	p := &Process{cmd: cmd, log: log, stdin: stdin, done: make(chan struct{})}

	var output sync.WaitGroup
	output.Add(2)
	go func() { defer output.Done(); p.readEvents(stdout) }()
	go func() { defer output.Done(); p.readStderr(stderr) }()
	go func() {
		output.Wait()
		err := cmd.Wait()
		log.Info("worker exited", "error", err)
		close(p.done)
	}()

	if err := p.send(command{Op: "connect", Token: accessToken}); err != nil {
		p.kill()
		return nil, err
	}
	return p, nil
}

func (p *Process) SetActivity(activity presence.Activity) error {
	return p.send(command{Op: "activity", Activity: &activity})
}

func (p *Process) Clear() error {
	return p.send(command{Op: "clear"})
}

func (p *Process) UpdateToken(accessToken string) error {
	return p.send(command{Op: "token", Token: accessToken})
}

func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Close asks the worker to clear the status and exit, and kills it if it
// doesn't exit in time.
func (p *Process) Close() error {
	p.closing.Store(true)
	if err := p.send(command{Op: "shutdown"}); err != nil {
		p.kill()
		return nil
	}
	select {
	case <-p.done:
	case <-time.After(shutdownTimeout):
		p.log.Warn("worker did not exit in time, killing it")
		p.kill()
	}
	return nil
}

func (p *Process) send(cmd command) error {
	line, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return errors.New("worker has exited")
	default:
	}
	if _, err := p.stdin.Write(line); err != nil {
		return fmt.Errorf("writing to worker: %w", err)
	}
	return nil
}

func (p *Process) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *Process) readEvents(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 4096), maxLineSize)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			p.log.Warn("unreadable worker output", "error", err)
			continue
		}
		p.logEvent(ev)
	}
}

func (p *Process) logEvent(ev event) {
	switch ev.Event {
	case "ready":
		p.log.Info("worker connected to Discord")
	case "disconnected":
		if p.closing.Load() {
			p.log.Info("worker disconnected from Discord")
		} else {
			p.log.Warn("worker lost its Discord connection", "error", ev.Error)
		}
	case "error":
		p.log.Warn("worker command failed", "error", ev.Error)
	case "log":
		level := slog.LevelInfo
		if ev.Level == "warning" || ev.Level == "error" {
			level = slog.LevelWarn
		}
		p.log.Log(context.Background(), level, "worker: "+ev.Message)
	default:
		p.log.Warn("unknown worker event", "event", ev.Event)
	}
}

func (p *Process) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 4096), maxLineSize)
	for scanner.Scan() {
		p.log.Warn("worker stderr", "line", scanner.Text())
	}
}
