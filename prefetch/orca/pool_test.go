package orca_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/solpipe-util/graph"
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

	orcaFetcher, err := orca.Create(ctx, client)
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

func TestAll(t *testing.T) {
	ctx := t.Context()
	var stateClient state.Client
	{
		stateStr := fmt.Sprintf("unix://%s", filepath.Join(os.Getenv("HOME"), ".solpipe.bidder.proxy.sock"))
		stateAddr, err := bidder.ParseAddress(stateStr)
		if err != nil {
			t.Fatal(err)
		}
		dialer := state.DefaultDialer(stateAddr)
		stateClient = state.New(ctx, dialer, 30*time.Second)
	}
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         orca.ProgramID,
				FilterWeight: graph.WeightAll,
				Depth:        3,
			},
		},
	)
	if err != nil {
		t.Fatalf("query failed: %s", err)
	}
	{
		em.Lock()
		mProgramDown := em.EdgeDown(orca.ProgramID)
		em.Unlock()
		_, present := mProgramDown[orca.CheckConfigID]
		if !present {
			for k, v := range mProgramDown {
				t.Logf("up__%s -> %d", k, v)
			}
			t.Errorf("pubkey missing %s %s", orca.CheckConfigID, orca.ProgramID)
		}
	}
	{
		em.Lock()
		mConfigUp := em.EdgeUp(orca.CheckConfigID)
		em.Unlock()
		t.Log("config")
		_, present := mConfigUp[orca.ProgramID]
		if !present {
			t.Errorf("pubkey missing %s %s", orca.CheckConfigID, orca.CheckPoolID)
		}
	}
	{
		em.Lock()
		mConfigDown := em.EdgeDown(orca.CheckConfigID)
		em.Unlock()
		t.Log("config")
		_, present := mConfigDown[orca.CheckPoolID]
		if !present {
			t.Errorf("pubkey missing %s %s; %d", orca.CheckConfigID, orca.CheckPoolID, len(mConfigDown))
		}
	}
	{
		em.Lock()
		mUp := em.EdgeUp(orca.CheckPoolID)
		em.Unlock()
		_, present := mUp[orca.CheckConfigID]
		if !present {
			t.Fatalf("pubkey missing %s %s; %d", orca.CheckConfigID, orca.CheckPoolID, len(mUp))
		}
	}
	// t.Fatal("stop")
}

func TestSingle(t *testing.T) {
	ctx := t.Context()
	var stateClient state.Client
	{
		stateStr := fmt.Sprintf("unix://%s", filepath.Join(os.Getenv("HOME"), ".solpipe.bidder.proxy.sock"))
		stateAddr, err := bidder.ParseAddress(stateStr)
		if err != nil {
			t.Fatal(err)
		}
		dialer := state.DefaultDialer(stateAddr)
		stateClient = state.New(ctx, dialer, 30*time.Second)
	}
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         orca.ProgramID,
				FilterWeight: graph.WeightAll,
				Depth:        2,
			},
			{
				Root:         orca.CheckConfigID,
				FilterWeight: graph.WeightAll,
				Depth:        2,
			},
			{
				Root:         orca.CheckPoolID,
				FilterWeight: graph.WeightAll,
				Depth:        2,
			},
		},
	)
	if err != nil {
		t.Fatalf("query failed: %s", err)
	}
	{
		em.Lock()
		mProgramDown := em.EdgeDown(orca.ProgramID)
		em.Unlock()
		_, present := mProgramDown[orca.CheckConfigID]
		if !present {
			for k, v := range mProgramDown {
				t.Logf("up__%s -> %d", k, v)
			}
			t.Errorf("pubkey missing %s %s", orca.CheckConfigID, orca.ProgramID)
		}
	}
	{
		em.Lock()
		mConfigUp := em.EdgeUp(orca.CheckConfigID)
		em.Unlock()
		t.Log("config")
		_, present := mConfigUp[orca.ProgramID]
		if !present {
			t.Errorf("pubkey missing %s %s", orca.CheckConfigID, orca.CheckPoolID)
		}
	}
	{
		em.Lock()
		mConfigDown := em.EdgeDown(orca.CheckConfigID)
		em.Unlock()
		t.Log("config")
		_, present := mConfigDown[orca.CheckPoolID]
		if !present {
			t.Errorf("pubkey missing %s %s", orca.CheckConfigID, orca.CheckPoolID)
		}
	}
	{
		em.Lock()
		mUp := em.EdgeUp(orca.CheckPoolID)
		em.Unlock()
		_, present := mUp[orca.CheckConfigID]
		if !present {
			t.Fatalf("pubkey missing %s %s", orca.CheckConfigID, orca.CheckPoolID)
		}
	}
	// t.Fatal("stop")
}
