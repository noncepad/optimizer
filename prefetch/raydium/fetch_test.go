package raydium_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	"git.noncepad.com/pkg/optimizer/store"
)

func TestPool(t *testing.T) {
	ctx := t.Context()
	var client state.Client
	homeDir := t.TempDir()
	//	homeDir, present := os.LookupEnv("HOME")
	//	if !present {
	//		t.Fatal("missing HOME")
	//	}
	workDir := filepath.Join(homeDir, ".optimizer")
	_ = os.Mkdir(workDir, 0o755)
	{
		stateStr := fmt.Sprintf("unix://%s", filepath.Join(os.Getenv("HOME"), ".solpipe.bidder.proxy.sock"))
		stateAddr, err := bidder.ParseAddress(stateStr)
		if err != nil {
			t.Fatal(err)
		}
		dialer := state.DefaultDialer(stateAddr)
		client = state.New(ctx, dialer, 30*time.Second)
	}

	s, err := store.Open(filepath.Join(workDir, "prefetch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
	}()

	mintTracker, err := mintinfo.New(s.DB())
	if err != nil {
		t.Fatal(err)
	}
	maxSubscriptionCount := 1_000
	err = raydium.Create(ctx, client, s.DB(), maxSubscriptionCount, false, mintTracker, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	err = cpmm.ReadPools(s.DB(), func(pool *cpmm.PoolRow) bool {
		if i%1_000 == 0 {
			t.Logf("pool %s; %d", pool.Pubkey, pool.Token0Balance)
		}
		i++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("raydium fetcher:  pool count: %d", i)

	if err = raydium.PrintSummary(s.DB(), os.Stdout); err != nil {
		t.Fatal(err)
	}
}
