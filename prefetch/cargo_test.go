package prefetch_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/store"
	"github.com/joho/godotenv"
)

func TestOrca(t *testing.T) {
	workDir := t.TempDir()
	dbFp := filepath.Join(workDir, "prefetch.db")
	s, err := store.Open(dbFp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
	}()
	err = godotenv.Load("../.env")
	if err != nil {
		t.Log(err)
	}
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
	wg := &sync.WaitGroup{}

	pf, err := prefetch.Create(ctx, wg, client, s)
	if err != nil {
		t.Fatal(err)
	}
	mintTracker, err := mintinfo.New(s.DB())
	if err != nil {
		t.Fatal(err)
	}
	_, err = orca.Create(ctx, pf.State(), s.DB(), 1, false, mintTracker)
	if err != nil {
		t.Fatal(err)
	}
	liquidityLoader := liquidity.Create(liquidity.DefaultConfig())
	repoDir := os.Getenv("REPO")
	_, err = pf.Build(ctx, repoDir, liquidityLoader)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
