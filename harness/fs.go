package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync/atomic"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	builderv1 "git.noncepad.com/pkg/optimizer/gen/builder/v1"
	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	"github.com/cloudwego/eino/adk/filesystem"
)

type fsysWrapper struct {
	cm            *compileMachine
	c             builderv1.FileSystemWrapperClient
	writeReqC     chan<- *writeSub
	nextFileID    atomic.Int32
	editReqC      chan<- *editSub
	nextRequestID atomic.Uint32
	readC         chan<- *readSub
	readErrorC    <-chan error
	readError     error
}

// ContainerWorkspace is the fixed path every coding container's source
// lives at inside the container - Compiler's own UntarFS upload (below)
// extracts startingFiles here, and every caller's own FilesystemWrapper
// path (LsInfo/Read/Write/Edit) is expected to be rooted at this same
// directory, the same way api.BotCompiler's own doc comment describes
// startingFiles landing in "the base directory of the coding directory
// in the container".
const ContainerWorkspace = "."

func (cm *compileMachine) FileSystem(ctx context.Context) (api.FilesystemWrapper, error) {
	return cm.fileSystem(ctx)
}

// fileSystem is FileSystem's own concrete-typed implementation - kept
// separate so Compiler (compiler.go) can call UntarFS directly on the
// result, which api.FilesystemWrapper itself doesn't expose (nothing
// outside this package needs to trigger an upload directly; Compiler
// does it once, automatically, on every machine it creates).
func (cm *compileMachine) fileSystem(ctx context.Context) (*fsysWrapper, error) {
	fw := new(fsysWrapper)
	fw.cm = cm
	fw.c = builderv1.NewFileSystemWrapperClient(cm.mm.conn)

	// writeStream/editStream are reused across every BatchWrite/Edit call
	// for this fsysWrapper's whole lifetime -- opened directly against
	// ctx, gRPC would abort the stream the instant ctx is cancelled,
	// racing against runWriteStream/runEditStream's own graceful
	// CloseSend and discarding whatever was already sent but not yet
	// acked.
	streamCtx := context.WithoutCancel(ctx)
	writeStream, err := fw.c.Write(streamCtx)
	if err != nil {
		return nil, err
	}
	writeReqC := make(chan *writeSub, 100)
	fw.writeReqC = writeReqC
	go runWriteStream(ctx, writeStream, writeReqC)

	editStream, err := fw.c.Edit(streamCtx)
	if err != nil {
		return nil, err
	}
	editReqC := make(chan *editSub, 100)
	fw.editReqC = editReqC
	go runEditStream(ctx, editStream, editReqC)

	readStream, err := fw.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	// machine_id is only ever read from the first message on the stream
	// (same convention WriteRequest/EditRequest document on their own
	// machine_id field) - unlike those, Read's own ReadDetail carries no
	// machine_id of its own anywhere, so without sending this first, the
	// server would have no way to know which machine every later
	// ReadRequest_Request on this stream targets.
	if err := readStream.Send(&builderv1.ReadRequest{Data: &builderv1.ReadRequest_MachineId{MachineId: cm.id}}); err != nil {
		return nil, err
	}
	readErrorC := make(chan error, 1)
	readC := make(chan *readSub, 100)
	fw.readC = readC
	fw.readErrorC = readErrorC

	headerC := make(chan *builderv1.FileHeader, 10)
	blobC := make(chan *builderv1.FileBlob, 10)
	readByteErrorC := make(chan error, 1)

	// read from the stream
	go func() {
		doneC := ctx.Done()
		var err2 error
	out:
		for {
			var msg *builderv1.ReadResponse
			msg, err2 = readStream.Recv()
			if err2 != nil {
				break out
			}
			switch x := msg.Data.(type) {
			case *builderv1.ReadResponse_Header:
				select {
				case <-doneC:
					err2 = ctx.Err()
					break out
				case headerC <- x.Header:
				}
			case *builderv1.ReadResponse_Blob:
				select {
				case <-doneC:
					err2 = ctx.Err()
					break out
				case blobC <- x.Blob:
				}
			}
		}
		readByteErrorC <- err2
	}()

	// read in read subscriptions and responses to read requests
	go func() {
		doneC := ctx.Done()
		var err2 error
		var present bool
		fileID := int32(0)
		mSub := make(map[int32]*readSub)
	out:
		for {
			select {
			case <-doneC:
				break out
			case sub := <-readC:
				req := sub.request
				fileID++
				req.Header.FileId = fileID
				mSub[fileID] = sub
				err2 = readStream.Send(&builderv1.ReadRequest{
					Data: &builderv1.ReadRequest_Request{
						Request: req,
					},
				})
				if err2 != nil {
					break out
				}
			case err2 = <-readByteErrorC:
				break out
			case header := <-headerC:
				var sub *readSub
				sub, present = mSub[header.FileId]
				if !present {
					err2 = fmt.Errorf("got bad file id %d", header.FileId)
					break out
				}
				sub.header = header
			case blob := <-blobC:
				var sub *readSub
				sub, present = mSub[blob.FileId]
				if !present {
					err2 = fmt.Errorf("got bad file id %d", blob.FileId)
					break out
				}
				if len(blob.Blob) == 0 {
					_ = sub.writer.Close()
					delete(mSub, blob.FileId)
				} else {
					_, err2 = io.Copy(sub.writer, bytes.NewBuffer(blob.Blob))
					if err2 != nil {
						break out
					}
				}
			}
		}
		readErrorC <- err2
	}()

	return fw, nil
}

