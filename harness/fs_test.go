package harness

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	"github.com/cloudwego/eino/adk/filesystem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeFSServer is a minimal builderv1.FileSystemWrapperServer: it records
// whatever Write/Edit sends it and acks each one individually (WriteAck/
// EditAck, correlated by FileId/RequestId - see build.proto's own doc
// comment on why one ack per file/edit, not one for the whole long-lived
// stream) -- enough to prove fsysWrapper's client side actually behaves
// like a well-formed gRPC client, without needing a real compile machine.
type fakeFSServer struct {
	builderv1.UnimplementedFileSystemWrapperServer

	writtenPath string
	writtenData []byte

	// writeClosedC/editClosedC close once their respective handler
	// returns (the stream itself ending, not any one ack) -- used only by
	// TestFileSystemClosesStreamsWhenContextIsCancelled.
	writeClosedC chan struct{}
	editClosedC  chan struct{}
}

func (s *fakeFSServer) Write(stream builderv1.FileSystemWrapper_WriteServer) error {
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
		case *builderv1.WriteRequest_Header:
			s.writtenPath = x.Header.Path
		case *builderv1.WriteRequest_Blob:
			if len(x.Blob.Blob) == 0 {
				// empty blob is BatchWrite's own end-of-file terminator -
				// ack it now, correlated by FileId.
				s.writtenData = buf.Bytes()
				if err := stream.Send(&builderv1.WriteAck{FileId: x.Blob.FileId}); err != nil {
					return err
				}
				continue
			}
			buf.Write(x.Blob.Blob)
		}
	}
	if s.writeClosedC != nil {
		close(s.writeClosedC)
	}
	return nil
}

func (s *fakeFSServer) Edit(stream builderv1.FileSystemWrapper_EditServer) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := stream.Send(&builderv1.EditAck{RequestId: msg.RequestId}); err != nil {
			return err
		}
	}
	if s.editClosedC != nil {
		close(s.editClosedC)
	}
	return nil
}

// dialFakeFSServer starts srv in-process over bufconn and returns a
// *grpc.ClientConn dialed to it, cleaned up via t.Cleanup.
func dialFakeFSServer(t *testing.T, srv *fakeFSServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	builderv1.RegisterFileSystemWrapperServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial fake fs server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestWriteSendsFullContentWithoutPrematureEOFError is a regression test
// for several bugs fixed together: Write used to open its own stream and
// send a duplicate header instead of any file content; the BatchWrite
// path it now delegates through used to never advance its own
// bytes-written counter, so it always reported "premature EOF" for any
// non-empty file even though the content had actually been sent in full;
// and Write had no way to know its file was actually applied before
// returning at all (the shared stream's only per-stream Empty ack is
// sent once, long after any individual caller's own file was written) -
// a real, live-confirmed race: a Write followed immediately by a Read of
// the same file could see stale (pre-write) content. Write now blocks on
// the matching WriteAck, so by the time it returns here, the server has
// already seen and applied this exact file - no cancel/wait dance needed
// to observe that.
func TestWriteSendsFullContentWithoutPrematureEOFError(t *testing.T) {
	srv := &fakeFSServer{}
	conn := dialFakeFSServer(t, srv)

	cm := &compileMachine{id: 7, mm: &machineManager{conn: conn}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fsw, err := cm.FileSystem(ctx)
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	const content = "fn main() {}\n"
	if err := fsw.Write(ctx, &filesystem.WriteRequest{FilePath: "src/main.rs", Content: content}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if srv.writtenPath != "src/main.rs" {
		t.Fatalf("server saw path %q, want src/main.rs", srv.writtenPath)
	}
	if got := string(srv.writtenData); got != content {
		t.Fatalf("server saw content %q, want %q", got, content)
	}
}

// TestFileSystemClosesStreamsWhenContextIsCancelled confirms the
// edit/write streams actually get finalized (CloseAndRecv) once ctx
// ends, rather than leaking open forever -- the fake server's Edit
// handler only returns (closing editClosedC) once it sees the client
// half-close its stream.
func TestFileSystemClosesStreamsWhenContextIsCancelled(t *testing.T) {
	srv := &fakeFSServer{editClosedC: make(chan struct{})}
	conn := dialFakeFSServer(t, srv)

	cm := &compileMachine{id: 1, mm: &machineManager{conn: conn}}
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := cm.FileSystem(ctx); err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	cancel()

	select {
	case <-srv.editClosedC:
	case <-time.After(2 * time.Second):
		t.Fatal("edit stream was never closed after ctx was cancelled")
	}
}
