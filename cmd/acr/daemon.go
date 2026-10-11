package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maximumDaemonInput   = 4 << 20
	daemonDialTimeout    = 200 * time.Millisecond
	daemonRequestTimeout = 60 * time.Second
)

// daemonEnv are the variables that change what a hook records. The client
// sends its values, and the daemon applies them for that request only.
var daemonEnv = []string{"ACR_LAUNCH_ID", "ACR_UNATTENDED"}

type daemonRequest struct {
	Command string            `json:"command"`
	Build   string            `json:"build"`
	Args    []string          `json:"args,omitempty"`
	Stdin   []byte            `json:"stdin,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type daemonResponse struct {
	Stdout []byte `json:"stdout,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	Code   int    `json:"code"`
	Stale  bool   `json:"stale,omitempty"`
	Error  string `json:"error,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Build  string `json:"build,omitempty"`
	Served uint64 `json:"served,omitempty"`
}

// daemonSocket is where the user's daemon listens: ACR_DAEMON_SOCKET, else a
// directory only the user can open.
func daemonSocket() string {
	if path := os.Getenv("ACR_DAEMON_SOCKET"); path != "" {
		return path
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "acr", "daemon.sock")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "acr-"+strconv.Itoa(os.Getuid()), "daemon.sock")
	}
	return filepath.Join(home, ".acr", "run", "daemon.sock")
}

// buildID identifies the running acr binary, so a daemon started from an
// older build never answers for a newer one.
func buildID() string {
	executable, err := os.Executable()
	if err != nil {
		return version
	}
	info, err := os.Stat(executable)
	if err != nil {
		return version + ":" + executable
	}
	return fmt.Sprintf("%s:%s:%d:%d", version, executable, info.Size(), info.ModTime().UnixNano())
}

func daemonCall(request daemonRequest, timeout time.Duration) (daemonResponse, error) {
	connection, err := net.DialTimeout("unix", daemonSocket(), daemonDialTimeout)
	if err != nil {
		return daemonResponse{}, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return daemonResponse{}, err
	}
	var response daemonResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return daemonResponse{}, err
	}
	return response, nil
}

// forwardHook runs a hook in the daemon. ok is false when the daemon did not
// run it, and the caller then runs it itself.
func forwardHook(args []string, stdin []byte, stdout, stderr io.Writer) (int, bool) {
	env := map[string]string{}
	for _, name := range daemonEnv {
		if value, set := os.LookupEnv(name); set {
			env[name] = value
		}
	}
	response, err := daemonCall(daemonRequest{Command: "run", Build: buildID(), Args: args, Stdin: stdin, Env: env}, daemonRequestTimeout)
	if err != nil || response.Stale || response.Error != "" {
		return 0, false
	}
	stdout.Write(response.Stdout)
	stderr.Write(response.Stderr)
	return response.Code, true
}

// daemonServer runs hooks for clients. Hooks read process environment, so
// requests run one at a time with the client's variables applied.
type daemonServer struct {
	build  string
	mutex  sync.Mutex
	served atomic.Uint64
	stop   chan struct{}
	once   sync.Once
}

func newDaemonServer() *daemonServer {
	return &daemonServer{build: buildID(), stop: make(chan struct{})}
}

func (server *daemonServer) shutdown() { server.once.Do(func() { close(server.stop) }) }

func (server *daemonServer) serve(listener net.Listener) error {
	go func() {
		<-server.stop
		listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			select {
			case <-server.stop:
				return nil
			default:
				return err
			}
		}
		go server.handle(connection)
	}
}

func (server *daemonServer) handle(connection net.Conn) {
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(daemonRequestTimeout))
	var request daemonRequest
	if err := json.NewDecoder(io.LimitReader(connection, 2*maximumDaemonInput)).Decode(&request); err != nil {
		json.NewEncoder(connection).Encode(daemonResponse{Error: "invalid request: " + err.Error()})
		return
	}
	json.NewEncoder(connection).Encode(server.answer(request))
}

func (server *daemonServer) answer(request daemonRequest) (response daemonResponse) {
	response.PID, response.Build = os.Getpid(), server.build
	switch request.Command {
	case "ping":
		response.Served = server.served.Load()
		return response
	case "stop":
		server.shutdown()
		return response
	case "run":
	default:
		response.Error = "unknown command " + strconv.Quote(request.Command)
		return response
	}
	if request.Build != server.build {
		// The binary was replaced: let the client run the new code, and
		// make room for a daemon started from it.
		response.Stale = true
		server.shutdown()
		return response
	}
	if len(request.Args) == 0 || request.Args[0] != "hook" {
		response.Error = "the daemon only runs hooks"
		return response
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			response = daemonResponse{Error: fmt.Sprint("hook panicked: ", recovered)}
		}
	}()
	server.mutex.Lock()
	defer server.mutex.Unlock()
	restore := map[string]*string{}
	for _, name := range daemonEnv {
		if value, set := os.LookupEnv(name); set {
			restore[name] = &value
		} else {
			restore[name] = nil
		}
		if value, set := request.Env[name]; set {
			os.Setenv(name, value)
		} else {
			os.Unsetenv(name)
		}
	}
	defer func() {
		for name, value := range restore {
			if value == nil {
				os.Unsetenv(name)
			} else {
				os.Setenv(name, *value)
			}
		}
	}()
	var stdout, stderr bytes.Buffer
	response.Code = runCommand(request.Args, bytes.NewReader(request.Stdin), &stdout, &stderr)
	response.Stdout, response.Stderr = stdout.Bytes(), stderr.Bytes()
	server.served.Add(1)
	return response
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: acr daemon run|start|stop|status")
		return 2
	}
	socket := daemonSocket()
	switch args[0] {
	case "run":
		listener, err := listenDaemon(socket)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer os.Remove(socket)
		fmt.Fprintf(stdout, "acr daemon %d listening on %s\n", os.Getpid(), socket)
		storeCache = newHandleCache()
		defer storeCache.closeAll()
		if err := newDaemonServer().serve(listener); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "start":
		pid, started, err := ensureDaemon()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !started {
			fmt.Fprintf(stdout, "acr daemon already running (pid %d)\n", pid)
			return 0
		}
		fmt.Fprintf(stdout, "acr daemon started (pid %d, %s)\n", pid, socket)
		return 0
	case "stop":
		if _, err := daemonCall(daemonRequest{Command: "stop"}, time.Second); err != nil {
			fmt.Fprintln(stdout, "acr daemon is not running")
			return 0
		}
		if !waitDaemonGone(3 * time.Second) {
			fmt.Fprintln(stderr, "acr daemon did not stop")
			return 1
		}
		fmt.Fprintln(stdout, "acr daemon stopped")
		return 0
	case "status":
		response, err := daemonCall(daemonRequest{Command: "ping"}, time.Second)
		if err != nil {
			fmt.Fprintf(stdout, "acr daemon is not running (%s); hooks run directly\n", socket)
			return 3
		}
		state := "current"
		if response.Build != buildID() {
			state = "from an older acr build; it exits on the next hook"
		}
		fmt.Fprintf(stdout, "acr daemon running (pid %d, %s, %d hooks served, %s)\n", response.PID, socket, response.Served, state)
		return 0
	}
	fmt.Fprintln(stderr, "usage: acr daemon run|start|stop|status")
	return 2
}

// listenDaemon listens on socket inside a directory only the user can use,
// replacing a socket file a dead daemon left behind.
func listenDaemon(socket string) (net.Listener, error) {
	directory := filepath.Dir(socket)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := checkPrivateDirectory(directory); err != nil {
		return nil, err
	}
	if connection, err := net.DialTimeout("unix", socket, daemonDialTimeout); err == nil {
		connection.Close()
		return nil, fmt.Errorf("an acr daemon is already listening on %s", socket)
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", socket)
}

// ensureDaemon makes sure a daemon from this build is running, replacing
// one from another build. started reports whether it had to start one.
func ensureDaemon() (int, bool, error) {
	if response, err := daemonCall(daemonRequest{Command: "ping"}, time.Second); err == nil {
		if response.Build == buildID() {
			return response.PID, false, nil
		}
		daemonCall(daemonRequest{Command: "stop"}, time.Second)
		waitDaemonGone(2 * time.Second)
	}
	pid, err := startDaemon(daemonSocket())
	return pid, err == nil, err
}

func startDaemon(socket string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(filepath.Join(filepath.Dir(socket), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	command := exec.Command(executable, "daemon", "run")
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = append(os.Environ(), "ACR_DAEMON_SOCKET="+socket)
	detach(command)
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	command.Process.Release()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if response, err := daemonCall(daemonRequest{Command: "ping"}, time.Second); err == nil && response.PID == pid {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("acr daemon did not start; see %s", filepath.Join(filepath.Dir(socket), "daemon.log"))
}

func waitDaemonGone(timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := daemonCall(daemonRequest{Command: "ping"}, 200*time.Millisecond); err != nil {
			return true
		}
	}
	return false
}
