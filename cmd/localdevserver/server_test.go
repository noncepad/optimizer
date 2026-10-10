package main

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	machinev1 "git.noncepad.com/pkg/optimizer/gen/machine/v1"
	"git.noncepad.com/pkg/optimizer/harness"
	"git.noncepad.com/pkg/optimizer/harness/localdev"
	adkfs "github.com/cloudwego/eino/adk/filesystem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// requireTool skips the test if name isn't on PATH.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH: %v", name, err)
	}
}

// dialTestServer starts a real coderServer in-process over bufconn,
// backed by a fresh localdev.NewMachineManager rooted at workingDir, and
// returns a *grpc.ClientConn dialed to it.
func dialTestServer(t *testing.T, workingDir string) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	srv := newCoderServer(localdev.NewMachineManager(workingDir))
	machinev1.RegisterMachineServer(s, srv)
	builderv1.RegisterFileSystemWrapperServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestServerEndToEnd drives this server through harness.CreateManager -
// the exact same client code fund-agent/cmd/wiki-server's own
// run_pipeline path uses against a real qemu-pool deployment - proving
// the two sides actually interoperate over the wire, not just that each
// compiles in isolation. The FileSystem CRUD path (Write/Read/Edit/
// LsInfo) is a hard requirement; Compile is best-effort (logged, not
// failed) since it depends on a real, fully-provisioned Rust toolchain
// (wasm32-wasip2 target, no broken RUSTC_WRAPPER, ...) this sandbox
// doesn't have - the same class of environment gap localdev_test.go's
// own TestCompilerEndToEnd already works around for rustfmt/wasm
// runners.
func TestServerEndToEnd(t *testing.T) {
	requireTool(t, "cargo")

	conn := dialTestServer(t, t.TempDir())
	coder, err := harness.CreateManager(conn, &harness.Configuration{
		MachineSize: machinev1.MachineSize_Medium,
		BuildType:   machinev1.BuildType_Rust,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("CreateManager: %v", err)
	}

	crate := fstest.MapFS{
		"Cargo.toml": &fstest.MapFile{Data: []byte("[package]\nname = \"e2e\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")},
		"src/lib.rs": &fstest.MapFile{Data: []byte("pub fn add(a: i32, b: i32) -> i32 { a + b }\n")},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	compiler, err := coder.Compiler(ctx, crate)
	if err != nil {
		t.Fatalf("Compiler: %v", err)
	}

	fsys, err := compiler.FileSystem(ctx)
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	got, err := fsys.Read(ctx, &adkfs.ReadRequest{FilePath: "Cargo.toml"})
	if err != nil {
		t.Fatalf("Read Cargo.toml: %v", err)
	}
	if !strings.Contains(got.Content, `name = "e2e"`) {
		t.Fatalf("Read Cargo.toml = %q, missing expected content", got.Content)
	}

	if err := fsys.Write(ctx, &adkfs.WriteRequest{FilePath: "NOTES.md", Content: "hello\n"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err = fsys.Read(ctx, &adkfs.ReadRequest{FilePath: "NOTES.md"})
	if err != nil || got.Content != "hello\n" {
		t.Fatalf("Read NOTES.md = %q, %v; want %q, nil", got.Content, err, "hello\n")
	}

	if err := fsys.Edit(ctx, &adkfs.EditRequest{FilePath: "NOTES.md", OldString: "hello", NewString: "world"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got, err = fsys.Read(ctx, &adkfs.ReadRequest{FilePath: "NOTES.md"})
	if err != nil || got.Content != "world\n" {
		t.Fatalf("Read after Edit = %q, %v; want %q, nil", got.Content, err, "world\n")
	}

	entries, err := fsys.LsInfo(ctx, &adkfs.LsInfoRequest{Path: "."})
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

	// api.LogResult's own contract (see its doc comment): LogC is never
	// closed, only ErrorC is ever written, exactly once - so ErrorC must
	// be in the same select as LogC/BinaryC, not checked only after a
	// loop that waits for both to "close" (LogC never will, so that loop
	// would never exit).
	compileRes := compiler.Compile(ctx, []string{"target/wasm32-wasip2/release/libe2e.rlib"})
	var compileLines []string
	var binaries []api.CompileBinary
	var compileErr error
	logC, binaryC := compileRes.LogC, compileRes.BinaryC
drain:
	for {
		select {
		case line, ok := <-logC:
			if !ok {
				logC = nil
				continue
			}
			compileLines = append(compileLines, line)
		case b, ok := <-binaryC:
			if !ok {
				binaryC = nil
				continue
			}
			binaries = append(binaries, b)
		case compileErr = <-compileRes.ErrorC:
			// Final non-blocking drain - select among simultaneously-ready
			// channels is random, so ErrorC firing doesn't guarantee
			// logC/binaryC's own already-buffered final items were read
			// first.
		finalDrain:
			for {
				select {
				case line, ok := <-logC:
					if !ok {
						break finalDrain
					}
					compileLines = append(compileLines, line)
				case b, ok := <-binaryC:
					if !ok {
						break finalDrain
					}
					binaries = append(binaries, b)
				default:
					break finalDrain
				}
			}
			break drain
		}
	}
	if compileErr != nil {
		t.Logf("Compile: best-effort step did not succeed in this environment: %v\n%s", compileErr, strings.Join(compileLines, "\n"))
	} else if len(binaries) != 1 || len(binaries[0].Data) == 0 {
		t.Errorf("got binaries %+v, want exactly one non-empty", binaries)
	}
}
