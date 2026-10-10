package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	machinev1 "git.noncepad.com/pkg/optimizer/gen/machine/v1"
	"google.golang.org/grpc"
)

type Configuration struct {
	MachineSize machinev1.MachineSize `json:"size"`
	BuildType   machinev1.BuildType   `json:"type"`
	Timeout     time.Duration         `json:"timeout"`
}

type machineManager struct {
	conn    *grpc.ClientConn
	manager machinev1.MachineClient
	config  *Configuration
}

func CreateManager(conn *grpc.ClientConn, config *Configuration) (api.BotCoder, error) {
	mm := new(machineManager)
	mm.conn = conn
	mm.manager = machinev1.NewMachineClient(mm.conn)
	mm.config = config
	return mm, nil
}

type compileMachine struct {
	id     uint32 // the machine ID
	stream grpc.ServerStreamingClient[machinev1.CreateResponse]
	mm     *machineManager
}

func (mm *machineManager) Compiler(ctx context.Context, startingFiles fs.FS) (api.BotCompiler, error) {
	cm := new(compileMachine)
	cm.mm = mm
	var err error
	cm.stream, err = mm.manager.Create(ctx, &machinev1.CreateRequest{
		Size:  mm.config.MachineSize,
		Build: mm.config.BuildType,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create machine: %s", err)
	}
	errorC := make(chan error, 1)
	go func() {
		msg, err2 := cm.stream.Recv()
		if err2 != nil {
			errorC <- err2
			return
		}
		if msg == nil {
			errorC <- errors.New("nil machine id")
			return
		}
		if msg.Data == nil {
			errorC <- errors.New("nil machine id")
			return
		}
		switch x := msg.Data.(type) {
		case *machinev1.CreateResponse_Id:
			cm.id = x.Id
		case *machinev1.CreateResponse_Usage:
			err2 = errors.New("must receive machine ID first")
		}
		errorC <- err2
	}()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(mm.config.Timeout):
		err = ErrMachineAllocationTimeout
	case err = <-errorC:
	}
	if err != nil {
		return nil, err
	}

	if startingFiles != nil {
		fw, err := cm.fileSystem(ctx)
		if err != nil {
			return nil, fmt.Errorf("open filesystem to upload starting files: %w", err)
		}
		if err := fw.UntarFS(ctx, ContainerWorkspace, startingFiles); err != nil {
			return nil, fmt.Errorf("upload starting files to %s: %w", ContainerWorkspace, err)
		}
	}

	return cm, nil
}

func (cm *compileMachine) Compile(ctx context.Context, listBinaryPath []string) api.LogResult {
	doneC := ctx.Done()
	logC := make(chan string, 100)
	binaryC := make(chan api.CompileBinary, 10)
	list := make([]string, len(listBinaryPath))
	copy(list, listBinaryPath)
	errorC := make(chan error, 1)
	stream, err := cm.mm.manager.Compile(ctx, &machinev1.CompileRequest{
		MachineId:  cm.id,
		ExportPath: list,
		Env:        make(map[string]string),
	})
	if err != nil {
		errorC <- err
		return api.LogResult{ErrorC: errorC}
	}
	go func() {
		var err error
	out:
		for {
			var msg *machinev1.CompileResponse
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
			if msg.Data == nil {
				err = errors.New("blank message")
				break out
			}
			switch x := msg.Data.(type) {
			case *machinev1.CompileResponse_Binary:
				y := x.Binary
				if y == nil {
					err = errors.New("blank message")
					break out
				}
				select {
				case <-doneC:
					err = ctx.Err()
					break out
				case binaryC <- api.CompileBinary{Path: y.Path, Data: y.Data}:
				}
			case *machinev1.CompileResponse_Log:
				y := x.Log
				if y == nil {
					err = errors.New("blank message")
					break out
				}
				select {
				case <-doneC:
					err = ctx.Err()
					break out
				case logC <- y.Data:
				}
			}
		}
		close(binaryC)
		errorC <- err
	}()
	return api.LogResult{
		LogC:    logC,
		ErrorC:  errorC,
		BinaryC: binaryC,
	}
}

func (cm *compileMachine) Lint(ctx context.Context) api.LogResult {
	doneC := ctx.Done()
	logC := make(chan string, 100)
	errorC := make(chan error, 1)
	stream, err := cm.mm.manager.Lint(ctx, &machinev1.IdOnly{
		MachineId: cm.id,
	})
	if err != nil {
		errorC <- err
		return api.LogResult{ErrorC: errorC}
	}
	go func() {
		var err error
	out:
		for {
			var msg *machinev1.LintResponse
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
			select {
			case <-doneC:
				err = ctx.Err()
				break out
			case logC <- msg.Data:
			}
		}
		errorC <- err
	}()
	return api.LogResult{
		LogC:   logC,
		ErrorC: errorC,
	}
}

