package localdev

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/optimizer/harness"
)

// localMachineManager implements api.BotCoder directly against local
// subprocesses - see the package doc comment.
type localMachineManager struct {
	workingDir string
}

// NewMachineManager returns an api.BotCoder whose Compiler creates a
// fresh subdirectory of workingDir per call (see localCompileMachine.
// Close and Compiler's own ctx-tied cleanup below) and runs every
// operation against it with local subprocesses, instead of allocating a
// real machine over machinev1. workingDir must already exist.
func NewMachineManager(workingDir string) api.BotCoder {
	return &localMachineManager{workingDir: workingDir}
}

// Compiler creates a fresh machine-<random>/ subdirectory of mm.workingDir
// (one per call, never reused) and, if startingFiles is non-nil, copies
// it in. startingFiles is allowed to be nil - a caller that uploads its
// starting files separately afterward (e.g. cmd/localdevserver's own
// Create handler, which always allocates an empty machine and lets a
// later Untar call populate it, mirroring harness.Compiler's own client-
// side upload step) has nothing to pass here.
//
// The returned machine's directory is removed once ctx ends, in
// addition to (not instead of) the explicit localCompileMachine.Close a
// caller holding the concrete type can still call sooner - matching
// machine.proto's own "a disconnect triggers the machine to be deleted"
// contract for the real remote implementation (harness.Compiler), so a
// server built on this package needs no separate cleanup path beyond
// just letting the Create RPC's own context end.
func (mm *localMachineManager) Compiler(ctx context.Context, startingFiles fs.FS) (api.BotCompiler, error) {
	dir, err := os.MkdirTemp(mm.workingDir, "machine-*")
	if err != nil {
		return nil, fmt.Errorf("create working directory: %w", err)
	}
	if startingFiles != nil {
		if err := copyFSInto(dir, startingFiles); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("copy starting files: %w", err)
		}
	}
	go func() {
		<-ctx.Done()
		_ = os.RemoveAll(dir)
	}()
	return &localCompileMachine{dir: dir}, nil
}