// writeSub is one message BatchWrite wants sent on the shared Write
// stream - fileID/doneC are only set on a file's own terminator (empty
// blob) message, which is what runWriteStream correlates the matching
// WriteAck to and delivers through doneC. Without this, a BatchWrite
// call would have no way to know its file was actually applied before
// returning - a plain CloseSend/EOF gives no such guarantee on a stream
// this long-lived and shared across many unrelated calls.
type writeSub struct {
	req    *builderv1.WriteRequest
	fileID int32
	doneC  chan error
}

// runWriteStream owns stream exclusively - the only goroutine that ever
// calls Send or Recv on it, so any number of concurrent BatchWrite
// callers only ever talk to it through reqC, one message at a time (a
// gRPC stream's Send/Recv are not safe to call concurrently from
// multiple goroutines). A second, pure-receive goroutine forwards every
// WriteAck into ackC so this one can select between {new outgoing
// message, incoming ack, ctx done} instead of blocking on whichever of
// Send/Recv a plain sequential loop would have to pick first.
func runWriteStream(ctx context.Context, stream builderv1.FileSystemWrapper_WriteClient, reqC <-chan *writeSub) {
	ackC := make(chan *builderv1.WriteAck, 100)
	recvErrC := make(chan error, 1)
	go func() {
		for {
			ack, err := stream.Recv()
			if err != nil {
				recvErrC <- err
				return
			}
			select {
			case ackC <- ack:
			case <-ctx.Done():
				return
			}
		}
	}()

	pending := make(map[int32]chan error)
	doneC := ctx.Done()
	for {
		select {
		case <-doneC:
			_ = stream.CloseSend()
			failPending(pending, ctx.Err())
			return
		case sub := <-reqC:
			if sub.doneC != nil {
				pending[sub.fileID] = sub.doneC
			}
			if err := stream.Send(sub.req); err != nil {
				if sub.doneC != nil {
					delete(pending, sub.fileID)
					sub.doneC <- err
				}
				failPending(pending, err)
				return
			}
		case ack := <-ackC:
			if done, ok := pending[ack.FileId]; ok {
				delete(pending, ack.FileId)
				if ack.Error != "" {
					done <- errors.New(ack.Error)
				} else {
					done <- nil
				}
			}
		case err := <-recvErrC:
			failPending(pending, err)
			return
		}
	}
}

// editSub is one Edit call's own request/ack pair - unlike writeSub,
// every message needs its own ack (there's no header/blob framing to
// wait out first), correlated by RequestId.
type editSub struct {
	req   *builderv1.EditRequest
	doneC chan error
}

