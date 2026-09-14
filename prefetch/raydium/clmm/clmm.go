// Package clmm tracks clmm
package clmm

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// Anchor discriminators: sha256("account:<TypeName>")[0..8]
var (
	DiscPoolState = [8]byte{247, 237, 227, 245, 215, 195, 222, 70}
	DiscAmmConfig = [8]byte{218, 244, 33, 104, 203, 203, 43, 111}
)
var ProgramID = sgo.MustPublicKeyFromBase58("CAMMCzo5YL8w4VFF8KVHrK22GGUsp5VTaW7grrKgrWqK")

// Body sizes (excluding the 8-byte Anchor discriminator).
const (
	PoolStateBodySize = 1536
	AmmConfigBodySize = 109
)

// U128 holds a little-endian 128-bit unsigned integer split into two 64-bit halves.
type U128 struct {
	Lo uint64
	Hi uint64
}

// RewardInfo mirrors the Raydium CLMM RewardInfo struct (repr(C, packed), 169 bytes).
type RewardInfo struct {
	RewardState           uint8
	OpenTime              uint64
	EndTime               uint64
	LastUpdateTime        uint64
	EmissionsPerSecondX64 U128
	RewardTotalEmitted    uint64
	RewardClaimed         uint64
	TokenMint             sgo.PublicKey
	TokenVault            sgo.PublicKey
	Authority             sgo.PublicKey
	RewardGrowthGlobalX64 U128
}

// DynamicFeeInfo mirrors the Raydium CLMM DynamicFeeInfo struct (repr(C, packed), 80 bytes).
type DynamicFeeInfo struct {
	FilterPeriod              uint16
	DecayPeriod               uint16
	ReductionFactor           uint16
	DynamicFeeControl         uint32
	MaxVolatilityAccumulator  uint32
	TickSpacingIndexReference int32
	VolatilityReference       uint32
	VolatilityAccumulator     uint32
	LastUpdateTimestamp       uint64
}

// PoolState mirrors the Raydium CLMM PoolState struct (repr(C, packed), 1536 bytes after discriminator).
// Source: https://github.com/raydium-io/raydium-clmm/blob/master/programs/amm/src/states/pool.rs
type PoolState struct {
	Bump                uint8
	AmmConfig           sgo.PublicKey
	Owner               sgo.PublicKey
	TokenMint0          sgo.PublicKey
	TokenMint1          sgo.PublicKey
	TokenVault0         sgo.PublicKey
	TokenVault1         sgo.PublicKey
	ObservationKey      sgo.PublicKey
	MintDecimals0       uint8
	MintDecimals1       uint8
	TickSpacing         uint16
	Liquidity           U128
	SqrtPriceX64        U128
	TickCurrent         int32
	FeeGrowthGlobal0X64 U128
	FeeGrowthGlobal1X64 U128
	ProtocolFeesToken0  uint64
	ProtocolFeesToken1  uint64
	Status              uint8
	FeeOn               uint8
	RewardInfos         [3]RewardInfo
	FundFeesToken0      uint64
	FundFeesToken1      uint64
	OpenTime            uint64
	RecentEpoch         uint64
	DynamicFeeInfo      DynamicFeeInfo
}

// AmmConfig mirrors the Raydium CLMM AmmConfig struct (repr(C, packed), 109 bytes after discriminator).
type AmmConfig struct {
	Bump            uint8
	Index           uint16
	Owner           sgo.PublicKey
	ProtocolFeeRate uint32
	TradeFeeRate    uint32
	TickSpacing     uint16
	FundFeeRate     uint32
	FundOwner       sgo.PublicKey
}

// PoolStateWithToken bundles a parsed pool state with its fetched vault balances.
type PoolStateWithToken struct {
	Info        *PoolState
	TokenVault0 *sgotkn.Account
	TokenVault1 *sgotkn.Account
}

// Amm bundles a parsed AmmConfig with the pools discovered under it. Only
// referenced by the (currently unpopulated) Configuration type below --
// event.go's eventHandler stopped using this shape once pool/vault
// bookkeeping moved to DB-backed lookups (see isConfigFresh/isPoolFresh/
// findPoolByVault).
type Amm struct {
	Config *AmmConfig
	MPool  map[sgo.PublicKey]*PoolStateWithToken
}

// Configuration is the result of a completed CLMM pool download.
type Configuration struct {
	List map[sgo.PublicKey]*Amm `json:"list"`
}

func u8(data []byte, off int) uint8 {
	return data[off]
}

func u16(data []byte, off int) uint16 {
	return binary.LittleEndian.Uint16(data[off : off+2])
}

func u32(data []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(data[off : off+4])
}

func i32(data []byte, off int) int32 {
	return int32(binary.LittleEndian.Uint32(data[off : off+4]))
}

