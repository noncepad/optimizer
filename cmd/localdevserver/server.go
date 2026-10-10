package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os/exec"
	"path"
	"sync"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	machinev1 "git.noncepad.com/pkg/optimizer/gen/machine/v1"
	adkfs "github.com/cloudwego/eino/adk/filesystem"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// coderServer implements both machinev1.MachineServer and
// builderv1.FileSystemWrapperServer on top of a single api.BotCoder
// (localdev.NewMachineManager, in main.go - a real local directory +
// subprocesses standing in for a machine, not a VM) - the server-side
// mirror of what harness.CreateManager's client already speaks, so
// run_pipeline/build_strategy's whole coding-agent path can be exercised
// locally without a real qemu-pool deployment.
type coderServer struct {
	machinev1.UnimplementedMachineServer
	builderv1.UnimplementedFileSystemWrapperServer

	coder api.BotCoder

	mu       sync.Mutex
	nextID   uint32
	machines map[uint32]api.BotCompiler
}

func newCoderServer(coder api.BotCoder) *coderServer {
	return &coderServer{coder: coder, machines: make(map[uint32]api.BotCompiler)}
}

func (s *coderServer) getCompiler(id uint32) (api.BotCompiler, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.machines[id]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "machine %d not found", id)
	}
	return c, nil
}

func (s *coderServer) filesystemFor(ctx context.Context, machineID uint32) (api.FilesystemWrapper, error) {
	compiler, err := s.getCompiler(machineID)
	if err != nil {
		return nil, err
	}
	fsys, err := compiler.FileSystem(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open filesystem: %s", err)
	}
	return fsys, nil
}