// runEditStream is runWriteStream's own twin for the Edit stream - see
// its doc comment for the design (one owning goroutine, one pure-receive
// goroutine, correlated by RequestId instead of FileId).
func runEditStream(ctx context.Context, stream builderv1.FileSystemWrapper_EditClient, reqC <-chan *editSub) {
	ackC := make(chan *builderv1.EditAck, 100)
	recvErrC := make(chan error, 1)
	go func() {
		for {
			ack, err := stream.Recv()
			if err != nil {
				recvErrC <- err
				return
			}
			select {
			case ackC <- ack:
			case <-ctx.Done():
				return
			}
		}
	}()

	pending := make(map[uint32]chan error)
	doneC := ctx.Done()
	for {
		select {
		case <-doneC:
			_ = stream.CloseSend()
			failPending(pending, ctx.Err())
			return
		case sub := <-reqC:
			pending[sub.req.RequestId] = sub.doneC
			if err := stream.Send(sub.req); err != nil {
				delete(pending, sub.req.RequestId)
				sub.doneC <- err
				failPending(pending, err)
				return
			}
		case ack := <-ackC:
			if done, ok := pending[ack.RequestId]; ok {
				delete(pending, ack.RequestId)
				if ack.Error != "" {
					done <- errors.New(ack.Error)
				} else {
					done <- nil
				}
			}
		case err := <-recvErrC:
			failPending(pending, err)
			return
		}
	}
}

// failPending delivers err to every still-outstanding caller in pending
// and empties the map - used when the stream itself ends (ctx done, a
// Send error, or Recv returning a terminal error), since none of those
// callers will ever otherwise get an ack.
func failPending[K comparable](pending map[K]chan error, err error) {
	for id, done := range pending {
		done <- err
		delete(pending, id)
	}
}

func (fw *fsysWrapper) UntarFS(ctx context.Context, destination string, source fs.FS) error {
	stream, err := fw.c.Untar(ctx)
	if err != nil {
		return err
	}
	err = stream.Send(&builderv1.UntarRequest{
		Data: &builderv1.UntarRequest_Header{
			Header: &builderv1.UntarHeader{
				MachineId: fw.cm.id,
				Path:      destination,
			},
		},
	})
	if err != nil {
		return err
	}
	r1, w := io.Pipe()
	errorC := make(chan error, 1)
	go func() {
		// w (the pipe writer) must close last, after both tw/gw have
		// flushed their own trailers into it -- defers run LIFO, so
		// deferring it first here, before gw/tw, is what makes that so.
		// Without this, nothing ever closes w: gw.Close()/tw.Close() only
		// flush data through to it, they don't close the underlying
		// writer they wrap (same as any other io.Writer wrapper) -- the
		// main goroutine's r1.Read loop below would block forever
		// waiting for an EOF that would never come.
		defer func() { _ = w.Close() }()

		// 1. Create gzip writer around the underlying io.Writer
		gw := gzip.NewWriter(w)
		defer func() {
			_ = gw.Close()
		}()

		// 2. Create tar writer around the gzip writer
		tw := tar.NewWriter(gw)
		defer func() {
			_ = tw.Close()
		}()
		errorC <- fs.WalkDir(source, ".", func(path string, d fs.DirEntry, err2 error) error {
			if err2 != nil {
				return err2
			}

			// Get FileInfo for header metadata (size, modtime, permissions)
			info, err2 := d.Info()
			if err2 != nil {
				return err2
			}

			// Create a tar header from the FileInfo struct
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err2
			}

			// Ensure path uses forward slashes in tar archive
			header.Name = path

			// Write header to tar stream
			if err2 := tw.WriteHeader(header); err != nil {
				return err2
			}

			// Directories and non-regular files don't have content to write
			if !info.Mode().IsRegular() {
				return nil
			}

			// Open the file inside fs.FS and copy its content into the tar stream
			file, err2 := source.Open(path)
			if err2 != nil {
				return err2
			}
			defer func() {
				_ = file.Close()
			}()

			_, err2 = io.Copy(tw, file)
			return err2
		})
	}()
out:
	for {
		buf := make([]byte, 100*1024)
		var n int
		n, err = r1.Read(buf)
		if err == io.EOF {
			err = nil
			break out
		} else if err != nil {
			break out
		}
		x := make([]byte, n)
		copy(x, buf[0:n])
		err = stream.Send(&builderv1.UntarRequest{
			Data: &builderv1.UntarRequest_Blob{
				Blob: x,
			},
		})
		if err != nil {
			break out
		}
	}
	_ = r1.Close()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-errorC:
	}
	if err != nil {
		return err
	}
	// CloseAndRecv, not just CloseSend -- Untar is a client-streaming RPC
	// (the server only applies/acks once the stream closes), and unlike
	// editStream/writeStream above (long-lived, intentionally left open
	// across many calls until ctx ends), this stream is created fresh for
	// this one call and never reused -- there's no reason not to wait for
	// and surface the real result. CloseSend alone would let this return
	// successfully before the upload is even guaranteed to have reached
	// the server, let alone succeeded.
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("untar %s: %w", destination, err)
	}
	return nil
}

