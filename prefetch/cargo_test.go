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
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"github.com/joho/godotenv"
)

func TestOrca(t *testing.T) {
	err := godotenv.Load("../.env")
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
	pf, err := prefetch.Create(ctx, wg, client)
	if err != nil {
		t.Fatal(err)
	}
	orcaLoader, err := orca.Create(ctx, pf.State())
	if err != nil {
		t.Fatal(err)
	}
	repoDir := os.Getenv("REPO")
	_, err = pf.Build(ctx, repoDir, []prefetch.StaticLoader{orcaLoader})
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
