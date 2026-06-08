package helloworldv1

import (
	"context"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	MintUSDC = sgo.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")
	MintSOL  = sgo.WrappedSol
)

func (hs *eventHook) initWallet() error {
	hs.childKey = common.DeriveChildKeyFromIndex(hs.parentKey, 1)
	_ = hs.graph.Subscribe(hs.ctx, hs.childKey.PublicKey(), graph.WeightAll, 2)
	hs.builder.AppendKey(hs.parentKey)
	hs.builder.AppendKey(hs.childKey)
	return hs.instance.CustomStdin(DoWallet(hs.childKey))
}

type walletInfo struct {
	parentSOL     graph.Lamports
	childTxFeeSOL graph.Lamports
	childSOL      graph.Lamports
	childCtx      *ctxPair
	transferCount int
}
type ctxPair struct {
	ctx    context.Context
	cancel context.CancelFunc
}

const (
	BudgetChildSOL      graph.Lamports = sgo.LAMPORTS_PER_SOL >> 5
	BudgetChildTxFeeSOL graph.Lamports = sgo.LAMPORTS_PER_SOL >> 6
)

func createWallet() *walletInfo {
	w := new(walletInfo)
	w.parentSOL = 0
	w.childSOL = 0
	w.childTxFeeSOL = 0
	w.childCtx = nil
	w.transferCount = 0
	return w
}

func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	if hs.wallet.childCtx != nil {
		if hs.wallet.childCtx.ctx.Err() != nil {
			hs.wallet.childCtx = nil
		}
	}
	hs.state.mx.Lock()
	for report := range hs.state.listTxLatency.Drain {
		hs.logger.Info(fmt.Sprintf("Transaction Latency Report: %+v", report))
	}
	hs.state.mx.Unlock()
	hs.wallet.parentSOL = solpipeState.System(hs.parentKey.PublicKey())
	hs.wallet.childSOL = solpipeState.System(hs.childKey.PublicKey())
	cp := new(ctxPair)
	cp.ctx, cp.cancel = context.WithTimeout(hs.ctx, 60*time.Second)
	var helper *txbuilder.Helper
	var err error
	// update SOL mint
	if false {
		if hs.wallet.childSOL < BudgetChildSOL && hs.wallet.childCtx == nil && hs.wallet.transferCount == 0 {
			hs.wallet.childCtx = cp
			if helper == nil {
				helper, err = hs.builder.Helper(cp.ctx)
				if err != nil {
					return fmt.Errorf("failed to create helper: %s", err)
				}
			}
			var tokenSOL sgo.PublicKey
			if hs.wallet.childSOL == 0 {
				tokenSOL = helper.ATACreate(hs.childKey.PublicKey(), MintSOL)
			} else {
				tokenSOL, _, _ = sgo.FindAssociatedTokenAddress(hs.childKey.PublicKey(), MintSOL)
			}
			helper.TransferSOL(hs.parentKey.PublicKey(), tokenSOL, BudgetChildSOL-hs.wallet.childSOL)
			helper.TokenSOLSync(tokenSOL)
			// helper.TransferSOL(hs.parentKey.PublicKey(), hs.childKey.PublicKey(), BudgetChildSOL-hs.wallet.childSOL)
		}
		// update transfer fee budget
		if hs.wallet.childTxFeeSOL < BudgetChildTxFeeSOL && hs.wallet.childCtx == nil && hs.wallet.transferCount == 0 {
			if helper == nil {
				helper, err = hs.builder.Helper(cp.ctx)
				if err != nil {
					return fmt.Errorf("failed to create helper: %s", err)
				}
			}
			helper.TransferSOL(hs.parentKey.PublicKey(), hs.childKey.PublicKey(), BudgetChildTxFeeSOL-hs.wallet.childTxFeeSOL)
		}

	}
	if helper != nil {
		entry := hs.logger
		hs.wallet.transferCount++
		go func() {
			sig, slot, err2 := helper.FinishTx()
			cp.cancel()
			if err2 == nil {
				entry.Info(fmt.Sprintf("did transaction %s %d", sig, slot))
			} else {
				entry.Info(fmt.Sprintf("transaction failed: %s", err2))
			}
		}()
	}
	/*
		{
			mBalance := solpipeState.TokenByOwner(hs.parentKey.PublicKey())
			balUSD, present := mBalance[MintUSDC]
			if !present {
				balUSD = 0
			}
			balSOL, present := mBalance[MintUSDC]
			if !present {
				balUSD = 0
			}

		}*/
	return nil
}
