package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

type countingCloser struct {
	closes int
	err    error
}

func (c *countingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *countingCloser) Close() error {
	c.closes++
	return c.err
}

func TestOnceCloserOnlyClosesUnderlyingOnce(t *testing.T) {
	underlying := &countingCloser{err: errors.New("boom")}
	c := newOnceCloser(underlying)

	err1 := c.Close()
	err2 := c.Close()
	err3 := c.Close()

	if underlying.closes != 1 {
		t.Fatalf("expected exactly 1 real close, got %d", underlying.closes)
	}
	for i, err := range []error{err1, err2, err3} {
		if err != underlying.err {
			t.Fatalf("call %d: expected the real close's error to be returned every time, got %v", i+1, err)
		}
	}
}

func TestOnceCloserConcurrentCloseOnlyClosesUnderlyingOnce(t *testing.T) {
	underlying := &countingCloser{}
	c := newOnceCloser(underlying)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
	}
	wg.Wait()

	if underlying.closes != 1 {
		t.Fatalf("expected exactly 1 real close across 50 concurrent Close calls, got %d", underlying.closes)
	}
}

func TestChildID(t *testing.T) {
	id := ChildID(1)
	want := sgo.SystemProgramID
	want[31] = 1
	if id != want {
		t.Fatalf("ChildID(1) = %s, want %s", id, want)
	}

	// Every byte except the last 4 must still match SystemProgramID
	// (all zero) -- confirms the encoding only ever touches the right
	// end, regardless of index.
	id = ChildID(0x01020304)
	for i := 0; i < 28; i++ {
		if id[i] != 0 {
			t.Fatalf("ChildID(0x01020304)[%d] = %d, want 0 (only the last 4 bytes should be touched)", i, id[i])
		}
	}
	if id[28] != 0x01 || id[29] != 0x02 || id[30] != 0x03 || id[31] != 0x04 {
		t.Fatalf("ChildID(0x01020304) last 4 bytes = %v, want big-endian [1 2 3 4]", id[28:32])
	}

	if ChildID(5) == ChildID(6) {
		t.Fatalf("different indices must produce different ids")
	}
}

func TestNewUploadRequestAllocatesResultC(t *testing.T) {
	req := NewUploadRequest("arbv1")
	if req.Mode != "arbv1" {
		t.Fatalf("expected mode arbv1, got %q", req.Mode)
	}
	if req.ResultC == nil || cap(req.ResultC) != 1 {
		t.Fatalf("expected a buffered (size 1) ResultC, got %v", req.ResultC)
	}
}

func TestRequestDeliversErrorWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hs := &eventHook{
		ctx: ctx,
		// unbuffered and never drained -- the send case can never become
		// ready, so this only passes if the ctx.Done() branch is what
		// actually fires.
		uploadRequestC: make(chan *UploadRequest),
		mBot:           make(map[sgo.PublicKey]*Bot),
	}
	req := NewUploadRequest("testmode")
	hs.Request(req)
	select {
	case res := <-req.ResultC:
		if res.Err == nil {
			t.Fatalf("expected an error result, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Request to deliver a result")
	}
}

func TestBotsReturnsIndependentSnapshot(t *testing.T) {
	hs := &eventHook{mBot: make(map[sgo.PublicKey]*Bot)}
	key := sgo.NewWallet().PublicKey()
	hs.mBot[key] = &Bot{mode: "x"}

	snap := hs.Bots()
	if len(snap) != 1 || snap[key] == nil || snap[key].Mode() != "x" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	// Mutating the live map after taking the snapshot must not affect it.
	hs.mBot[key] = &Bot{mode: "y"}
	if snap[key].Mode() != "x" {
		t.Fatalf("snapshot was not independent of the live map: got mode %q", snap[key].Mode())
	}
}

func TestCreateLogFileWritesRealFileUnderConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	hs := &eventHook{config: &Configuration{LogDir: dir}}
	pipeline := sgo.NewWallet().PublicKey()

	path, f, err := hs.createLogFile("testmode", pipeline)
	if err != nil {
		t.Fatalf("createLogFile failed: %s", err)
	}
	defer f.Close()

	if filepath.Dir(path) != dir {
		t.Fatalf("expected file under %s, got %s", dir, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file to exist on disk at %s: %s", path, err)
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("expected returned file to be writable: %s", err)
	}
}

func TestCreateLogFileDefaultsToTempDir(t *testing.T) {
	hs := &eventHook{config: &Configuration{}}
	pipeline := sgo.NewWallet().PublicKey()

	path, f, err := hs.createLogFile("testmode", pipeline)
	if err != nil {
		t.Fatalf("createLogFile failed: %s", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(path)
	}()

	if filepath.Dir(path) != os.TempDir() {
		t.Fatalf("expected file under %s (os.TempDir), got %s", os.TempDir(), path)
	}
}
