package localdev

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/optimizer/harness"
	adkfs "github.com/cloudwego/eino/adk/filesystem"
)

// requireTool skips the test if name isn't on PATH - these tests shell
// out to the real cargo/rustfmt toolchain rather than faking it.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH: %v", name, err)
	}
}

// minimalCrate is just enough of a crate for `cargo build --target
// wasm32-wasip2`, `cargo clippy`, `cargo test`, and `cargo fmt`/`rustfmt`
// to all have something real to do.
func minimalCrate() fstest.MapFS {
	return fstest.MapFS{
		"Cargo.toml": &fstest.MapFile{Data: []byte(`[package]
name = "localdev_fixture"
version = "0.1.0"
edition = "2021"
`)},
		"src/lib.rs": &fstest.MapFile{Data: []byte(`pub fn add(a: i32, b: i32) -> i32 {
    a + b
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn it_adds() {
        assert_eq!(add(2, 2), 4);
    }
}
`)},
	}
}

func drainLog(t *testing.T, logC <-chan string) []string {
	t.Helper()
	var lines []string
	for line := range logC {
		lines = append(lines, line)
	}
	return lines
}

// lastLines returns at most the last n entries of lines, in order.
func lastLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

func TestCompilerEndToEnd(t *testing.T) {
	requireTool(t, "cargo")
	requireTool(t, "rustfmt")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	mm := NewMachineManager(t.TempDir())
	compilerIface, err := mm.Compiler(ctx, minimalCrate())
	if err != nil {
		t.Fatalf("Compiler: %v", err)
	}
	cm, ok := compilerIface.(*localCompileMachine)
	if !ok {
		t.Fatalf("Compiler returned %T, want *localCompileMachine", compilerIface)
	}
	defer func() {
		if err := cm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	t.Run("Test", func(t *testing.T) {
		res := cm.Test(ctx)
		lines := drainLog(t, res.LogC)
		if err := <-res.ErrorC; err != nil {
			// Test targets wasm32-wasip2, same as Compile - that needs a
			// [target.wasm32-wasip2] runner (e.g. wasmtime) configured in
			// cargo config, which this machine may not have even if
			// wasmtime itself is installed. That's an environment gap,
			// not a bug in this package, so skip rather than fail on its
			// specific signature instead of masking a real regression.
			if strings.Contains(strings.Join(lines, "\n"), "never executed") {
				t.Skipf("no wasm32-wasip2 cargo runner configured (e.g. wasmtime) - skipping: %v\nlog:\n%s", err, lines)
			}
			t.Fatalf("Test failed: %v\nlog:\n%s", err, lines)
		}
	})

	t.Run("Lint", func(t *testing.T) {
		res := cm.Lint(ctx)
		lines := drainLog(t, res.LogC)
		if err := <-res.ErrorC; err != nil {
			t.Fatalf("Lint failed: %v\nlog:\n%s", err, lines)
		}
	})

	t.Run("Format", func(t *testing.T) {
		if err := cm.Format(ctx, []string{"src/lib.rs"}); err != nil {
			t.Fatalf("Format: %v", err)
		}
	})

	t.Run("Compile", func(t *testing.T) {
		res := cm.Compile(ctx, []string{"target/wasm32-wasip2/release/liblocaldev_fixture.rlib"})
		var lines []string
		var binaries []api.CompileBinary
		logC, binaryC := res.LogC, res.BinaryC
		for logC != nil || binaryC != nil {
			select {
			case line, ok := <-logC:
				if !ok {
					logC = nil
					continue
				}
				lines = append(lines, line)
			case b, ok := <-binaryC:
				if !ok {
					binaryC = nil
					continue
				}
				binaries = append(binaries, b)
			}
		}
		t.Logf("last %d line(s) of cargo build output:\n%s", len(lastLines(lines, 20)), strings.Join(lastLines(lines, 20), "\n"))
		if err := <-res.ErrorC; err != nil {
			t.Fatalf("Compile failed: %v\nlog:\n%s", err, lines)
		}
		if len(binaries) != 1 {
			t.Fatalf("got %d binaries, want 1", len(binaries))
		}
		if len(binaries[0].Data) == 0 {
			t.Fatalf("binary %s is empty", binaries[0].Path)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		stdin, stdout, stderr := cm.Exec(ctx, "cat", []string{"Cargo.toml"}, nil)
		_ = stdin.Close()
		data, err := io.ReadAll(stdout)
		if err != nil {
			t.Fatalf("read stdout: %v", err)
		}
		if len(data) == 0 {
			t.Fatalf("expected Cargo.toml content on stdout, got nothing")
		}
		_, _ = io.ReadAll(stderr)
	})

	t.Run("FileSystem", func(t *testing.T) {
		fs, err := cm.FileSystem(ctx)
		if err != nil {
			t.Fatalf("FileSystem: %v", err)
		}

		if err := fs.Write(ctx, &adkfs.WriteRequest{FilePath: "NOTES.md", Content: "needle: hello\n"}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := fs.Read(ctx, &adkfs.ReadRequest{FilePath: "NOTES.md"})
		if err != nil || got.Content != "needle: hello\n" {
			t.Fatalf("Read = %q, %v; want %q, nil", got, err, "needle: hello\n")
		}

		if err := fs.Edit(ctx, &adkfs.EditRequest{FilePath: "NOTES.md", OldString: "hello", NewString: "world"}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		got, err = fs.Read(ctx, &adkfs.ReadRequest{FilePath: "NOTES.md"})
		if err != nil || got.Content != "needle: world\n" {
			t.Fatalf("Read after Edit = %q, %v; want %q, nil", got, err, "needle: world\n")
		}

		entries, err := fs.LsInfo(ctx, &adkfs.LsInfoRequest{Path: "."})
		if err != nil {
			t.Fatalf("LsInfo: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Path == "NOTES.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("LsInfo(.) = %+v, missing NOTES.md", entries)
		}

		matches, err := fs.GrepRaw(ctx, &adkfs.GrepRequest{Pattern: "needle"})
		if err != nil {
			t.Fatalf("GrepRaw: %v", err)
		}
		if len(matches) != 1 || matches[0].Path != "/NOTES.md" {
			t.Fatalf("GrepRaw = %+v, want exactly one match at /NOTES.md", matches)
		}

		globbed, err := fs.GlobInfo(ctx, &adkfs.GlobInfoRequest{Path: ".", Pattern: "*.md"})
		if err != nil {
			t.Fatalf("GlobInfo: %v", err)
		}
		if len(globbed) != 1 || globbed[0].Path != "NOTES.md" {
			t.Fatalf("GlobInfo = %+v, want exactly one match NOTES.md", globbed)
		}

		pr := fs.BatchRead(ctx, []*api.ReadHeader{{Path: "NOTES.md"}})
		if len(pr) != 1 {
			t.Fatalf("BatchRead returned %d readers, want 1", len(pr))
		}
		batchData, err := io.ReadAll(pr[0])
		if err != nil || string(batchData) != "needle: world\n" {
			t.Fatalf("BatchRead content = %q, %v; want %q, nil", batchData, err, "needle: world\n")
		}
	})
}

func TestCompileNonzeroExitWrapsSentinel(t *testing.T) {
	requireTool(t, "cargo")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A crate with a syntax error - cargo build must fail, and the
	// returned error must unwrap to harness.ErrCompileNonzeroExit.
	broken := fstest.MapFS{
		"Cargo.toml": &fstest.MapFile{Data: []byte(`[package]
name = "localdev_broken"
version = "0.1.0"
edition = "2021"
`)},
		"src/lib.rs": &fstest.MapFile{Data: []byte(`this is not valid rust`)},
	}

	mm := NewMachineManager(t.TempDir())
	compilerIface, err := mm.Compiler(ctx, broken)
	if err != nil {
		t.Fatalf("Compiler: %v", err)
	}
	cm := compilerIface.(*localCompileMachine)
	defer func() { _ = cm.Close() }()

	res := cm.Compile(ctx, nil)
	drainLog(t, res.LogC)
	for range res.BinaryC {
	}
	err = <-res.ErrorC
	if err == nil {
		t.Fatal("expected a compile error, got nil")
	}
	if !errors.Is(err, harness.ErrCompileNonzeroExit) {
		t.Errorf("error %v does not wrap harness.ErrCompileNonzeroExit", err)
	}
}