// parseModifiedAt converts adkfs.FileInfo's own RFC3339Nano-formatted
// ModifiedAt string (see localdev's localFileSystem) to the proto's unix-
// seconds int64 - 0 (not an error) if it fails to parse, matching every
// other best-effort posture in the localdev package this server wraps.
func parseModifiedAt(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// Create allocates a fresh machine - MachineSize/BuildType are accepted
// for wire compatibility but otherwise ignored: this server has exactly
// one kind of machine (a scratch directory plus whatever toolchain is on
// this host's own PATH), not real differently-sized VMs.
//
// Holds the stream open for the machine's whole life, evicting the
// registry entry once ctx (== stream.Context()) ends - matching
// machine.proto's own "a disconnect triggers the machine to be deleted"
// contract. The directory itself is removed by localdev.Compiler's own
// ctx-tied cleanup, not here - this handler only owns the registry.
func (s *coderServer) Create(req *machinev1.CreateRequest, stream machinev1.Machine_CreateServer) error {
	ctx := stream.Context()
	compiler, err := s.coder.Compiler(ctx, nil)
	if err != nil {
		return status.Errorf(codes.Internal, "create machine: %s", err)
	}

	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.machines[id] = compiler
	s.mu.Unlock()

	if err := stream.Send(&machinev1.CreateResponse{Data: &machinev1.CreateResponse_Id{Id: id}}); err != nil {
		s.mu.Lock()
		delete(s.machines, id)
		s.mu.Unlock()
		return err
	}

	<-ctx.Done()
	s.mu.Lock()
	delete(s.machines, id)
	s.mu.Unlock()
	return nil
}

// Compile streams compiler.Compile's log/binary channels onto the wire,
// then returns its final error (if any).
func (s *coderServer) Compile(req *machinev1.CompileRequest, stream machinev1.Machine_CompileServer) error {
	compiler, err := s.getCompiler(req.MachineId)
	if err != nil {
		return err
	}
	result := compiler.Compile(stream.Context(), req.ExportPath)
	logC, binaryC := result.LogC, result.BinaryC
	for logC != nil || binaryC != nil {
		select {
		case line, ok := <-logC:
			if !ok {
				logC = nil
				continue
			}
			if err := stream.Send(&machinev1.CompileResponse{Data: &machinev1.CompileResponse_Log{Log: &machinev1.CompileLog{Data: line}}}); err != nil {
				return err
			}
		case b, ok := <-binaryC:
			if !ok {
				binaryC = nil
				continue
			}
			if err := stream.Send(&machinev1.CompileResponse{Data: &machinev1.CompileResponse_Binary{Binary: &machinev1.CompileBinary{Path: b.Path, Data: b.Data}}}); err != nil {
				return err
			}
		}
	}
	if err := <-result.ErrorC; err != nil {
		return status.Errorf(codes.Unknown, "%s", err)
	}
	return nil
}

// Lint streams compiler.Lint's log channel onto the wire. localdev's own
// Lint always closes LogC once done (success or failure alike), so this
// plain range is safe - unlike harness's real remote implementation,
// where LogC is never closed and a caller must select against ErrorC
// too (see codingagent.go's drainLogResult, fund-agent).
func (s *coderServer) Lint(req *machinev1.IdOnly, stream machinev1.Machine_LintServer) error {
	compiler, err := s.getCompiler(req.MachineId)
	if err != nil {
		return err
	}
	result := compiler.Lint(stream.Context())
	for line := range result.LogC {
		if err := stream.Send(&machinev1.LintResponse{Data: line}); err != nil {
			return err
		}
	}
	if err := <-result.ErrorC; err != nil {
		return status.Errorf(codes.Unknown, "%s", err)
	}
	return nil
}

// Format runs compiler.Format synchronously - api.BotCompiler.Format
// already returns a plain error, no log stream.
func (s *coderServer) Format(ctx context.Context, req *machinev1.FormatRequest) (*emptypb.Empty, error) {
	compiler, err := s.getCompiler(req.MachineId)
	if err != nil {
		return nil, err
	}
	if err := compiler.Format(ctx, req.Path); err != nil {
		return nil, status.Errorf(codes.Unknown, "%s", err)
	}
	return &emptypb.Empty{}, nil
}

// Test streams compiler.Test's log channel - req.Env/req.Arg are
// accepted for wire compatibility but have no effect: api.BotCompiler.
// Test takes neither.
func (s *coderServer) Test(req *machinev1.TestRequest, stream machinev1.Machine_TestServer) error {
	compiler, err := s.getCompiler(req.MachineId)
	if err != nil {
		return err
	}
	result := compiler.Test(stream.Context())
	for line := range result.LogC {
		if err := stream.Send(&machinev1.LintResponse{Data: line}); err != nil {
			return err
		}
	}
	if err := <-result.ErrorC; err != nil {
		return status.Errorf(codes.Unknown, "%s", err)
	}
	return nil
}

// Exec bridges the bidi Exec stream onto api.BotCompiler.Exec's
// stdin/stdout/stderr pipes: the first message must be a Command
// (naming the target machine), after which Stdio messages feed the
// process's stdin and its stdout/stderr are forwarded back as Stdio
// messages on channels 1/2, finishing with one ExitCode message - the
// real exit code recovered from the *exec.ExitError
// localCompileMachine.Exec's own CloseWithError leaves on the pipe's
// final Read, or -1 if the process ended some other way (killed by a
// signal, pipe broken, ...), matching ExecResponse.exit_code's own doc
// comment.
func (s *coderServer) Exec(stream machinev1.Machine_ExecServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	cmdMsg, ok := first.Data.(*machinev1.ExecRequest_Command)
	if !ok {
		return status.Error(codes.InvalidArgument, "first Exec message must be a Command")
	}
	compiler, err := s.getCompiler(cmdMsg.Command.MachineId)
	if err != nil {
		return err
	}

	stdin, stdout, stderr := compiler.Exec(stream.Context(), cmdMsg.Command.Command, cmdMsg.Command.Arg, cmdMsg.Command.Env)

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				_ = stdin.Close()
				return
			}
			stdinMsg, ok := msg.Data.(*machinev1.ExecRequest_Stdin)
			if !ok {
				continue
			}
			if _, err := stdin.Write(stdinMsg.Stdin.Data); err != nil {
				return
			}
		}
	}()

	var sendMu sync.Mutex
	send := func(resp *machinev1.ExecResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(resp)
	}

	var wg sync.WaitGroup
	var stdoutErr, stderrErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		stdoutErr = pumpExecOutput(stdout, 1, send)
	}()
	go func() {
		defer wg.Done()
		stderrErr = pumpExecOutput(stderr, 2, send)
	}()
	wg.Wait()

	finalErr := stdoutErr
	if finalErr == nil {
		finalErr = stderrErr
	}
	exitCode := int32(0)
	if finalErr != nil {
		var exitErr *exec.ExitError
		if errors.As(finalErr, &exitErr) {
			exitCode = int32(exitErr.ExitCode())
		} else {
			exitCode = -1
		}
	}
	return send(&machinev1.ExecResponse{Data: &machinev1.ExecResponse_ExitCode{ExitCode: exitCode}})
}

