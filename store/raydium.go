package store

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"

	raydiumamm "git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
)

func pk(p sgo.PublicKey) []byte { return p[:] }

func nullU64(v uint64, ok bool) sql.NullInt64 {
	if !ok {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// WriteRaydiumClmm upserts all CLMM configs and pools.
func (s *DB) WriteRaydiumClmm(list map[sgo.PublicKey]*clmm.Amm) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store clmm: begin: %w", err)
	}
	defer tx.Rollback()

	cfgStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO raydium_clmm_config
		(pubkey, bump, config_index, owner, protocol_fee_rate,
		 trade_fee_rate, tick_spacing, fund_fee_rate, fund_owner)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store clmm: prepare config: %w", err)
	}
	defer cfgStmt.Close()

	poolStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO raydium_clmm_pool
		(pubkey, amm_config, owner, token_mint0, token_mint1,
		 token_vault0, token_vault1, observation_key,
		 mint_decimals0, mint_decimals1, tick_spacing,
		 liquidity_lo, liquidity_hi, sqrt_price_lo, sqrt_price_hi,
		 tick_current, protocol_fees0, protocol_fees1, status, open_time,
		 token0_balance, token1_balance)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store clmm: prepare pool: %w", err)
	}
	defer poolStmt.Close()

	for cfgPubkey, a := range list {
		c := a.Config
		_, err = cfgStmt.Exec(
			pk(cfgPubkey),
			int(c.Bump), int(c.Index), pk(c.Owner),
			int64(c.ProtocolFeeRate), int64(c.TradeFeeRate),
			int(c.TickSpacing), int64(c.FundFeeRate),
			pk(c.FundOwner),
		)
		if err != nil {
			return fmt.Errorf("store clmm: insert config %s: %w", cfgPubkey, err)
		}

		for poolPubkey, p := range a.MPool {
			info := p.Info
			var bal0, bal1 sql.NullInt64
			if p.TokenVault0 != nil {
				bal0 = nullU64(p.TokenVault0.Amount, true)
			}
			if p.TokenVault1 != nil {
				bal1 = nullU64(p.TokenVault1.Amount, true)
			}
			_, err = poolStmt.Exec(
				pk(poolPubkey), pk(cfgPubkey),
				pk(info.Owner),
				pk(info.TokenMint0), pk(info.TokenMint1),
				pk(info.TokenVault0), pk(info.TokenVault1),
				pk(info.ObservationKey),
				int(info.MintDecimals0), int(info.MintDecimals1),
				int(info.TickSpacing),
				int64(info.Liquidity.Lo), int64(info.Liquidity.Hi),
				int64(info.SqrtPriceX64.Lo), int64(info.SqrtPriceX64.Hi),
				int64(info.TickCurrent),
				int64(info.ProtocolFeesToken0), int64(info.ProtocolFeesToken1),
				int(info.Status), int64(info.OpenTime),
				bal0, bal1,
			)
			if err != nil {
				return fmt.Errorf("store clmm: insert pool %s: %w", poolPubkey, err)
			}
		}
	}

	return tx.Commit()
}

// WriteRaydiumAmm upserts all AMM v4 pool summaries.
// Summary carries only vault pubkeys and balances; fee/market fields are omitted
// because AMM full-download is currently disabled in raydium.Create.
func (s *DB) WriteRaydiumAmm(list []*raydiumamm.Summary) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store amm: begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO raydium_amm_pool
		(pubkey, coin_vault, pc_vault, coin_balance, pc_balance)
		VALUES (?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store amm: prepare: %w", err)
	}
	defer stmt.Close()

	for _, row := range list {
		_, err = stmt.Exec(
			pk(row.Pubkey),
			pk(row.Coin),
			pk(row.Pc),
			nullU64(row.CoinBalance, row.CoinBalance != 0),
			nullU64(row.PcBalance, row.PcBalance != 0),
		)
		if err != nil {
			return fmt.Errorf("store amm: insert pool %s: %w", row.Pubkey, err)
		}
	}

	return tx.Commit()
}
