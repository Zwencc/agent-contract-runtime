package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

// Tests never talk to a daemon the developer may be running.
func TestMain(m *testing.M) {
	os.Setenv("ACR_DAEMON", "off")
	os.Exit(m.Run())
}

// startTestDaemon serves hooks from this process on a private socket.
func startTestDaemon(t *testing.T) *daemonServer {
	t.Helper()
	directory, err := os.MkdirTemp("", "acrd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	os.Chmod(directory, 0o700)
	t.Setenv("ACR_DAEMON_SOCKET", filepath.Join(directory, "daemon.sock"))
	t.Setenv("ACR_DAEMON", "")
	listener, err := listenDaemon(daemonSocket())
	if err != nil {
		t.Fatal(err)
	}
	server := newDaemonServer()
	done := make(chan struct{})
	go func() { server.serve(listener); close(done) }()
	t.Cleanup(func() { server.shutdown(); <-done })
	return server
}

func TestHooksRunInTheDaemonWithTheClientsEnvironment(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	server := startTestDaemon(t)
	t.Setenv("ACR_LAUNCH_ID", "launch-7")
	before := os.Getenv("ACR_UNATTENDED")

	decision, _, _ := hookDecision(t, "claude", root, config, paths, "Bash", map[string]string{"command": "acr contract activate x.json --apply"}, "default")
	if decision != "deny" || server.served.Load() != 1 {
		t.Fatalf("decision = %q, served = %d", decision, server.served.Load())
	}
	if os.Getenv("ACR_UNATTENDED") != before || os.Getenv("ACR_LAUNCH_ID") != "launch-7" {
		t.Fatal("the daemon left a request's environment behind")
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.List(context.Background(), config.ProjectID, 0, 10)
	if err != nil || len(events) == 0 || events[0].Actor.ACRSessionID != "launch-7" {
		t.Fatalf("events = %+v, %v", events, err)
	}

	// Commands other than hooks run in the client.
	stdout.Reset()
	if code := run([]string{"version"}, strings.NewReader(""), &stdout, &stderr); code != 0 || server.served.Load() != 1 {
		t.Fatalf("version served by the daemon: %d", server.served.Load())
	}
	response, err := daemonCall(daemonRequest{Command: "run", Build: buildID(), Args: []string{"init", "--root", t.TempDir()}}, daemonRequestTimeout)
	if err != nil || response.Error == "" {
		t.Fatalf("daemon ran a non-hook command: %+v, %v", response, err)
	}
}

// Without a daemon, or with one from another build, hooks still run here.
func TestHooksFallBackWhenTheDaemonCannotRunThem(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACR_DAEMON", "")
	t.Setenv("ACR_DAEMON_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	if decision, _, _ := hookDecision(t, "codex", root, config, paths, "Bash", map[string]string{"command": "acr contract retire x --reason y --apply"}, "default"); decision != "deny" {
		t.Fatalf("no daemon: decision = %q", decision)
	}

	server := startTestDaemon(t)
	server.build = "an older build"
	if decision, _, _ := hookDecision(t, "codex", root, config, paths, "Bash", map[string]string{"command": "acr contract retire x --reason y --apply"}, "default"); decision != "deny" {
		t.Fatalf("stale daemon: decision = %q", decision)
	}
	if server.served.Load() != 0 {
		t.Fatal("a daemon from another build ran the hook")
	}
	select {
	case <-server.stop:
	default:
		t.Fatal("a daemon from another build kept running")
	}
}

func TestDaemonRefusesSharedDirectoryAndSecondInstance(t *testing.T) {
	shared := t.TempDir()
	os.Chmod(shared, 0o755)
	if _, err := listenDaemon(filepath.Join(shared, "daemon.sock")); err == nil || !strings.Contains(err.Error(), "mode 0700") {
		t.Fatalf("listen in a shared directory: %v", err)
	}
	startTestDaemon(t)
	if _, err := listenDaemon(daemonSocket()); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("second daemon: %v", err)
	}
	connection, err := net.Dial("unix", daemonSocket())
	if err != nil {
		t.Fatal(err)
	}
	connection.Write([]byte("not json\n"))
	var response daemonResponse
	json.NewDecoder(connection).Decode(&response)
	connection.Close()
	if !strings.Contains(response.Error, "invalid request") {
		t.Fatalf("garbage request answered with %+v", response)
	}
}

// The daemon reuses an open database until the file is replaced.
func TestDaemonStoreCacheReusesUntilReplaced(t *testing.T) {
	storeCache = newHandleCache()
	defer func() { storeCache.closeAll(); storeCache = nil }()
	path := filepath.Join(t.TempDir(), "events.db")
	first, release, err := openEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	again, _, err := openEvents(path)
	if err != nil || again != first {
		t.Fatalf("second open = %p, want the cached %p (%v)", again, first, err)
	}
	if _, err := first.Append(context.Background(), eventstore.AppendRequest{ProjectID: "p", OccurredAt: time.Now(), Type: "x", Actor: eventstore.Actor{Kind: "agent"}, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("a released cached store must stay usable: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}
	replaced, _, err := openEvents(path)
	if err != nil || replaced == first {
		t.Fatalf("a replaced database reused the old handle: %v", err)
	}
}