// pumpExecOutput copies r (stdout or stderr, tagged by channel: 1 or 2)
// into send as ExecResponse_Stdio chunks until EOF, returning any
// non-EOF error (e.g. the *exec.ExitError a finished local subprocess's
// closed pipe carries).
func pumpExecOutput(r io.ReadCloser, channel int32, send func(*machinev1.ExecResponse) error) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			if sendErr := send(&machinev1.ExecResponse{Data: &machinev1.ExecResponse_Stdio{Stdio: &machinev1.Stdio{Channel: channel, Data: data}}}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// GetInformation just confirms req.MachineId names a live machine.
func (s *coderServer) GetInformation(ctx context.Context, req *builderv1.GetInformationRequest) (*emptypb.Empty, error) {
	if _, err := s.getCompiler(req.MachineId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// LsInfo lists req.Directory's immediate children.
func (s *coderServer) LsInfo(ctx context.Context, req *builderv1.LsRequest) (*builderv1.LsResponse, error) {
	fsys, err := s.filesystemFor(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	infos, err := fsys.LsInfo(ctx, &adkfs.LsInfoRequest{Path: req.Directory})
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "%s", err)
	}
	resp := &builderv1.LsResponse{Entry: make([]*builderv1.LsEntry, len(infos))}
	for i, info := range infos {
		resp.Entry[i] = &builderv1.LsEntry{
			Path:       info.Path,
			IsDir:      info.IsDir,
			Size:       info.Size,
			ModifiedAt: parseModifiedAt(info.ModifiedAt),
		}
	}
	return resp, nil
}

// Read serves every ReadRequest_Request sent after the mandatory first
// ReadRequest_MachineId message (harness/fs.go's fsysWrapper.FileSystem
// sends this first, matching WriteRequest/EditRequest's own "machine_id
// only read from the first message" convention - Read's own ReadDetail
// carries no machine_id anywhere else). One file's read failing doesn't
// abort the stream - this is a long-lived, many-files-over-its-lifetime
// stream, same as Write/Edit below - it's reported as a zero-byte result
// instead, so a bad path can't take down every other pending read.
func (s *coderServer) Read(stream builderv1.FileSystemWrapper_ReadServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	idMsg, ok := first.Data.(*builderv1.ReadRequest_MachineId)
	if !ok {
		return status.Error(codes.InvalidArgument, "first Read message must carry machine_id")
	}
	fsys, err := s.filesystemFor(stream.Context(), idMsg.MachineId)
	if err != nil {
		return err
	}

	const chunkSize = 64 * 1024
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		reqMsg, ok := msg.Data.(*builderv1.ReadRequest_Request)
		if !ok {
			continue
		}
		detail := reqMsg.Request
		fileID := detail.Header.FileId

		content, err := fsys.Read(stream.Context(), &adkfs.ReadRequest{
			FilePath: detail.Header.Path, Offset: int(detail.Offset), Limit: int(detail.Limit),
		})
		var data []byte
		if err == nil {
			data = []byte(content.Content)
		}
		if err := stream.Send(&builderv1.ReadResponse{Data: &builderv1.ReadResponse_Header{Header: &builderv1.FileHeader{
			FileId: fileID, Path: detail.Header.Path, Size: int64(len(data)),
		}}}); err != nil {
			return err
		}
		for off := 0; off < len(data); off += chunkSize {
			end := off + chunkSize
			if end > len(data) {
				end = len(data)
			}
			if err := stream.Send(&builderv1.ReadResponse{Data: &builderv1.ReadResponse_Blob{Blob: &builderv1.FileBlob{FileId: fileID, Blob: data[off:end]}}}); err != nil {
				return err
			}
		}
		// Empty-blob terminator - fsysWrapper's own read dispatch
		// goroutine (harness/fs.go) treats len(blob)==0 as "this file is
		// done", sent even for a zero-byte file or a failed read alike.
		if err := stream.Send(&builderv1.ReadResponse{Data: &builderv1.ReadResponse_Blob{Blob: &builderv1.FileBlob{FileId: fileID, Blob: []byte{}}}}); err != nil {
			return err
		}
	}
}

// Write serves a long-lived stream of possibly many files, each framed
// as one FileHeader followed by FileBlob chunks and an empty-blob
// terminator (fsysWrapper.BatchWrite's own framing) - machine_id is only
// read from the stream's first message, per the proto's own comment.
func (s *coderServer) Write(stream builderv1.FileSystemWrapper_WriteServer) error {
	var fsys api.FilesystemWrapper
	var curPath string
	var buf bytes.Buffer
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if fsys == nil {
			fsys, err = s.filesystemFor(stream.Context(), msg.MachineId)
			if err != nil {
				return err
			}
		}
		switch x := msg.Data.(type) {
		case *builderv1.WriteRequest_Header:
			curPath = x.Header.Path
			buf.Reset()
		case *builderv1.WriteRequest_Blob:
			if len(x.Blob.Blob) == 0 {
				// The empty blob is this file's own terminator - write it
				// now and ack it (correlated by FileId), one ack per file,
				// not one for the whole (long-lived, many-files) stream -
				// see WriteAck's own doc comment in build.proto.
				writeErr := fsys.Write(stream.Context(), &adkfs.WriteRequest{FilePath: curPath, Content: buf.String()})
				ack := &builderv1.WriteAck{FileId: x.Blob.FileId}
				if writeErr != nil {
					ack.Error = writeErr.Error()
				}
				if err := stream.Send(ack); err != nil {
					return err
				}
				continue
			}
			buf.Write(x.Blob.Blob)
		}
	}
}