func (cm *compileMachine) Format(ctx context.Context, listPath []string) error {
	list := make([]string, len(listPath))
	copy(list, listPath)
	_, err := cm.mm.manager.Format(ctx, &machinev1.FormatRequest{
		MachineId: cm.id,
		Path:      list,
	})
	return err
}

func (cm *compileMachine) Test(ctx context.Context) api.LogResult {
	doneC := ctx.Done()
	logC := make(chan string, 100)
	errorC := make(chan error, 1)
	stream, err := cm.mm.manager.Test(ctx, &machinev1.TestRequest{
		MachineId: cm.id,
		Arg:       make([]string, 0),
		Env:       make(map[string]string),
	})
	if err != nil {
		errorC <- err
		return api.LogResult{ErrorC: errorC}
	}
	go func() {
		var err error
	out:
		for {
			var msg *machinev1.LintResponse
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
			select {
			case <-doneC:
				err = ctx.Err()
				break out
			case logC <- msg.Data:
			}
		}
		errorC <- err
	}()
	return api.LogResult{
		LogC:   logC,
		ErrorC: errorC,
	}
}

func (cm *compileMachine) Exec(ctx context.Context, command string, args []string, mEnv map[string]string) (io.WriteCloser, io.ReadCloser, io.ReadCloser) {
	stdinRead, stdinWrite := io.Pipe()
	stdoutRead, stdoutWrite := io.Pipe()
	stderrRead, stderrWrite := io.Pipe()
	stream, err := cm.mm.manager.Exec(ctx)
	if err != nil {
		stdoutWrite.CloseWithError(err)
		stderrWrite.CloseWithError(err)
		return stdinWrite, stdoutRead, stderrRead
	}
	listArg := make([]string, len(args))
	copyMEnv := make(map[string]string, len(mEnv))
	for k, v := range mEnv {
		copyMEnv[k] = v
	}
	copy(listArg, args)
	err = stream.Send(&machinev1.ExecRequest{
		Data: &machinev1.ExecRequest_Command{
			Command: &machinev1.Command{
				MachineId: cm.id,
				Command:   command,
				Arg:       listArg,
				Env:       copyMEnv,
			},
		},
	})
	if err != nil {
		stdoutWrite.CloseWithError(err)
		stderrWrite.CloseWithError(err)
		return stdinWrite, stdoutRead, stderrRead
	}

	badStdinC := make(chan error, 1)

	// handle stdin
	go func() {
		buf := make([]byte, 10*1024)
		var n int
		var err2 error
	out:
		for {
			n, err2 = stdinRead.Read(buf[:])
			if err2 == io.EOF {
				err2 = nil
				break out
			} else if err2 != nil {
				break out
			}
			data := make([]byte, n)
			copy(data, buf[0:n])
			err2 = stream.Send(&machinev1.ExecRequest{
				Data: &machinev1.ExecRequest_Stdin{
					Stdin: &machinev1.Stdio{
						Channel: 0,
						Data:    data,
					},
				},
			})
			if err2 != nil {
				break out
			}
		}
		_ = stdinRead.CloseWithError(err2)
		badStdinC <- err2
	}()

	// handle stdout stderr
	go func() {
		var err2 error
		gotStdin := false
	out:
		for {
			if !gotStdin {
				select {
				case err2 = <-badStdinC:
					gotStdin = true
					if err2 != nil {
						break out
					}
				default:
				}
			}
			var msg *machinev1.ExecResponse
			msg, err2 = stream.Recv()
			if err2 == io.EOF {
				err2 = errors.New("premature disconnect")
				break out
			} else if err != nil {
				break out
			}
			if msg == nil {
				err2 = errors.New("blank message")
				break out
			}
			if msg.Data == nil {
				err2 = errors.New("blank message")
				break out
			}
			switch x := msg.Data.(type) {
			case *machinev1.ExecResponse_ExitCode:
				if x.ExitCode != 0 {
					err2 = fmt.Errorf("process non-zero exit %d", x.ExitCode)
				}
				// always close
				break out
			case *machinev1.ExecResponse_Stdio:
				switch x.Stdio.Channel {
				case 1:
					_, err2 = io.Copy(stdoutWrite, bytes.NewBuffer(x.Stdio.Data))
				case 2:
					_, err2 = io.Copy(stderrWrite, bytes.NewBuffer(x.Stdio.Data))
				default:
					err2 = fmt.Errorf("unknown channel %d", x.Stdio.Channel)
				}
			default:
				err2 = fmt.Errorf("unknown type %T", x)
			}
			if err2 != nil {
				break out
			}
		}
		if err2 != nil {
			_ = stdoutWrite.CloseWithError(err2)
			_ = stderrWrite.CloseWithError(err2)
		} else {
			_ = stdoutWrite.CloseWithError(io.EOF)
			_ = stderrWrite.CloseWithError(io.EOF)
		}
	}()
	return stdinWrite, stdoutRead, stderrRead
}