type readSub struct {
	request *builderv1.ReadDetail
	header  *builderv1.FileHeader // this is from the server
	writer  io.WriteCloser
}

func (fw *fsysWrapper) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	resp, err := fw.c.LsInfo(ctx, &builderv1.LsRequest{
		MachineId: fw.cm.id,
		Directory: req.Path,
	})
	if err != nil {
		return nil, err
	}
	listFi := make([]filesystem.FileInfo, len(resp.Entry))
	for i, x := range resp.Entry {
		listFi[i] = filesystem.FileInfo{
			Path:       x.Path,
			IsDir:      x.IsDir,
			Size:       x.Size,
			ModifiedAt: time.Unix(x.ModifiedAt, 0).String(),
		}
	}
	return listFi, nil
}

func (fw *fsysWrapper) BatchRead(ctx context.Context, listPath []*api.ReadHeader) []*io.PipeReader {
	listReader := make([]*io.PipeReader, len(listPath))
	if fw.readError != nil {
		for i := range len(listReader) {
			var w *io.PipeWriter
			listReader[i], w = io.Pipe()
			_ = w.CloseWithError(fw.readError)
		}
		return listReader
	}
	doneC := ctx.Done()
	var err error
out:
	for i, remoteFilePath := range listPath {

		rs := new(readSub)
		rs.request = new(builderv1.ReadDetail)
		rs.request.Header = new(builderv1.FileHeader)
		rs.request.Header.Path = remoteFilePath.Path
		// this is not relavent to the serve side
		rs.request.Header.Size = 0
		// this is set in the read goroutine
		rs.request.Header.FileId = 0
		rs.request.Offset = int64(remoteFilePath.Offset)
		rs.request.Limit = int64(remoteFilePath.Limit)
		listReader[i], rs.writer = io.Pipe()
		select {
		case <-doneC:
			err = ctx.Err()
			break out
		case err = <-fw.readErrorC:
			fw.readError = err
			break out
		case fw.readC <- rs:
		}
	}
	if err != nil {
		for i := range len(listReader) {
			var w *io.PipeWriter
			listReader[i], w = io.Pipe()
			_ = w.CloseWithError(err)
		}
	}
	return listReader
}

// Read file content, supports line offset and limit
func (fw *fsysWrapper) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	listReader := fw.BatchRead(ctx, []*api.ReadHeader{
		{
			Path:   req.FilePath,
			Limit:  req.Limit,
			Offset: req.Offset,
		},
	})
	if len(listReader) != 1 {
		return nil, errors.New("bad number of listreaders")
	}
	data, err := io.ReadAll(listReader[0])
	if err != nil {
		return nil, err
	}
	return &filesystem.FileContent{Content: string(data)}, nil
}

// Search for matching content in files
func (fw *fsysWrapper) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	stream, err := fw.c.GrepRaw(ctx, &builderv1.GrepRequest{
		MachineId:       fw.cm.id,
		Path:            req.Path,
		Pattern:         req.Pattern,
		Glob:            req.Glob,
		FileType:        req.FileType,
		BeforeLines:     int32(req.BeforeLines),
		AfterLines:      int32(req.AfterLines),
		EnableMultiline: req.EnableMultiline,
		CaseInsensitive: req.CaseInsensitive,
	})
	if err != nil {
		return nil, err
	}
	list := dslist.CreateGeneric[filesystem.GrepMatch]()
out:
	for {
		var msg *builderv1.GrepResponse
		msg, err = stream.Recv()
		if err == io.EOF {
			err = nil
			break out
		} else if err != nil {
			break out
		}
		if msg == nil {
			err = errors.New("blank message")
			break out
		}
		list.Append(filesystem.GrepMatch{
			Content: msg.Content,
			Path:    msg.Header.Path,
			Line:    int(msg.Line),
		})
	}
	if err != nil {
		return nil, err
	}
	return list.Array(), nil
}

