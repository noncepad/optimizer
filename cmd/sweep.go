package main

import (
	"encoding/binary"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	harness "git.noncepad.com/pkg/optimizer/harness"
	sgo "github.com/gagliardetto/solana-go"
)

// SweepCmd sweeps a child wallet's SOL and SPL token balances back to
// the parent fee-payer, via harness.Treasury/Budget.Close -- no WASM bot
// image involved, this only needs the Go-side treasury's own graph
// subscription and txbuilder.
//
// The child swept is the same index-1 child every other bot mode in
// this repo already derives via
// common.DeriveChildKeyFromIndex(parentKey, 1) (arbv1, testperpv1,
// testperplatencyv1, ...) -- reproduced here as the equivalent
// treasury.Budget(id) call (see DeriveChildKeyFromIndex's own body:
// it just builds this same id and calls DeriveChildKeyV2, which is what
// Budget/Child do internally too).
type SweepCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer (parent wallet)"`
	// Generous relative to a real native transfer's typical ~1-2 slot
	// confirmation (see this session's own testlatencylitev1 runs) --
	// this treasury's graph subscription is brand new to this command and
	// its actual warm-up latency hasn't been measured yet, so this errs
	// wide rather than risk sweeping against stale (zero) balances.
	WarmUp time.Duration `option:"warm-up" default:"20s" help:"how long to wait for the treasury's graph subscription to deliver real balances before sweeping."`
	// Close()'s own send is fire-and-forget (logged, not returned) --
	// this is how long Run waits after each call before reading back
	// balances again, so the process doesn't exit (and cancel the
	// context Close's goroutine is still using) before the real
	// transaction has had a chance to land, and so the next iteration
	// (if one is needed) sees this one's results rather than stale state.
	SettleTime time.Duration `option:"settle-time" default:"30s" help:"how long to wait after each Close() call for its transaction to land before checking whether another pass is needed."`
	// Close() only fits as many token-account closes as
	// txbuilder.TxMaxSize allows in one transaction, stopping early and
	// expecting to be called again for the rest (see its own doc
	// comment) -- this bounds how many times Run will do that before
	// giving up, so a wallet that (for whatever real on-chain reason)
	// never actually empties can't loop forever.
	MaxPasses int `option:"max-passes" default:"10" help:"give up after this many Close() passes if the child wallet still isn't empty."`
	// Real, live-confirmed finding (2026-09-15): a child wallet whose own
	// SOL balance is exactly 0 has its own account classified as
	// "deleted" by the underlying graph library (see
	// solpipe.external.onAccount's `Owner.Equals(SystemProgramID) &&
	// Lamports == 0` branch) -- and, correlated with that, its
	// SPLTokenOwner depth-2 discovery never delivers any of its real SPL
	// token accounts either, even though they genuinely exist on-chain
	// (confirmed independently via `spl-token accounts`). The parent
	// (nonzero SOL, not "deleted") had its own token accounts discovered
	// correctly in the same run. Priming the child with a small amount
	// of SOL first (so its account is no longer zero/"deleted") is the
	// cheap, reversible way to test and work around this -- set to 0 to
	// skip priming (e.g. once a child is known to already hold SOL).
	PrimeLamports uint64 `option:"prime-lamports" default:"3000000" help:"if the child's own SOL balance is 0, send this many lamports parent->child first, so its account is no longer 'deleted' on-chain -- needed for its SPL token accounts to be discovered at all. 0 disables priming."`
}

func (r *SweepCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}

	treasury, err := harness.Create(ctx, dialer.State(), dialer.Tx(), parentKey)
	if err != nil {
		return fmt.Errorf("failed to create treasury: %s", err)
	}

	// Same id DeriveChildKeyFromIndex(parentKey, 1) builds internally --
	// a 32-byte pubkey-shaped buffer with the index little-endian in the
	// first 8 bytes, zero elsewhere.
	var id sgo.PublicKey
	binary.LittleEndian.PutUint64(id[:8], 1)

	child := treasury.Child(id)
	fmt.Printf("parent: %s\n", treasury.Parent().PublicKey())
	fmt.Printf("child (index 1): %s\n", child.PublicKey())

	fmt.Printf("waiting up to %s for the treasury's graph subscription to observe real balances...\n", r.WarmUp)
	warmUpDeadline := time.After(r.WarmUp)
warmup:
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during warm-up: %w", ctx.Err())
		case <-warmUpDeadline:
			break warmup
		case <-time.After(2 * time.Second):
			fmt.Printf("  child SOL=%d token=%v\n", child.SOLBalance(), child.TokenBalance())
		}
	}

	if child.SOLBalance() == 0 && 0 < r.PrimeLamports {
		fmt.Printf(
			"child SOL balance is 0 -- priming with %d lamports so its own account is no longer 'deleted' on-chain (needed for its SPL token accounts to be discovered at all -- see this command's own PrimeLamports doc comment)...\n",
			r.PrimeLamports,
		)
		helper, err := dialer.Tx().Helper(ctx)
		if err != nil {
			return fmt.Errorf("failed to create helper for priming transfer: %s", err)
		}
		helper.TransferSOL(treasury.Parent().PublicKey(), child.PublicKey(), r.PrimeLamports)
		sig, slot, err := helper.FinishTx()
		if err != nil {
			return fmt.Errorf("priming transfer failed: %s", err)
		}
		fmt.Printf("priming transfer confirmed: sig=%s slot=%d\n", sig, slot)

		fmt.Printf("waiting up to %s for the treasury to observe the primed balance...\n", r.WarmUp)
		primeDeadline := time.After(r.WarmUp)
	primewait:
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for primed balance: %w", ctx.Err())
			case <-primeDeadline:
				break primewait
			case <-time.After(2 * time.Second):
				fmt.Printf("  child SOL=%d token=%v\n", child.SOLBalance(), child.TokenBalance())
				if child.SOLBalance() > 0 {
					break primewait
				}
			}
		}
	}

	budget := treasury.Budget(id)
	for pass := 1; pass <= r.MaxPasses; pass++ {
		sol := child.SOLBalance()
		tokens := child.TokenBalance()
		if sol == 0 && len(tokens) == 0 {
			fmt.Printf("child wallet is empty -- nothing left to sweep (done after %d pass(es))\n", pass-1)
			break
		}
		fmt.Printf("pass %d/%d -- closing child budget (child SOL=%d token=%v)...\n", pass, r.MaxPasses, sol, tokens)
		budget.Close()

		fmt.Printf("  waiting %s for this pass's transaction to land...\n", r.SettleTime)
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for sweep to land: %w", ctx.Err())
		case <-time.After(r.SettleTime):
		}
		if pass == r.MaxPasses {
			fmt.Printf("reached max-passes (%d) -- child SOL=%d token=%v may still be nonzero, see above\n", r.MaxPasses, child.SOLBalance(), child.TokenBalance())
		}
	}

	fmt.Printf("final (treasury-tracked) child balances -- SOL=%d token=%v\n", child.SOLBalance(), child.TokenBalance())
	fmt.Printf("final (treasury-tracked) parent balances -- SOL=%d token=%v\n", treasury.Parent().SOLBalance(), treasury.Parent().TokenBalance())
	return nil
}
