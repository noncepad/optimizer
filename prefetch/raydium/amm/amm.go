package amm

import (
	"context"
	"encoding/binary"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// Size is the exact byte length of a serialised AmmInfo account (repr(C, packed)).
const Size = 752

// U128 holds a little-endian 128-bit unsigned integer split into two 64-bit halves.
type U128 struct {
	Lo uint64
	Hi uint64
}

// Fees mirrors the Raydium AMM v4 Fees struct (repr(C, packed), 64 bytes).
type Fees struct {
	MinSeparateNumerator   uint64 // 5
	MinSeparateDenominator uint64 // 10_000
	TradeFeeNumerator      uint64 // 25
	TradeFeeDenominator    uint64 // 10_000
	PnlNumerator           uint64 // 12  (protocol share of the swap fee)
	PnlDenominator         uint64 // 100
	SwapFeeNumerator       uint64 // 25  → 0.25% gross swap fee
	SwapFeeDenominator     uint64 // 10_000
}

// StateData mirrors the Raydium AMM v4 StateData struct (repr(C, packed), 144 bytes).
type StateData struct {
	NeedTakePnlCoin     uint64
	NeedTakePnlPc       uint64
	TotalPnlPc          uint64
	TotalPnlCoin        uint64
	PoolOpenTime        uint64
	PunishPcAmount      uint64
	PunishCoinAmount    uint64
	OrderbookToInitTime uint64
	SwapCoinInAmount    U128
	SwapPcOutAmount     U128
	SwapAccPcFee        uint64
	SwapPcInAmount      U128
	SwapCoinOutAmount   U128
	SwapAccCoinFee      uint64
}
type AmmInfoWithToken struct {
	Info      *AmmInfo
	CoinVault *sgotkn.Account
	PcVault   *sgotkn.Account
}
type Summary struct {
	Pubkey      sgo.PublicKey
	Coin        sgo.PublicKey
	CoinBalance uint64
	Pc          sgo.PublicKey
	PcBalance   uint64
}

// AmmInfo mirrors the Raydium AMM v4 AmmInfo struct (repr(C, packed), 752 bytes).
// Source: https://github.com/raydium-io/raydium-amm/blob/master/program/src/state.rs
type AmmInfo struct {
	Status             uint64 // bitmask: swap/deposit/withdraw/crank enabled
	Nonce              uint64 // bump used to derive amm_authority
	OrderNum           uint64
	Depth              uint64
	CoinDecimals       uint64
	PcDecimals         uint64
	State              uint64 // internal state machine
	ResetFlag          uint64
	MinSize            uint64
	VolMaxCutRatio     uint64
	AmountWave         uint64
	CoinLotSize        uint64 // mirrors OpenBook market
	PcLotSize          uint64
	MinPriceMultiplier uint64
	MaxPriceMultiplier uint64
	SysDecimalValue    uint64

	Fees      Fees
	StateData StateData

	CoinVault     sgo.PublicKey // pool-owned token account for base mint
	PcVault       sgo.PublicKey // pool-owned token account for quote mint
	CoinVaultMint sgo.PublicKey // base mint
	PcVaultMint   sgo.PublicKey // quote mint
	LpMint        sgo.PublicKey
	OpenOrders    sgo.PublicKey // pool's OpenOrders on OpenBook
	Market        sgo.PublicKey // OpenBook market
	MarketProgram sgo.PublicKey // OpenBook program ID
	TargetOrders  sgo.PublicKey
	WithdrawQueue sgo.PublicKey
	LpVault       sgo.PublicKey // pool_temp_lp
	Owner         sgo.PublicKey // admin (multisig)
	LpReserve     uint64
	Padding       [3]uint64
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

// Parse deserialises a raw account data slice into an AmmInfo.
func Parse(pubkeyID sgo.PublicKey, data []byte) (*AmmInfo, error) {
	if len(data) != Size {
		return nil, fmt.Errorf("amm: expected %d bytes, got %d", Size, len(data))
	}
	a := &AmmInfo{}
	a.Status = u64(data, 0)
	a.Nonce = u64(data, 8)
	a.OrderNum = u64(data, 16)
	a.Depth = u64(data, 24)
	a.CoinDecimals = u64(data, 32)
	a.PcDecimals = u64(data, 40)
	a.State = u64(data, 48)
	a.ResetFlag = u64(data, 56)
	a.MinSize = u64(data, 64)
	a.VolMaxCutRatio = u64(data, 72)
	a.AmountWave = u64(data, 80)
	a.CoinLotSize = u64(data, 88)
	a.PcLotSize = u64(data, 96)
	a.MinPriceMultiplier = u64(data, 104)
	a.MaxPriceMultiplier = u64(data, 112)
	a.SysDecimalValue = u64(data, 120)

	// Fees at offset 128
	a.Fees.MinSeparateNumerator = u64(data, 128)
	a.Fees.MinSeparateDenominator = u64(data, 136)
	a.Fees.TradeFeeNumerator = u64(data, 144)
	a.Fees.TradeFeeDenominator = u64(data, 152)
	a.Fees.PnlNumerator = u64(data, 160)
	a.Fees.PnlDenominator = u64(data, 168)
	a.Fees.SwapFeeNumerator = u64(data, 176)
	a.Fees.SwapFeeDenominator = u64(data, 184)

	// StateData at offset 192
	a.StateData.NeedTakePnlCoin = u64(data, 192)
	a.StateData.NeedTakePnlPc = u64(data, 200)
	a.StateData.TotalPnlPc = u64(data, 208)
	a.StateData.TotalPnlCoin = u64(data, 216)
	a.StateData.PoolOpenTime = u64(data, 224)
	a.StateData.PunishPcAmount = u64(data, 232)
	a.StateData.PunishCoinAmount = u64(data, 240)
	a.StateData.OrderbookToInitTime = u64(data, 248)
	a.StateData.SwapCoinInAmount = u128(data, 256)
	a.StateData.SwapPcOutAmount = u128(data, 272)
	a.StateData.SwapAccPcFee = u64(data, 288)
	a.StateData.SwapPcInAmount = u128(data, 296)
	a.StateData.SwapCoinOutAmount = u128(data, 312)
	a.StateData.SwapAccCoinFee = u64(data, 328)

	// Pool-owned accounts at offset 336
	a.CoinVault = pubkey(data, 336)
	a.PcVault = pubkey(data, 368)
	a.CoinVaultMint = pubkey(data, 400)
	a.PcVaultMint = pubkey(data, 432)
	a.LpMint = pubkey(data, 464)
	a.OpenOrders = pubkey(data, 496)
	a.Market = pubkey(data, 528)
	a.MarketProgram = pubkey(data, 560)
	a.TargetOrders = pubkey(data, 592)
	a.WithdrawQueue = pubkey(data, 624)
	a.LpVault = pubkey(data, 656)
	a.Owner = pubkey(data, 688)
	a.LpReserve = u64(data, 720)
	a.Padding[0] = u64(data, 728)
	a.Padding[1] = u64(data, 736)
	a.Padding[2] = u64(data, 744)
	_ = pubkeyID
	return a, nil
}

type Configuration struct {
	List []*Summary
}

func Download(
	parentCtx context.Context,
	stateClient state.Client,
) (*Configuration, error) {
	entry := logger.FromContext(parentCtx)
	ctx, cancel := context.WithCancelCause(parentCtx)
	poolMapC := make(chan map[sgo.PublicKey]*AmmInfoWithToken, 1)
	errorC := make(chan error, 1)
	handler := createHandler(ctx, sgo.SysVarClockPubkey, cancel, poolMapC, errorC, entry)
	_ = stateClient.Hook(handler)

	select {
	case err := <-errorC:
		return nil, err
	case x := <-poolMapC:
		ans := new(Configuration)
		ans.List = make([]*Summary, len(x))
		i := 0
		for pubkey, v := range x {
			var coinBalance uint64
			if v.CoinVault != nil {
				coinBalance = v.CoinVault.Amount
			}
			var pcBalance uint64
			if v.PcVault != nil {
				pcBalance = v.PcVault.Amount
			}
			ans.List[i] = &Summary{
				Pubkey:      pubkey,
				Coin:        v.Info.CoinVault,
				CoinBalance: coinBalance,
				Pc:          v.Info.PcVault,
				PcBalance:   pcBalance,
			}
			i++
		}
		return ans, nil
	}
}