// Match files by glob pattern
func (fw *fsysWrapper) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	stream, err := fw.c.GlobRaw(ctx, &builderv1.GlobRequest{
		MachineId: fw.cm.id,
		Path:      req.Path,
		Pattern:   req.Pattern,
	})
	if err != nil {
		return nil, err
	}
	list := dslist.CreateGeneric[filesystem.FileInfo]()
out:
	for {
		var msg *builderv1.GlobResponse
		msg, err = stream.Recv()
		if err == io.EOF {
			err = nil
			break out
		} else if err != nil {
			break out
		}
		if msg == nil {
			err = errors.New("blank message")
			break out
		}
		list.Append(filesystem.FileInfo{
			Path:       msg.Path,
			IsDir:      msg.IsDir,
			Size:       msg.Size,
			ModifiedAt: time.Unix(msg.ModifiedAt, 0).String(),
		})
	}
	if err != nil {
		return nil, err
	}
	return list.Array(), nil
}

// sendWrite enqueues req for runWriteStream to send, blocking only until
// it's accepted onto the queue (not until it's actually sent/acked) -
// used for the header/blob messages that don't need their own ack (see
// BatchWrite's own terminator handling for the one message that does).
func (fw *fsysWrapper) sendWrite(ctx context.Context, req *builderv1.WriteRequest) error {
	select {
	case fw.writeReqC <- &writeSub{req: req}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (fw *fsysWrapper) BatchWrite(ctx context.Context, listFile []*api.WriteHeader) error {
	buf := make([]byte, 500*1024)
	for _, f := range listFile {
		fileID := fw.nextFileID.Add(1)
		if err := fw.sendWrite(ctx, &builderv1.WriteRequest{
			MachineId: fw.cm.id,
			Data: &builderv1.WriteRequest_Header{
				Header: &builderv1.FileHeader{
					FileId: fileID,
					Path:   f.Path,
					Size:   int64(f.Size),
				},
			},
		}); err != nil {
			return err
		}
		p := 0
	out3:
		for p < f.Size {
			n, err := f.Reader.Read(buf)
			if err == io.EOF {
				break out3
			} else if err != nil {
				break out3
			}
			data := make([]byte, n)
			copy(data, buf[0:n])
			p += n
			if err := fw.sendWrite(ctx, &builderv1.WriteRequest{
				MachineId: fw.cm.id,
				Data: &builderv1.WriteRequest_Blob{
					Blob: &builderv1.FileBlob{
						FileId: fileID,
						Blob:   data,
					},
				},
			}); err != nil {
				return err
			}
		}
		if p < f.Size {
			return fmt.Errorf("premature EOF: %d vs %d", p, f.Size)
		}

		// The terminator blob is the one message that carries a doneC -
		// its matching WriteAck (correlated by fileID) is what tells this
		// call whether the file was actually applied, not just enqueued.
		doneC := make(chan error, 1)
		select {
		case fw.writeReqC <- &writeSub{
			req: &builderv1.WriteRequest{
				MachineId: fw.cm.id,
				Data: &builderv1.WriteRequest_Blob{
					Blob: &builderv1.FileBlob{FileId: fileID, Blob: []byte{}},
				},
			},
			fileID: fileID,
			doneC:  doneC,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case err := <-doneC:
			if err != nil {
				return fmt.Errorf("write %s: %w", f.Path, err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// Write file content
func (fw *fsysWrapper) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	content := []byte(req.Content)
	return fw.BatchWrite(ctx, []*api.WriteHeader{
		{
			Path:   req.FilePath,
			Size:   len(content),
			Reader: io.NopCloser(bytes.NewReader(content)),
		},
	})
}

// Edit file content (string replacement) - blocks until the matching
// EditAck (correlated by RequestId) comes back, so a caller actually
// knows whether its edit took effect before proceeding (e.g. to compile
// or read the file it just edited), not just that it was enqueued.
func (fw *fsysWrapper) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	requestID := fw.nextRequestID.Add(1)
	doneC := make(chan error, 1)
	select {
	case fw.editReqC <- &editSub{
		req: &builderv1.EditRequest{
			MachineId:  fw.cm.id,
			Path:       req.FilePath,
			OldContent: req.OldString,
			NewContent: req.NewString,
			ReplaceAll: req.ReplaceAll,
			RequestId:  requestID,
		},
		doneC: doneC,
	}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-doneC:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
