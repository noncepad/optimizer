// Package cpmm tracks cpmm
package cpmm

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// Anchor discriminators: sha256("account:<TypeName>")[0..8]
var (
	DiscPoolState = [8]byte{247, 237, 227, 245, 215, 195, 222, 70}
	DiscAmmConfig = [8]byte{218, 244, 33, 104, 203, 203, 43, 111}
)

// Body sizes (excluding the 8-byte Anchor discriminator).
const (
	PoolStateBodySize = 629
	AmmConfigBodySize = 228
)

var ProgramID = sgo.MustPublicKeyFromBase58("CPMMoo8L3F4NbTegBCKVNunggL7H1ZpdTHKxQB5qKP1C")

// PoolState mirrors the Raydium CPMM PoolState struct (repr(C, packed), 629 bytes after discriminator).
// Source: https://github.com/raydium-io/raydium-cp-swap/blob/master/programs/cp-swap/src/states/pool.rs
type PoolState struct {
	AmmConfig          sgo.PublicKey
	PoolCreator        sgo.PublicKey
	Token0Vault        sgo.PublicKey
	Token1Vault        sgo.PublicKey
	LpMint             sgo.PublicKey
	Token0Mint         sgo.PublicKey
	Token1Mint         sgo.PublicKey
	Token0Program      sgo.PublicKey // SPL Token or Token-2022 program
	Token1Program      sgo.PublicKey
	ObservationKey     sgo.PublicKey
	AuthBump           uint8
	Status             uint8 // bitmask: deposit | withdraw | swap
	LpMintDecimals     uint8
	Mint0Decimals      uint8
	Mint1Decimals      uint8
	LpSupply           uint64
	ProtocolFeesToken0 uint64
	ProtocolFeesToken1 uint64
	FundFeesToken0     uint64
	FundFeesToken1     uint64
	OpenTime           uint64
	RecentEpoch        uint64
	CreatorFeeOn       uint8
	EnableCreatorFee   bool
	CreatorFeesToken0  uint64
	CreatorFeesToken1  uint64
}

// AmmConfig mirrors the Raydium CPMM AmmConfig struct (repr(C, packed), 228 bytes after discriminator).
type AmmConfig struct {
	Bump              uint8
	DisableCreatePool bool
	Index             uint16
	TradeFeeRate      uint64
	ProtocolFeeRate   uint64
	FundFeeRate       uint64
	CreatePoolFee     uint64
	ProtocolOwner     sgo.PublicKey
	FundOwner         sgo.PublicKey
	CreatorFeeRate    uint64
}

// Summary is the compact, JSON-serialisable view of a CPMM pool.
type Summary struct {
	Pubkey       sgo.PublicKey `json:"pubkey"`
	Mint0        sgo.PublicKey `json:"mint_0"`
	Mint1        sgo.PublicKey `json:"mint_1"`
	TradeFeeRate uint64        `json:"trade_fee_rate"`
}

// Configuration is the result of a completed CPMM pool download.
type Configuration struct{}

func u8(data []byte, off int) uint8 {
	return data[off]
}

func u16(data []byte, off int) uint16 {
	return binary.LittleEndian.Uint16(data[off : off+2])
}

func u64(data []byte, off int) uint64 {
	return binary.LittleEndian.Uint64(data[off : off+8])
}

func pubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// ParsePoolState deserialises the body of a PoolState account (after the 8-byte discriminator).
func ParsePoolState(id sgo.PublicKey, body []byte) (*PoolState, error) {
	if len(body) != PoolStateBodySize {
		return nil, fmt.Errorf("cpmm: PoolState expected %d bytes, got %d", PoolStateBodySize, len(body))
	}
	p := &PoolState{}
	p.AmmConfig = pubkey(body, 0)
	p.PoolCreator = pubkey(body, 32)
	p.Token0Vault = pubkey(body, 64)
	p.Token1Vault = pubkey(body, 96)
	p.LpMint = pubkey(body, 128)
	p.Token0Mint = pubkey(body, 160)
	p.Token1Mint = pubkey(body, 192)
	p.Token0Program = pubkey(body, 224)
	p.Token1Program = pubkey(body, 256)
	p.ObservationKey = pubkey(body, 288)
	p.AuthBump = u8(body, 320)
	p.Status = u8(body, 321)
	p.LpMintDecimals = u8(body, 322)
	p.Mint0Decimals = u8(body, 323)
	p.Mint1Decimals = u8(body, 324)
	p.LpSupply = u64(body, 325)
	p.ProtocolFeesToken0 = u64(body, 333)
	p.ProtocolFeesToken1 = u64(body, 341)
	p.FundFeesToken0 = u64(body, 349)
	p.FundFeesToken1 = u64(body, 357)
	p.OpenTime = u64(body, 365)
	p.RecentEpoch = u64(body, 373)
	p.CreatorFeeOn = u8(body, 381)
	p.EnableCreatorFee = body[382] != 0
	// padding1 [u8; 6] at 383 (skipped)
	p.CreatorFeesToken0 = u64(body, 389)
	p.CreatorFeesToken1 = u64(body, 397)
	// padding [u64; 28] at 405 (skipped)
	_ = id
	return p, nil
}

// ParseAmmConfig deserialises the body of an AmmConfig account (after the 8-byte discriminator).
func ParseAmmConfig(id sgo.PublicKey, body []byte) (*AmmConfig, error) {
	if len(body) != AmmConfigBodySize {
		return nil, fmt.Errorf("cpmm: AmmConfig expected %d bytes, got %d", AmmConfigBodySize, len(body))
	}
	c := &AmmConfig{}
	c.Bump = u8(body, 0)
	c.DisableCreatePool = body[1] != 0
	c.Index = u16(body, 2)
	c.TradeFeeRate = u64(body, 4)
	c.ProtocolFeeRate = u64(body, 12)
	c.FundFeeRate = u64(body, 20)
	c.CreatePoolFee = u64(body, 28)
	c.ProtocolOwner = pubkey(body, 36)
	c.FundOwner = pubkey(body, 68)
	c.CreatorFeeRate = u64(body, 100)
	// padding [u64; 15] at 108 (skipped)
	_ = id
	return c, nil
}

func Download(
	parentCtx context.Context,
	stateClient state.Client,
	db *sql.DB,
	maxSubscriptionCount int,
	force bool,
	mintTracker *mintinfo.Tracker,
) error {
	entry := logger.FromContext(parentCtx)
	n, err := poolCount(db)
	if err != nil {
		return fmt.Errorf("cpmm: check pool count: %w", err)
	}
	if n > 0 && !force {
		entry.Info(fmt.Sprintf("cpmm: %d pools already in db, skipping fetch", n))
		return nil
	}
	ctx, cancel := context.WithCancelCause(parentCtx)
	handler := createHandler(ctx, cancel, entry, maxSubscriptionCount, db, mintTracker)
	err = stateClient.Hook(handler)
	cancel(err)
	if err != nil {
		handler.logger.Error(fmt.Sprintf("cause err %s", err))
		return fmt.Errorf("hook failed: %s", err)
	}
	handler.logger.Info(fmt.Sprintf("Download cpmm - finished - 2 - bad pools %d", handler.oldPoolCount))
	return nil
}
