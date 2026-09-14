-- pnl_position_snapshot is a mark-to-market position history for a
-- trading wallet: one row per (wallet, mint) whenever that position's
-- raw balance actually changes, tagged with the mint's USD price at
-- capture time (when known). There is no per-trade ledger here --
-- deliberately: PnL is computed as a mark-to-market comparison between a
-- mint's earliest and latest recorded snapshot for a wallet, not a sum of
-- realized per-trade gains/losses. See store.go's RecordIfChanged (the
-- only writer -- it skips inserting when the balance hasn't moved since
-- the last row for that wallet+mint, so this table only grows on real
-- position changes) and Positions (the reader that turns this history
-- into start-vs-latest PnL).
CREATE TABLE IF NOT EXISTS pnl_position_snapshot (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    time      INTEGER NOT NULL, -- unix seconds
    wallet    BLOB    NOT NULL, -- 32 raw bytes: the wallet holding this position
    mint      BLOB    NOT NULL, -- 32 raw bytes
    balance   INTEGER NOT NULL, -- raw token amount (u64)
    decimals  INTEGER,          -- mint decimals at capture time; NULL if unknown
    usd_price REAL              -- USD price per whole token at capture time; NULL if unavailable
);
CREATE INDEX IF NOT EXISTS idx_pnl_position_snapshot_wallet_mint_time ON pnl_position_snapshot(wallet, mint, time);
