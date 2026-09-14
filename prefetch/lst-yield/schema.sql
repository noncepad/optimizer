-- lst_yield_snapshot is a periodic SOL-per-LST exchange-rate timeseries,
-- one row per (lst_mint, time) poll -- unlike pnl_position_snapshot, this
-- is NOT skip-if-unchanged: the exchange rate moves in small continuous
-- increments (staking rewards), so every poll is a real, distinct data
-- point needed for the rate-of-change (annualized yield) estimate in
-- store.go's EstimateAPY. There is no external oracle here -- sol_per_lst
-- is decoded directly from each LST's own real on-chain state account
-- (SPL Stake Pool's total_lamports/pool_token_supply, or Marinade's own
-- msol_price), the same account layouts already verified in
-- catscope-rust-bot's src/trader/dex/{spl_stake_pool,marinade}.rs.
CREATE TABLE IF NOT EXISTS lst_yield_snapshot (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    time        INTEGER NOT NULL, -- unix seconds
    lst_mint    BLOB    NOT NULL, -- 32 raw bytes
    sol_per_lst REAL    NOT NULL  -- exchange rate at capture time
);
CREATE INDEX IF NOT EXISTS idx_lst_yield_snapshot_mint_time ON lst_yield_snapshot(lst_mint, time);
