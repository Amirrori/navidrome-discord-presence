package worker

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navidrome-discord-status/bridge/internal/presence"
)

// fakeWorker records its arguments and every command it receives, and exits
// on "shutdown", like the real worker.
const fakeWorker = `#!/bin/sh
echo "$@" > "${0%/*}/args"
echo '{"event":"ready"}'
while IFS= read -r line; do
	printf '%s\n' "$line" >> "${0%/*}/commands"
	case "$line" in
		*'"op":"shutdown"'*) exit 0 ;;
	esac
done
`

func startFake(t *testing.T) (*Process, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.sh")
	if err := os.WriteFile(path, []byte(fakeWorker), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := Start(path, "123", "secret-access-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return p, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTokenGoesOverStdinOnly(t *testing.T) {
	p, dir := startFake(t)
	if err := p.SetActivity(presence.Activity{Details: "Undone", State: "Weezer"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	args := readFile(t, filepath.Join(dir, "args"))
	if strings.Contains(args, "secret-access-token") {
		t.Error("the access token must not be passed as an argument")
	}
	if strings.TrimSpace(args) != "123" {
		t.Errorf("args = %q, want only the application ID", args)
	}

	commands := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "commands"))), "\n")
	want := []string{
		`{"op":"connect","token":"secret-access-token"}`,
		`{"op":"activity","activity":{"details":"Undone","state":"Weezer"}}`,
		`{"op":"shutdown"}`,
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %q", commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Errorf("command %d = %s, want %s", i, commands[i], want[i])
		}
	}
}

func TestDoneClosesWhenWorkerExits(t *testing.T) {
	p, _ := startFake(t)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done was not closed after the worker exited")
	}
	if err := p.Clear(); err == nil {
		t.Error("commands to an exited worker must fail")
	}
}
