package marginfi_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/marginfi"
	"git.noncepad.com/pkg/optimizer/store"
)

func TestPool(t *testing.T) {
	ctx := t.Context()
	var client state.Client
	{
		stateStr := fmt.Sprintf("unix://%s", filepath.Join(os.Getenv("HOME"), ".solpipe.bidder.proxy.sock"))
		stateAddr, err := bidder.ParseAddress(stateStr)
		if err != nil {
			t.Fatal(err)
		}
		dialer := state.DefaultDialer(stateAddr)
		client = state.New(ctx, dialer, 30*time.Second)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "prefetch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
	}()
	fetcher, err := marginfi.Create(ctx, client, s.DB(), true)
	if err != nil {
		t.Fatal(err)
	}

	t.Fatalf("fetcher: %s", fetcher)
}
