package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.noncepad.com/pkg/optimizer/prefetch/alt"
	"git.noncepad.com/pkg/optimizer/store"
	sgo "github.com/gagliardetto/solana-go"
	addresslookuptable "github.com/gagliardetto/solana-go/programs/address-lookup-table"
	"github.com/gagliardetto/solana-go/rpc"
)

// extendBatchSize is a conservative number of addresses per
// ExtendLookupTable call -- well under the real per-transaction limit for
// an instruction this simple, chosen to avoid needing a simulateTransaction
// probe just to find the exact ceiling.
const extendBatchSize = 20

// AltCmd ranks accounts by real usage already reported by a running bot
// (MessageSend::CommonAddressUpdate, persisted into prefetch.db's
// account_usage table by every brain/*/instance.go -- see
// optimizer/prefetch/alt.RecordUsage, the only writer), then (unless
// --dry-run, the default) creates a real on-chain Address Lookup Table
// containing the top-ranked accounts and writes the result into
// prefetch.db's address_lookup_table table (see prefetch/alt/schema.sql)
// -- the same database download-arb/testperp/perp already read and write
// via store.Open. Deliberately does not fall back to scanning transaction
// history via RPC if account_usage is empty -- run the bot for a while
// first. See catscope-rust-bot's build.rs "prefetch db
// (address_lookup_table) → address_lookup_table.rs" section and
// src/wallet.rs's AddressLookupTable::load_default/Wallet::assemble for
// how that table gets picked up and used -- no changes are needed there;
// regenerating prefetch.db and rebuilding the bot is the only remaining
// step.
type AltCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer / lookup-table authority (not bidder proxy fee payer)"`
	RPCURL    string `name:"rpc-url" default:"https://api.mainnet-beta.solana.com" help:"Solana RPC endpoint to send the lookup-table transactions to (only used with --no-dry-run)"`
	TopN      int    `option:"top-n" default:"200" help:"how many of the most-used accounts to put in the lookup table (capped at 256)"`
	DryRun    bool   `option:"dry-run" negatable:"" default:"true" help:"print the ranked account list only -- send nothing, write nothing. Pass --no-dry-run to actually create/extend the table on-chain and persist it to prefetch.db"`
	Table     string `option:"table" help:"skip creating a new lookup table and extend this already-created one (base58 pubkey) instead -- use to resume a run that created a table but failed partway through extending it"`
}

