package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"testing"
	"testing/fstest"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	machinev1 "git.noncepad.com/pkg/optimizer/gen/machine/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeMachineServer's Compile streams back one log line then one binary
// chunk, mirroring a real compile that both logs progress and exports the
// requested binaryPath. Create always allocates machine id 1, for tests
// that need Compiler() to succeed (e.g. TestCompilerUploadsStartingFilesViaUntar).
type fakeMachineServer struct {
	machinev1.UnimplementedMachineServer
}

func (fakeMachineServer) Compile(req *machinev1.CompileRequest, stream machinev1.Machine_CompileServer) error {
	if err := stream.Send(&machinev1.CompileResponse{Data: &machinev1.CompileResponse_Log{Log: &machinev1.CompileLog{Data: "compiling...\n"}}}); err != nil {
		return err
	}
	return stream.Send(&machinev1.CompileResponse{Data: &machinev1.CompileResponse_Binary{Binary: &machinev1.CompileBinary{
		Path: req.ExportPath[0],
		Data: []byte("not actually wasm, just test bytes"),
	}}})
}

func (fakeMachineServer) Create(req *machinev1.CreateRequest, stream machinev1.Machine_CreateServer) error {
	return stream.Send(&machinev1.CreateResponse{Data: &machinev1.CreateResponse_Id{Id: 1}})
}

// fakeUntarServer records the one Untar call Compiler's own upload step
// makes: the destination path from the request's header, and every
// regular file's name/content recovered by gunzipping+untarring the
// blob chunks sent after it.
type fakeUntarServer struct {
	builderv1.UnimplementedFileSystemWrapperServer

	gotPath  string
	gotFiles map[string]string
}

func (s *fakeUntarServer) Untar(stream builderv1.FileSystemWrapper_UntarServer) error {
	var buf bytes.Buffer
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch x := msg.Data.(type) {
		case *builderv1.UntarRequest_Header:
			s.gotPath = x.Header.Path
		case *builderv1.UntarRequest_Blob:
			if len(x.Blob) == 0 {
				continue // empty blob is UntarFS's own end-of-stream terminator
			}
			buf.Write(x.Blob)
		}
	}

	gz, err := gzip.NewReader(&buf)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	s.gotFiles = make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		s.gotFiles[hdr.Name] = string(data)
	}
	return stream.SendAndClose(&emptypb.Empty{})
}

// TestCompilerUploadsStartingFilesViaUntar is a regression test for
// Compiler accepting a startingFiles fs.FS and never doing anything with
// it - api.BotCoder.Compiler's own doc comment promises startingFiles
// "get copied into the base directory of the coding directory in the
// container", which nothing actually did until this wired UntarFS in.
func TestCompilerUploadsStartingFilesViaUntar(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	machinev1.RegisterMachineServer(s, fakeMachineServer{})
	untarServer := &fakeUntarServer{}
	builderv1.RegisterFileSystemWrapperServer(s, untarServer)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial fake server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	mm := &machineManager{conn: conn, manager: machinev1.NewMachineClient(conn), config: &Configuration{Timeout: 5 * time.Second}}
	startingFiles := fstest.MapFS{
		"Cargo.toml":  &fstest.MapFile{Data: []byte("[package]\nname = \"x\"\n")},
		"src/main.rs": &fstest.MapFile{Data: []byte("fn main() {}\n")},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := mm.Compiler(ctx, startingFiles); err != nil {
		t.Fatalf("Compiler: %v", err)
	}

	if untarServer.gotPath != ContainerWorkspace {
		t.Fatalf("Untar destination = %q, want %q", untarServer.gotPath, ContainerWorkspace)
	}
	want := map[string]string{
		"Cargo.toml":  "[package]\nname = \"x\"\n",
		"src/main.rs": "fn main() {}\n",
	}
	for name, content := range want {
		if got, ok := untarServer.gotFiles[name]; !ok || got != content {
			t.Errorf("uploaded file %q = %q (present=%v), want %q", name, got, ok, content)
		}
	}
}

// TestCompileDeliversExportedBinary is a regression test for
// CompileResponse_Binary previously being received and silently dropped --
// Compile's caller had no way to ever retrieve the exported artifact.
func TestCompileDeliversExportedBinary(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	machinev1.RegisterMachineServer(s, fakeMachineServer{})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial fake machine server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cm := &compileMachine{id: 1, mm: &machineManager{conn: conn, manager: machinev1.NewMachineClient(conn)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := cm.Compile(ctx, []string{"target/wasm32-wasip2/release/bot.wasm"})

	// The fake server sends exactly two data messages (one log line, one
	// binary chunk) before closing the stream -- read exactly that many,
	// then check the final error separately, rather than racing ErrorC
	// against the other two channels (ErrorC can become ready before
	// LogC/BinaryC are fully drained, since select among ready channels
	// is random).
	var gotBinary *api.CompileBinary
	var gotLog string
	for range 2 {
		select {
		case b := <-result.BinaryC:
			gotBinary = &b
		case line := <-result.LogC:
			gotLog += line
		case <-ctx.Done():
			t.Fatal("timed out waiting for compile result")
		}
	}
	select {
	case err := <-result.ErrorC:
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for compile result")
	}

	if gotLog != "compiling...\n" {
		t.Fatalf("log = %q, want %q", gotLog, "compiling...\n")
	}
	if gotBinary == nil {
		t.Fatal("never received the exported binary")
	}
	if gotBinary.Path != "target/wasm32-wasip2/release/bot.wasm" {
		t.Fatalf("binary path = %q, want target/wasm32-wasip2/release/bot.wasm", gotBinary.Path)
	}
	if string(gotBinary.Data) != "not actually wasm, just test bytes" {
		t.Fatalf("binary data = %q, want the test fixture bytes", string(gotBinary.Data))
	}
}
