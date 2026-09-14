package lstyield

import (
	"context"
	"encoding/binary"
	"fmt"

	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// tokenAccountAmountOffset is the byte offset of the `amount` (u64) field
// in a standard SPL token account -- mint(32) + owner(32) = 64, same
// layout catscope-rust-bot's spl-token account parsing uses.
const tokenAccountAmountOffset = 64

// maxGetMultipleAccounts is the Solana RPC's own hard limit on how many
// pubkeys a single getMultipleAccounts call may request.
const maxGetMultipleAccounts = 100

// FetchRates reads Sanctum's shared lst_state_list account once (real
// sol_value for every LST Sanctum tracks -- verified live against
// jitoSOL's known real rate during development, see this package's doc
// comment) and batch-fetches each requested mint's pool-reserves ATA
// (its real raw token balance), returning sol_per_lst = sol_value /
// reserve for every mint in mints that resolved cleanly. A mint missing
// from the result (rather than erroring the whole call) means its
// reserve account wasn't found or was still zero this poll -- callers
// should just skip it and retry next tick, same as every other real-data
// gap in this codebase.
func FetchRates(ctx context.Context, rpcClient *rpc.Client, mints []sgo.PublicKey) (map[sgo.PublicKey]float64, error) {
	listResp, err := rpcClient.GetAccountInfo(ctx, sanctum.LstStateListPDA)
	if err != nil {
		return nil, fmt.Errorf("lstyield: fetch lst_state_list: %w", err)
	}
	if listResp == nil || listResp.Value == nil {
		return nil, fmt.Errorf("lstyield: lst_state_list account %s not found", sanctum.LstStateListPDA)
	}
	entries := sanctum.ParseLstStateList(listResp.Value.Data.GetBinary())
	solValueByMint := make(map[sgo.PublicKey]uint64, len(entries))
	for _, e := range entries {
		solValueByMint[e.Mint] = e.SolValue
	}

	mintByReserve := make(map[sgo.PublicKey]sgo.PublicKey, len(mints))
	reserveAddrs := make([]sgo.PublicKey, 0, len(mints))
	for _, mint := range mints {
		reservePk, err := sanctum.FindPoolReservesAddress(mint)
		if err != nil {
			continue
		}
		mintByReserve[reservePk] = mint
		reserveAddrs = append(reserveAddrs, reservePk)
	}

	rates := make(map[sgo.PublicKey]float64, len(mints))
	for start := 0; start < len(reserveAddrs); start += maxGetMultipleAccounts {
		end := start + maxGetMultipleAccounts
		if end > len(reserveAddrs) {
			end = len(reserveAddrs)
		}
		chunk := reserveAddrs[start:end]
		out, err := rpcClient.GetMultipleAccounts(ctx, chunk...)
		if err != nil {
			return nil, fmt.Errorf("lstyield: batch fetch reserves: %w", err)
		}
		for i, acc := range out.Value {
			if acc == nil {
				continue
			}
			mint, ok := mintByReserve[chunk[i]]
			if !ok {
				continue
			}
			solValue, ok := solValueByMint[mint]
			if !ok {
				continue
			}
			data := acc.Data.GetBinary()
			if len(data) < tokenAccountAmountOffset+8 {
				continue
			}
			reserve := binary.LittleEndian.Uint64(data[tokenAccountAmountOffset : tokenAccountAmountOffset+8])
			if reserve == 0 {
				continue
			}
			rates[mint] = float64(solValue) / float64(reserve)
		}
	}
	return rates, nil
}