func (r *AltCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx

	if r.TopN > 256 {
		r.TopN = 256
	}

	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("open prefetch db: %w", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()

	accounts, err := alt.TopUsedAccounts(prefetchDB.Raw(), r.TopN)
	if err != nil {
		return fmt.Errorf("query account_usage: %w", err)
	}

	fmt.Printf("%d account(s) ranked by reported usage in %s\n", len(accounts), prefetchDB.FilePath())
	for i, pk := range accounts {
		fmt.Printf("  %3d. %s\n", i+1, pk)
	}
	if len(accounts) == 0 {
		fmt.Println("\nno accounts reported yet -- run the bot (testperp/perp/arb) long enough for its periodic account-usage report to fire, then retry.")
	}

	if r.DryRun {
		fmt.Println("\ndry run: no transaction sent, prefetch.db not written further. Pass --no-dry-run to actually create and populate the lookup table on-chain.")
		return nil
	}
	if len(accounts) == 0 {
		return errors.New("no accounts found to put in the lookup table")
	}

	rpcClient := rpc.New(r.RPCURL)

	var tableAddr sgo.PublicKey
	if len(r.Table) > 0 {
		tableAddr, err = sgo.PublicKeyFromBase58(r.Table)
		if err != nil {
			return fmt.Errorf("invalid --table: %s", err)
		}
		fmt.Printf("\nresuming extension of existing lookup table %s\n", tableAddr)
	} else {
		tableAddr, err = createLookupTable(ctx, rpcClient, parentKey)
		if err != nil {
			return fmt.Errorf("create lookup table: %w", err)
		}
	}

	for i := 0; i < len(accounts); i += extendBatchSize {
		end := i + extendBatchSize
		if end > len(accounts) {
			end = len(accounts)
		}
		batch := accounts[i:end]
		label := fmt.Sprintf("extend lookup table (accounts %d-%d of %d)", i+1, end, len(accounts))
		fmt.Println(label + " ...")
		if err := sendWithRetries(ctx, rpcClient, parentKey, label, func() ([]sgo.Instruction, error) {
			extendIx := addresslookuptable.NewExtendLookupTableInstruction(tableAddr, parentKey.PublicKey(), parentKey.PublicKey(), batch)
			built, err := extendIx.ValidateAndBuild()
			if err != nil {
				return nil, err
			}
			return []sgo.Instruction{built}, nil
		}); err != nil {
			return fmt.Errorf("%s: %w (table=%s -- rerun with --table=%s --no-dry-run to resume)", label, err, tableAddr, tableAddr)
		}
	}

	if err := alt.Replace(prefetchDB.Raw(), tableAddr, accounts); err != nil {
		return fmt.Errorf("persist lookup table to prefetch.db: %w (the lookup table was created and populated on-chain regardless -- rerun with --table=%s --no-dry-run to retry the write)", err, tableAddr)
	}

	fmt.Printf("\ncreated lookup table %s with %d account(s); persisted to %s\n", tableAddr, len(accounts), prefetchDB.FilePath())
	fmt.Println("the table needs ~1 slot of warmup before it's usable in a transaction -- by the time catscope-rust-bot is rebuilt and restarted to pick up the new prefetch.db, that will already have long passed.")
	return nil
}

func createLookupTable(ctx context.Context, rpcClient *rpc.Client, parentKey sgo.PrivateKey) (sgo.PublicKey, error) {
	var tableAddr sgo.PublicKey
	err := sendWithRetries(ctx, rpcClient, parentKey, "create lookup table", func() ([]sgo.Instruction, error) {
		slot, err := rpcClient.GetSlot(ctx, rpc.CommitmentFinalized)
		if err != nil {
			return nil, fmt.Errorf("get slot: %w", err)
		}
		createIx, addr, err := addresslookuptable.NewCreateLookupTableInstruction(parentKey.PublicKey(), parentKey.PublicKey(), slot)
		if err != nil {
			return nil, fmt.Errorf("build create instruction: %w", err)
		}
		built, err := createIx.ValidateAndBuild()
		if err != nil {
			return nil, fmt.Errorf("validate create instruction: %w", err)
		}
		tableAddr = addr
		fmt.Printf("\ncreating lookup table %s (slot %d) ...\n", addr, slot)
		return []sgo.Instruction{built}, nil
	})
	return tableAddr, err
}

// rpcRetryAttempts/rpcRetryDelay bound retries of a build+send+confirm
// sequence against transient public-RPC-pool inconsistencies: the
// mainnet-beta endpoint load-balances across many backend nodes with
// slightly different views of the chain tip, so e.g. a slot fetched from
// one node can be a skipped/absent SlotHashes entry on whichever node ends
// up simulating the send ("<slot> is not a recent slot"), or a table
// created via one node hasn't yet replicated to the node simulating a
// following ExtendLookupTable ("Invalid account owner"/"Lookup table owner
// should be the Address Lookup Table program") -- both observed live
// against this same endpoint. A fresh slot/blockhash plus a short delay
// for replication to catch up on retry reliably clears both.
const rpcRetryAttempts = 5

const rpcRetryDelay = 3 * time.Second

// sendWithRetries calls buildFn fresh on every attempt (it must pick up a
// current slot/blockhash each time, not reuse a stale one from an earlier
// failed attempt) and sends+confirms the result.
func sendWithRetries(ctx context.Context, rpcClient *rpc.Client, parentKey sgo.PrivateKey, label string, buildFn func() ([]sgo.Instruction, error)) error {
	var lastErr error
	for attempt := 1; attempt <= rpcRetryAttempts; attempt++ {
		instructions, err := buildFn()
		if err == nil {
			_, err = signAndSendConfirmed(ctx, rpcClient, parentKey, instructions)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		fmt.Printf("%s: attempt %d/%d failed: %s\n", label, attempt, rpcRetryAttempts, err)
		if attempt == rpcRetryAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rpcRetryDelay):
		}
	}
	return fmt.Errorf("all %d attempts failed, last error: %w", rpcRetryAttempts, lastErr)
}

// signAndSendConfirmed builds a legacy transaction (ALT-management
// instructions always precede the table's own warmup, so there's nothing
// to look up yet -- v0/ALT support is irrelevant here), signs it with
// signer as sole signer and fee payer, sends it, and blocks until it lands
// or fails.
func signAndSendConfirmed(ctx context.Context, rpcClient *rpc.Client, signer sgo.PrivateKey, instructions []sgo.Instruction) (sgo.Signature, error) {
	bh, err := rpcClient.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return sgo.Signature{}, fmt.Errorf("get latest blockhash: %w", err)
	}
	tx, err := sgo.NewTransaction(instructions, bh.Value.Blockhash, sgo.TransactionPayer(signer.PublicKey()))
	if err != nil {
		return sgo.Signature{}, fmt.Errorf("build transaction: %w", err)
	}
	if _, err := tx.Sign(func(key sgo.PublicKey) *sgo.PrivateKey {
		if key.Equals(signer.PublicKey()) {
			return &signer
		}
		return nil
	}); err != nil {
		return sgo.Signature{}, fmt.Errorf("sign transaction: %w", err)
	}
	sig, err := rpcClient.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: rpc.CommitmentFinalized,
	})
	if err != nil {
		return sgo.Signature{}, fmt.Errorf("send transaction: %w", err)
	}
	if err := confirmSignature(ctx, rpcClient, sig); err != nil {
		return sig, err
	}
	return sig, nil
}

func confirmSignature(ctx context.Context, rpcClient *rpc.Client, sig sgo.Signature) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		res, err := rpcClient.GetSignatureStatuses(ctx, true, sig)
		if err == nil && len(res.Value) == 1 && res.Value[0] != nil {
			st := res.Value[0]
			if st.Err != nil {
				return fmt.Errorf("transaction %s failed on-chain: %v", sig, st.Err)
			}
			if st.ConfirmationStatus == rpc.ConfirmationStatusConfirmed || st.ConfirmationStatus == rpc.ConfirmationStatusFinalized {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for %s to confirm", sig)
}
