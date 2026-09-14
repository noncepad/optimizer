package marginfi

import (
	sgo "github.com/gagliardetto/solana-go"
)

// marginfi-v2 Bank account offsets (Anchor, 8-byte discriminator prepended).
// Offsets are absolute from the start of the account (discriminator
// included), verified empirically against live mainnet accounts: mint@8
// resolves to a real SPL Token mint, group@41 matches the pubkey of a real
// on-chain MarginfiGroup account, and oracleSetup@609/oracleKey@610 were
// confirmed against 10 real banks -- oracleSetup decoded to small integers
// (1, 3, 4, 6, 8, 16) matching marginfi's OracleSetup enum ordinals rather
// than one constant (banks mix Pyth/Switchboard/other-protocol oracles and
// even a hardcoded "Fixed" price with no oracle at all), and the one bank
// with oracleSetup==8 ("Fixed") paired with a blank all-1s oracleKey,
// exactly as expected for a mode with no real oracle account.
const (
	offMint        = 8   // Pubkey - bank's asset mint
	offGroup       = 41  // Pubkey
	offOracleSetup = 609 // u8 - OracleSetup enum ordinal; NOT always Pyth
	offOracleKey   = 610 // Pubkey - primary oracle account (oracle_keys[0])
	minBankLen     = offOracleKey + 32 // 642
)

// Bank is the parsed state of a marginfi-v2 Bank account.
type Bank struct {
	Pubkey sgo.PublicKey `json:"pubkey"`
	Group  sgo.PublicKey `json:"group"`
	Mint   sgo.PublicKey `json:"mint"`
	// OracleSetup is the raw config.oracle_setup enum ordinal. It is NOT
	// always Pyth -- marginfi supports Pyth (legacy/push), Switchboard,
	// a hardcoded "Fixed" price (no oracle account at all, OracleKey will
	// be blank), and even reading other protocols' (Kamino/Drift/Solend/
	// Juplend) own oracle accounts. See marginfi-v2's OracleSetup enum for
	// the full variant list before assuming which oracle type this is.
	OracleSetup uint8 `json:"oracle_setup"`
	// OracleKey is config.oracle_keys[0], the primary oracle account to
	// read for this bank's price. Only the first of marginfi's 5 possible
	// oracle key slots is captured -- every real bank sampled during
	// verification used at most one.
	OracleKey sgo.PublicKey `json:"oracle_key"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseBank parses a marginfi-v2 Bank account. body must be the full
// account data including the 8-byte Anchor discriminator -- the offset
// constants above are absolute from the start of the account.
func parseBank(pubkey sgo.PublicKey, body []byte) *Bank {
	if len(body) < minBankLen {
		return nil
	}
	return &Bank{
		Pubkey:      pubkey,
		Group:       readPubkey(body, offGroup),
		Mint:        readPubkey(body, offMint),
		OracleSetup: body[offOracleSetup],
		OracleKey:   readPubkey(body, offOracleKey),
	}
}

