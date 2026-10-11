package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/agent-contract-runtime/acr/internal/console"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	webassets "github.com/agent-contract-runtime/acr/web"
)

func runUI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	listen := flags.String("listen", "127.0.0.1:7480", "loopback listen address")
	noOpen := flags.Bool("no-open", false, "do not open a browser")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ui accepts no positional arguments")
		return 2
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !loopbackListenHost(host) {
		fmt.Fprintln(stderr, "ui --listen must be a loopback host and port, for example 127.0.0.1:7480")
		return 2
	}
	assets := webassets.Dist()
	if err := console.EnsureAssets(assets); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	server, err := console.New(*root, assets)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer server.Close()
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	// The console reconciles the workspace the same way session start and
	// acr reconcile accept do.
	server.UseWorkspace(console.Workspace{
		Scan: func(ctx context.Context) error {
			return reconcileWorkspaceCoverage(ctx, store, paths.Root, config.ProjectID, config.WorkspaceID, "")
		},
		Resolve: func(ctx context.Context, gapEventID, resolution, reason string) (eventstore.Event, error) {
			return acceptCoverageGap(ctx, store, config, paths, gapEventID, resolution, reason)
		},
	})
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "listen for ACR Console: %v\n", err)
		return 1
	}
	address := "http://" + listener.Addr().String()
	fmt.Fprintf(stdout, "ACR Console: %s\n", address)
	if !*noOpen {
		go func() {
			time.Sleep(150 * time.Millisecond)
			openBrowser(address)
		}()
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(stderr, "serve ACR Console: %v\n", err)
		return 1
	}
	return 0
}

func loopbackListenHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func openBrowser(address string) {
	command := "xdg-open"
	args := []string{address}
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", address}
	}
	exec.Command(command, args...).Start()
}