// GrepRaw searches req.Path for req.Pattern.
func (s *coderServer) GrepRaw(req *builderv1.GrepRequest, stream builderv1.FileSystemWrapper_GrepRawServer) error {
	fsys, err := s.filesystemFor(stream.Context(), req.MachineId)
	if err != nil {
		return err
	}
	matches, err := fsys.GrepRaw(stream.Context(), &adkfs.GrepRequest{
		Path: req.Path, Pattern: req.Pattern, Glob: req.Glob, FileType: req.FileType,
		BeforeLines: int(req.BeforeLines), AfterLines: int(req.AfterLines),
		EnableMultiline: req.EnableMultiline, CaseInsensitive: req.CaseInsensitive,
	})
	if err != nil {
		return status.Errorf(codes.Unknown, "%s", err)
	}
	for _, m := range matches {
		if err := stream.Send(&builderv1.GrepResponse{Content: m.Content, Path: m.Path, Line: int32(m.Line)}); err != nil {
			return err
		}
	}
	return nil
}

// GlobRaw matches req.Pattern against files under req.Path.
func (s *coderServer) GlobRaw(req *builderv1.GlobRequest, stream builderv1.FileSystemWrapper_GlobRawServer) error {
	fsys, err := s.filesystemFor(stream.Context(), req.MachineId)
	if err != nil {
		return err
	}
	infos, err := fsys.GlobInfo(stream.Context(), &adkfs.GlobInfoRequest{Path: req.Path, Pattern: req.Pattern})
	if err != nil {
		return status.Errorf(codes.Unknown, "%s", err)
	}
	for _, info := range infos {
		if err := stream.Send(&builderv1.GlobResponse{Path: info.Path, IsDir: info.IsDir, Size: info.Size, ModifiedAt: parseModifiedAt(info.ModifiedAt)}); err != nil {
			return err
		}
	}
	return nil
}

// Edit serves a long-lived stream of possibly many independent edits,
// each one self-contained in a single EditRequest - machine_id is only
// read from the stream's first message.
func (s *coderServer) Edit(stream builderv1.FileSystemWrapper_EditServer) error {
	var fsys api.FilesystemWrapper
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if fsys == nil {
			fsys, err = s.filesystemFor(stream.Context(), msg.MachineId)
			if err != nil {
				return err
			}
		}
		editErr := fsys.Edit(stream.Context(), &adkfs.EditRequest{
			FilePath: msg.Path, OldString: msg.OldContent, NewString: msg.NewContent, ReplaceAll: msg.ReplaceAll,
		})
		ack := &builderv1.EditAck{RequestId: msg.RequestId}
		if editErr != nil {
			ack.Error = editErr.Error()
		}
		if err := stream.Send(ack); err != nil {
			return err
		}
	}
}

// Untar receives one gzip+tar archive (header naming the machine and
// extraction destination, then blob chunks until the client closes its
// send side) and writes every regular entry via fsys.Write, joined onto
// destination - this is how harness.Compiler's own upload step
// (UntarFS) populates a freshly created, otherwise-empty machine.
func (s *coderServer) Untar(stream builderv1.FileSystemWrapper_UntarServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hdrMsg, ok := first.Data.(*builderv1.UntarRequest_Header)
	if !ok {
		return status.Error(codes.InvalidArgument, "first Untar message must be a header")
	}
	fsys, err := s.filesystemFor(stream.Context(), hdrMsg.Header.MachineId)
	if err != nil {
		return err
	}
	destination := hdrMsg.Header.Path

	pr, pw := io.Pipe()
	recvErrC := make(chan error, 1)
	go func() {
		defer func() { _ = pw.Close() }()
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				recvErrC <- nil
				return
			}
			if err != nil {
				recvErrC <- err
				return
			}
			blobMsg, ok := msg.Data.(*builderv1.UntarRequest_Blob)
			if !ok {
				continue
			}
			if len(blobMsg.Blob) == 0 {
				continue
			}
			if _, err := pw.Write(blobMsg.Blob); err != nil {
				recvErrC <- err
				return
			}
		}
	}()

	gz, err := gzip.NewReader(pr)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "gzip: %s", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "tar: %s", err)
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return status.Errorf(codes.Internal, "read tar entry %s: %s", hdr.Name, err)
		}
		target := path.Join(destination, hdr.Name)
		if err := fsys.Write(stream.Context(), &adkfs.WriteRequest{FilePath: target, Content: string(data)}); err != nil {
			return status.Errorf(codes.Internal, "write %s: %s", target, err)
		}
	}
	if err := <-recvErrC; err != nil {
		return err
	}
	return stream.SendAndClose(&emptypb.Empty{})
}