func u64(data []byte, off int) uint64 {
	return binary.LittleEndian.Uint64(data[off : off+8])
}

func u128(data []byte, off int) U128 {
	return U128{
		Lo: binary.LittleEndian.Uint64(data[off : off+8]),
		Hi: binary.LittleEndian.Uint64(data[off+8 : off+16]),
	}
}

func pubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

func parseRewardInfo(data []byte, off int) RewardInfo {
	return RewardInfo{
		RewardState:           u8(data, off+0),
		OpenTime:              u64(data, off+1),
		EndTime:               u64(data, off+9),
		LastUpdateTime:        u64(data, off+17),
		EmissionsPerSecondX64: u128(data, off+25),
		RewardTotalEmitted:    u64(data, off+41),
		RewardClaimed:         u64(data, off+49),
		TokenMint:             pubkey(data, off+57),
		TokenVault:            pubkey(data, off+89),
		Authority:             pubkey(data, off+121),
		RewardGrowthGlobalX64: u128(data, off+153),
	}
}

// ParsePoolState deserialises the body of a PoolState account (after the 8-byte discriminator).
func ParsePoolState(id sgo.PublicKey, body []byte) (*PoolState, error) {
	if len(body) != PoolStateBodySize {
		return nil, fmt.Errorf("clmm: PoolState expected %d bytes, got %d", PoolStateBodySize, len(body))
	}
	p := &PoolState{}
	p.Bump = u8(body, 0)
	p.AmmConfig = pubkey(body, 1)
	p.Owner = pubkey(body, 33)
	p.TokenMint0 = pubkey(body, 65)
	p.TokenMint1 = pubkey(body, 97)
	p.TokenVault0 = pubkey(body, 129)
	p.TokenVault1 = pubkey(body, 161)
	p.ObservationKey = pubkey(body, 193)
	p.MintDecimals0 = u8(body, 225)
	p.MintDecimals1 = u8(body, 226)
	p.TickSpacing = u16(body, 227)
	p.Liquidity = u128(body, 229)
	p.SqrtPriceX64 = u128(body, 245)
	p.TickCurrent = i32(body, 261)
	// padding3 u16 at 265, padding4 u16 at 267 (skipped)
	p.FeeGrowthGlobal0X64 = u128(body, 269)
	p.FeeGrowthGlobal1X64 = u128(body, 285)
	p.ProtocolFeesToken0 = u64(body, 301)
	p.ProtocolFeesToken1 = u64(body, 309)
	// padding5 [u128; 4] at 317 (skipped)
	p.Status = u8(body, 381)
	p.FeeOn = u8(body, 382)
	// padding [u8; 6] at 383 (skipped)
	p.RewardInfos[0] = parseRewardInfo(body, 389)
	p.RewardInfos[1] = parseRewardInfo(body, 558)
	p.RewardInfos[2] = parseRewardInfo(body, 727)
	// tick_array_bitmap [u64;16] at 896, padding6 [u64;4] at 1024 (skipped)
	p.FundFeesToken0 = u64(body, 1056)
	p.FundFeesToken1 = u64(body, 1064)
	p.OpenTime = u64(body, 1072)
	p.RecentEpoch = u64(body, 1080)
	p.DynamicFeeInfo = DynamicFeeInfo{
		FilterPeriod:              u16(body, 1088),
		DecayPeriod:               u16(body, 1090),
		ReductionFactor:           u16(body, 1092),
		DynamicFeeControl:         u32(body, 1094),
		MaxVolatilityAccumulator:  u32(body, 1098),
		TickSpacingIndexReference: i32(body, 1102),
		VolatilityReference:       u32(body, 1106),
		VolatilityAccumulator:     u32(body, 1110),
		LastUpdateTimestamp:       u64(body, 1114),
	}
	_ = id
	return p, nil
}

// ParseAmmConfig deserialises the body of an AmmConfig account (after the 8-byte discriminator).
func ParseAmmConfig(id sgo.PublicKey, body []byte) (*AmmConfig, error) {
	if len(body) != AmmConfigBodySize {
		return nil, fmt.Errorf("clmm: AmmConfig expected %d bytes, got %d", AmmConfigBodySize, len(body))
	}
	c := &AmmConfig{}
	c.Bump = u8(body, 0)
	c.Index = u16(body, 1)
	c.Owner = pubkey(body, 3)
	c.ProtocolFeeRate = u32(body, 35)
	c.TradeFeeRate = u32(body, 39)
	c.TickSpacing = u16(body, 43)
	c.FundFeeRate = u32(body, 45)
	// padding_u32 at 49 (skipped)
	c.FundOwner = pubkey(body, 53)
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
		return fmt.Errorf("clmm: check pool count: %w", err)
	}
	if n > 0 && !force {
		entry.Info(fmt.Sprintf("clmm: %d pools already in db, skipping fetch", n))
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
	return nil
}
