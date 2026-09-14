package orca_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/store"
	sgo "github.com/gagliardetto/solana-go"
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
	mintTracker, err := mintinfo.New(s.DB())
	if err != nil {
		t.Fatal(err)
	}
	maxSubscriptionCount := 30
	orcaFetcher, err := orca.Create(ctx, client, s.DB(), maxSubscriptionCount, false, mintTracker)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("orca fetcher: %s", orcaFetcher)
	checkPoolID := sgo.MustPublicKeyFromBase58("Czfq3xZZDmsdGdUyrNLtRhGc47cXcZtLG4crryfu44zE")
	whirlpool := orcaFetcher.Find(checkPoolID)
	if whirlpool == nil {
		t.Fatalf("missing pool %s", checkPoolID)
	}
}
