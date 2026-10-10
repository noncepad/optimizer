// Command localdevserver spins up a real machinev1.Machine +
// builderv1.FileSystemWrapper gRPC server backed entirely by local
// directories and local subprocesses (harness/localdev), instead of real
// Firecracker guests (gitlab.noncepad.com/eflam/qemu-pool). It exists so
// harness.CreateManager's client side - and everything built on top of
// it, e.g. fund-agent's run_pipeline/build_strategy - can be exercised
// against a real gRPC server during local development without a qemu-pool
// deployment.
//
// Each machine Create allocates gets its own fresh subdirectory of
// Configuration.WorkingDir (localdev.NewMachineManager); the directory is
// removed automatically once that machine's Create stream ends (see
// coderServer.Create's own doc comment).
//
// There is no VM-level isolation here - Compile/Lint/Format/Test/Exec run
// as ordinary local subprocesses on whatever host runs this binary (same
// requirement/caveat as harness/localdev itself: a real cargo/rustfmt
// toolchain must already be on PATH). Treat this as a developer-trusted
// convenience, not a sandbox - never point a real qemu-pool client at
// this for anything you wouldn't run directly on your own machine.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	machinev1 "git.noncepad.com/pkg/optimizer/gen/machine/v1"
	"git.noncepad.com/pkg/optimizer/harness/localdev"
	"google.golang.org/grpc"
)

// Configuration is this server's one JSON config file.
type Configuration struct {
	// WorkingDir is the parent directory every allocated machine gets its
	// own fresh subdirectory under - created if it doesn't already exist.
	WorkingDir string `json:"working_dir"`
	// Addr is the address this server listens on for both the Machine and
	// FileSystemWrapper services - "host:port" for TCP, or
	// "unix:///path/to.sock" for a Unix socket.
	Addr string `json:"addr"`
}

func main() {
	configPath := flag.String("config", "", "path to a JSON Configuration file (required)")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("localdevserver: -config is required")
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("localdevserver: read config: %s", err)
	}
	var cfg Configuration
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("localdevserver: parse config: %s", err)
	}
	if cfg.WorkingDir == "" {
		log.Fatal("localdevserver: config.working_dir is required")
	}
	if cfg.Addr == "" {
		log.Fatal("localdevserver: config.addr is required")
	}
	if err := os.MkdirAll(cfg.WorkingDir, 0o755); err != nil {
		log.Fatalf("localdevserver: create working directory: %s", err)
	}

	lis, err := listen(cfg.Addr)
	if err != nil {
		log.Fatalf("localdevserver: listen on %s: %s", cfg.Addr, err)
	}

	srv := newCoderServer(localdev.NewMachineManager(cfg.WorkingDir))
	s := grpc.NewServer()
	machinev1.RegisterMachineServer(s, srv)
	builderv1.RegisterFileSystemWrapperServer(s, srv)

	// SIGINT/SIGTERM trigger GracefulStop rather than letting the process
	// die mid-signal - every live machine's own Create stream then
	// observes its context end and runs localdev's own ctx-tied cleanup
	// (removing its directory) before this process actually exits.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()

	log.Printf("localdevserver: serving Machine/FileSystemWrapper on %s, machines under %s", cfg.Addr, cfg.WorkingDir)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("localdevserver: serve: %s", err)
	}
}

// listen supports "unix:///path" and plain "host:port" (TCP) addresses.
func listen(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
		return net.Listen("unix", path)
	}
	return net.Listen("tcp", addr)
}
