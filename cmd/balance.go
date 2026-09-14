package main

import (
	"fmt"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/optimizer/util"
	sgo "github.com/gagliardetto/solana-go"
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
	balance, err := util.FetchWalletBalance(ctx, stateClient, parentKey.PublicKey())
	if err != nil {
		return fmt.Errorf("query failed: %s", err)
	}
	if !balance.Found {
		return fmt.Errorf("parent account %s has no funds", parentKey.PublicKey())
	}
	_, _ = fmt.Printf("parent balance: pubkey %s; slot %d; lamports %d\n", balance.Pubkey, balance.Slot, balance.Lamports)
	mBalance := make(map[sgo.PublicKey]uint64)
	for _, t := range balance.Tokens {
		mBalance[t.Mint] += t.Amount
		_, _ = fmt.Printf("...token mint %s; amount %d\n", t.Mint, t.Amount)
	}
	return nil
}