// copyFSInto copies every entry of source into dir, creating parent
// directories as needed - the local-disk equivalent of fsysWrapper.
// UntarFS, with no tar/gzip roundtrip needed since there is no network
// boundary to cross.
func copyFSInto(dir string, source fs.FS) error {
	return fs.WalkDir(source, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		target := filepath.Join(dir, p)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(source, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if parent := filepath.Dir(target); parent != dir {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// localCompileMachine implements api.BotCompiler directly against dir
// (a real local directory) and local subprocesses - see the package doc
// comment.
type localCompileMachine struct {
	dir string
}

// Close removes dir. Not part of api.BotCompiler - os.MkdirTemp
// directories are not cleaned up automatically the way e.g. testing.T.
// TempDir's are, so a caller that knows it's holding a *localCompileMachine
// specifically (not just an api.BotCompiler) should call this once done,
// to avoid leaking a scratch directory per Compiler call.
func (cm *localCompileMachine) Close() error {
	return os.RemoveAll(cm.dir)
}

func (cm *localCompileMachine) FileSystem(ctx context.Context) (api.FilesystemWrapper, error) {
	return &localFileSystem{root: cm.dir}, nil
}

// runLogged runs name with args in cm.dir, streaming combined stdout+
// stderr line-by-line into logC (closed before this returns, whether or
// not the command succeeded - see api.LogResult's own doc comment) and
// returning a non-nil error (wrapping both wrapErr - one of harness's own
// ErrXNonzeroExit sentinels - and the underlying *exec.ExitError) if it
// exits non-zero or never starts at all.
func (cm *localCompileMachine) runLogged(ctx context.Context, logC chan<- string, wrapErr error, name string, args ...string) error {
	defer close(logC)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cm.dir
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		doneC := ctx.Done()
		for scanner.Scan() {
			select {
			case <-doneC:
				return
			case logC <- scanner.Text():
			}
		}
	}()

	waitErr := cmd.Wait()
	_ = pw.Close()
	<-scanDone
	if waitErr != nil {
		return fmt.Errorf("%w: %w", wrapErr, waitErr)
	}
	return nil
}

// Compile runs `cargo build --release --target wasm32-wasip2` (matching
// gitlab.noncepad.com/eflam/qemu-pool's own cmd/smoketest/test/0-rust.sh,
// the one concrete reference this package has for the real build
// command), streaming its combined output into LogC and, on a clean
// exit, reading each requested binaryPath (crate-relative) off local
// disk into BinaryC.
func (cm *localCompileMachine) Compile(ctx context.Context, binaryPath []string) api.LogResult {
	logC := make(chan string, 100)
	binaryC := make(chan api.CompileBinary, 10)
	errorC := make(chan error, 1)
	go func() {
		defer close(binaryC)
		if err := cm.runLogged(ctx, logC, harness.ErrCompileNonzeroExit, "cargo", "build", "--release", "--target", "wasm32-wasip2"); err != nil {
			errorC <- err
			return
		}
		for _, p := range binaryPath {
			data, err := os.ReadFile(filepath.Join(cm.dir, p))
			if err != nil {
				errorC <- fmt.Errorf("read compiled binary %s: %w", p, err)
				return
			}
			select {
			case <-ctx.Done():
				errorC <- ctx.Err()
				return
			case binaryC <- api.CompileBinary{Path: p, Data: data}:
			}
		}
		errorC <- nil
	}()
	return api.LogResult{LogC: logC, ErrorC: errorC, BinaryC: binaryC}
}

// Lint runs `cargo clippy --target wasm32-wasip2`.
func (cm *localCompileMachine) Lint(ctx context.Context) api.LogResult {
	logC := make(chan string, 100)
	errorC := make(chan error, 1)
	go func() {
		errorC <- cm.runLogged(ctx, logC, harness.ErrLintNonzeroExit, "cargo", "clippy", "--target", "wasm32-wasip2")
	}()
	return api.LogResult{LogC: logC, ErrorC: errorC}
}

// Test runs `cargo test --target wasm32-wasip2`, matching Compile's own
// target - this needs a wasm test runner configured (e.g. wasmtime via
// [target.wasm32-wasip2] runner = "..." in .cargo/config.toml); without
// one, cargo compiles the test binary but fails to run it.
func (cm *localCompileMachine) Test(ctx context.Context) api.LogResult {
	logC := make(chan string, 100)
	errorC := make(chan error, 1)
	go func() {
		errorC <- cm.runLogged(ctx, logC, harness.ErrTestNonzeroExit, "cargo", "test", "--target", "wasm32-wasip2")
	}()
	return api.LogResult{LogC: logC, ErrorC: errorC}
}

// Format runs `rustfmt <paths>` when paths is non-empty (not `cargo fmt
// -- <paths>`, which does not actually scope to the given paths - see
// api.BotCompiler.Format's own doc comment), or a whole-crate `cargo
// fmt` otherwise.
func (cm *localCompileMachine) Format(ctx context.Context, paths []string) error {
	var cmd *exec.Cmd
	if len(paths) > 0 {
		cmd = exec.CommandContext(ctx, "rustfmt", paths...)
	} else {
		cmd = exec.CommandContext(ctx, "cargo", "fmt")
	}
	cmd.Dir = cm.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %w (%s)", harness.ErrFmtNonzeroExit, err, out)
	}
	return nil
}

// Exec runs command as a local subprocess in cm.dir, wiring the returned
// pipes directly to its stdin/stdout/stderr - the local-disk equivalent
// of compileMachine.Exec's gRPC-streamed version, with no wire protocol
// in between.
func (cm *localCompileMachine) Exec(ctx context.Context, command string, args []string, mEnv map[string]string) (io.WriteCloser, io.ReadCloser, io.ReadCloser) {
	stdinRead, stdinWrite := io.Pipe()
	stdoutRead, stdoutWrite := io.Pipe()
	stderrRead, stderrWrite := io.Pipe()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cm.dir
	if len(mEnv) > 0 {
		env := os.Environ()
		for k, v := range mEnv {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	cmd.Stdin = stdinRead
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite

	if err := cmd.Start(); err != nil {
		_ = stdoutWrite.CloseWithError(err)
		_ = stderrWrite.CloseWithError(err)
		return stdinWrite, stdoutRead, stderrRead
	}
	go func() {
		err := cmd.Wait()
		_ = stdoutWrite.CloseWithError(err)
		_ = stderrWrite.CloseWithError(err)
	}()
	return stdinWrite, stdoutRead, stderrRead
}
