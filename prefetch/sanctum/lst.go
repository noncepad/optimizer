package sanctum

import (
	"encoding/binary"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// Sanctum S Controller account layout offsets.
const (
	// pool_state (176 bytes total, no Anchor discriminator)
	poolStateSize      = 176
	offPsTradingFeeBps = 8  // u16
	offPsLpFeeBps      = 10 // u16

	// lst_state_list entry (80 bytes per entry, no Anchor discriminator)
	lstStateSize            = 80
	offLsSolValue           = 8  // u64
	offLsMint               = 16 // Pubkey (32 bytes)
	offLsSolValueCalculator = 48 // Pubkey (32 bytes)
)

// PDAs derived from the Sanctum S Controller program. LstStateListPDA is
// exported so callers outside this package (e.g. prefetch/lst-yield) can
// read the single shared account tracking every Sanctum LST's sol_value
// without re-deriving the PDA themselves.
var (
	poolStatePDA    sgo.PublicKey
	LstStateListPDA sgo.PublicKey
)

// AssociatedTokenProgramID is the real SPL Associated Token Account
// program. Verified directly against igneous-labs/sanctum-ata-sdk
// (core/src/lib.rs) and a decoded live swap_exact_in transaction -- do not
// confuse with the visually-similar but wrong "...LJe1bxr" address that
// was hardcoded on the Rust side for a while (same vanity prefix, wrong
// tail); see catscope-rust-bot's docs/SANCTUM_ROUTING_DEBUG.md for the
// full story of how that bug was found.
var AssociatedTokenProgramID = sgo.MustPublicKeyFromBase58("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")

func init() {
	var err error
	poolStatePDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("state")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive Sanctum pool_state PDA: %s", err))
	}
	LstStateListPDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("lst-state-list")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive Sanctum lst_state_list PDA: %s", err))
	}
}

// FindPoolReservesAddress derives an LST's pool-reserves ATA the same way
// s-controller-lib's find_pool_reserves_address does: standard ATA seeds
// [wallet, token_program, mint] under AssociatedTokenProgramID, with
// wallet = the pool_state PDA -- mirrors sanctum.rs's spl_ata helper on
// the Rust side (now fixed to use the same correct program ID). Exported
// so callers outside this package (e.g. prefetch/lst-yield's generic rate
// fetcher) can derive an LST's reserve ATA without a live Subscribe/Hook
// round-trip.
func FindPoolReservesAddress(mint sgo.PublicKey) (sgo.PublicKey, error) {
	addr, _, err := sgo.FindProgramAddress(
		[][]byte{poolStatePDA[:], sgotkn.ProgramID[:], mint[:]},
		AssociatedTokenProgramID,
	)
	return addr, err
}

// LstEntry holds one LST's parsed state from the Sanctum S Controller.
type LstEntry struct {
	Mint               sgo.PublicKey `json:"mint"`
	SolValueCalculator sgo.PublicKey `json:"sol_value_calculator"`
	SolValue           uint64        `json:"sol_value"`
	// PoolState is the LST's underlying stake-pool account, needed as one
	// of the SOL-value-calculator CPI accounts for the SanctumSpl/
	// SanctumSplMulti/Spl calculator kinds (see registry.go). Zero value
	// means unknown -- either this LST's calculator kind doesn't need it
	// (Marinade/Lido/Wsol are resolved without this field, on the Rust
	// side) or the vendored registry didn't have an entry for it.
	PoolState sgo.PublicKey `json:"pool_state"`
	// Reserve is the raw token balance of this LST's pool-reserves ATA
	// (see findPoolReservesAddress), fetched live during Create. Seeds the
	// Rust side's SanctumState so spot_price() can resolve from the first
	// commit instead of waiting on a live on_token event -- see
	// SanctumLstSetup.reserve in sanctum.rs.
	Reserve uint64 `json:"reserve"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

func readU64(data []byte, off int) uint64 {
	return binary.LittleEndian.Uint64(data[off : off+8])
}

// ParseLstStateList parses the lst_state_list account data into a slice of LstEntry.
// Each entry is lstStateSize bytes; the body starts at offset 0 (no discriminator).
// Exported so callers outside this package (e.g. prefetch/lst-yield's
// generic rate fetcher) can decode a raw account read of LstStateListPDA
// without going through this package's live Subscribe/Hook flow.
func ParseLstStateList(data []byte) []*LstEntry {
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
