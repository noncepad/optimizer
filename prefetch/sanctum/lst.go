package sanctum

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// Sanctum S Controller account layout offsets.
const (
	// pool_state (176 bytes total, no Anchor discriminator)
	poolStateSize     = 176
	offPsTradingFeeBps = 8  // u16
	offPsLpFeeBps      = 10 // u16

	// lst_state_list entry (80 bytes per entry, no Anchor discriminator)
	lstStateSize             = 80
	offLsSolValue            = 8  // u64
	offLsMint                = 16 // Pubkey (32 bytes)
	offLsSolValueCalculator  = 48 // Pubkey (32 bytes)
)

// PDAs derived from the Sanctum S Controller program.
var (
	poolStatePDA    sgo.PublicKey
	lstStateListPDA sgo.PublicKey
)

func init() {
	var err error
	poolStatePDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("state")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive Sanctum pool_state PDA: %s", err))
	}
	lstStateListPDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("lst-state-list")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive Sanctum lst_state_list PDA: %s", err))
	}
}

// LstEntry holds one LST's parsed state from the Sanctum S Controller.
type LstEntry struct {
	Mint               sgo.PublicKey `json:"mint"`
	SolValueCalculator sgo.PublicKey `json:"sol_value_calculator"`
	SolValue           uint64        `json:"sol_value"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

func readU64(data []byte, off int) uint64 {
	return binary.LittleEndian.Uint64(data[off : off+8])
}

// parseLstStateList parses the lst_state_list account data into a slice of LstEntry.
// Each entry is lstStateSize bytes; the body starts at offset 0 (no discriminator).
func parseLstStateList(data []byte) []*LstEntry {
	n := len(data) / lstStateSize
	entries := make([]*LstEntry, 0, n)
	for i := 0; i < n; i++ {
		off := i * lstStateSize
		entry := &LstEntry{
			SolValue:           readU64(data, off+offLsSolValue),
			Mint:               readPubkey(data, off+offLsMint),
			SolValueCalculator: readPubkey(data, off+offLsSolValueCalculator),
		}
		// Skip zero mints (empty/uninitialized slots)
		if entry.Mint == (sgo.PublicKey{}) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (s *Sanctum) fetch(ctx context.Context, stateClient state.Client, logger *slog.Logger) error {
	// Query both PDAs directly — depth=1 to get their account data
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         poolStatePDA,
				FilterWeight: graph.WeightAll,
				Depth:        1,
			},
			{
				Root:         lstStateListPDA,
				FilterWeight: graph.WeightAll,
				Depth:        1,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("query failed: %s", err)
	}

	em.Lock()

	lstAcct := em.UnsafeAccount(lstStateListPDA)
	if lstAcct == nil {
		em.Unlock()
		return fmt.Errorf("lst_state_list account %s not found", lstStateListPDA)
	}
	lstData := lstAcct.Data()
	em.Unlock()

	s.Lsts = parseLstStateList(lstData)
	logger.Info(fmt.Sprintf("sanctum: found %d LSTs", len(s.Lsts)))
	return nil
}

