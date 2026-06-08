package main

import (
	"fmt"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

type BalanceCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
}

func (r *BalanceCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	stateClient := dialer.State()
	em, err := stateClient.QuerySingleShot(ctx, []state.QueryRequest{
		{
			Root:         parentKey.PublicKey(),
			FilterWeight: graph.WeightAll,
			Depth:        2,
		},
	})
	if err != nil {
		return fmt.Errorf("query failed: %s", err)
	}
	em.Lock()
	parentAccount := em.UnsafeAccount(parentKey.PublicKey())
	if parentAccount == nil {
		return fmt.Errorf("parent account %s has no funds", parentKey.PublicKey())
	}
	mEdge := em.EdgeDown(parentKey.PublicKey())
	if mEdge == nil {
		mEdge = make(map[sgo.PublicKey]graph.Weight)
	}
	em.Unlock()
	{
		header := parentAccount.Header()
		_, _ = fmt.Printf("parent balance: pubkey %s; slot %d; lamports %d\n", header.Pubkey, header.Slot, header.Lamports)
	}
	mBalance := make(map[sgo.PublicKey]uint64)
	for pubkey := range mEdge {
		em.Lock()
		account := em.UnsafeAccount(pubkey)
		em.Unlock()
		if account == nil {
			panic(fmt.Errorf("account %s missing", pubkey))
		}
		header := account.Header()
		if !header.Owner.Equals(sgo.TokenProgramID) {
			continue
		}
		data := account.Data()
		if len(data) == 165 {
			d := new(sgotkn.Account)
			err = bin.UnmarshalBorsh(d, data)
			if err != nil {
				return fmt.Errorf("failed to parse token account: %s", err)
			}
			xi, present := mBalance[d.Mint]
			if present {
				mBalance[d.Mint] = xi + d.Amount
			} else {
				mBalance[d.Mint] = d.Amount
			}
			_, _ = fmt.Printf("...token mint %s; amount %d\n", d.Mint, d.Amount)
		}
	}
	return err
}
