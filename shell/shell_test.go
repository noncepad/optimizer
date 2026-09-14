package shell_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/shell"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

// Run the shell server with:
//
//	go test -v -run ^TestExample$ ./shell
//
// Do the following command to get a shell
//
//	telnet 127.0.0.1 2323
func TestExample(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	addr := new(simpleAddr)
	addr.port = 2323
	entry := logger.FromContext(ctx)

	cb, err := shell.CreateExampleChat(ctx, "qwen2.5:7b", "http://localhost:11434")
	if err != nil {
		t.Fatal(err)
	}
	s, err := shell.Create(ctx, addr, entry, cb)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-s.CloseSignal():
	case <-time.After(4 * time.Minute):
	}
	if err != nil {
		t.Fatal(err)
	}
	err = s.Close()
	if err != nil {
		t.Fatal(err)
	}
}

type simpleAddr struct {
	port uint16
}

func (s *simpleAddr) Network() string {
	return "tcp"
}

func (s *simpleAddr) String() string {
	return fmt.Sprintf(":%d", s.port)
}
