package prefetch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/shell"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

// go test -v -run ^TestPrmoptV1$ ./prefetch
func TestPromptV1(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	addr := new(simpleAddr)
	addr.port = 2323
	storeDB, err := store.Open(filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db"))
	if err != nil {
		t.Fatal(err)
	}
	// qwen2.5:7b
	// llama3.3:70b-instruct-q4_K_M
	cb, err := prefetch.CreatePrefetchOnlyChat(
		ctx,
		"qwen3-coder:30b",
		"http://localhost:11434",
		storeDB,
		"./fee-payer.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompter, err := cb.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := prompter.Prompt("what are the top raydium clmm pools?")
	if err != nil {
		t.Fatal(err)
	}
	// do some grep or something
	t.Logf("prompt response: %s", answer)
}

// Run the shell server with:
//
//	go test -v -run ^TestShell$ ./prefetch
//
// Do the following command to get a shell
//
//	telnet 127.0.0.1 2323
func TestShell(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	addr := new(simpleAddr)
	addr.port = 2323
	entry := logger.FromContext(ctx)
	storeDB, err := store.Open(filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db"))
	if err != nil {
		t.Fatal(err)
	}
	cb, err := prefetch.CreatePrefetchOnlyChat(
		ctx,
		"qwen2.5:7b",
		"http://localhost:11434",
		storeDB,
		"./fee-payer.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := shell.Create(ctx, addr, entry, cb)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-s.CloseSignal():
	case <-time.After(30 * time.Minute):
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
