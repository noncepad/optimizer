-- obligation_position_snapshot is a per-(wallet, protocol, trade_type,
-- reserve, kind) timeseries of this bot's own Solend/Kamino lending
-- obligation state -- deposits (collateral) and borrows, across every
-- trade type that bootstraps its own obligation (multimodelv1's pair/
-- directional/hawkes, each on both Solend and Kamino -- see
-- catscope-rust-bot's src/brain/multimodelv1/state.rs PAIR_*_OBLIGATION_ID/
-- DIRECTIONAL_*_OBLIGATION_ID/HAWKES_*_OBLIGATION_ID constants for the
-- exact id values this table's obligation_id column mirrors). Skip-if-
-- unchanged, same shape as pnl_position_snapshot -- see store.go's
-- RecordIfChanged.
--
-- amount is the raw on-chain unit for that (protocol, kind): Solend
-- deposits are raw underlying-token units, Solend borrows are WAD-scaled
-- (already /1e18'd before insertion), Kamino deposits are raw cToken
-- units (not yet converted to underlying -- needs the reserve's live
-- collateral exchange rate, deliberately left to the reader rather than
-- computed here), Kamino borrows are SF-scaled (already /2^60'd before
-- insertion). See catscope-rust-bot's src/trader/dex/{solend,kamino}.rs
-- parse_obligation/parse_kamino_obligation for the exact same convention
-- this table's writer (obligation.ParseSolend/ParseKamino) mirrors.
CREATE TABLE IF NOT EXISTS obligation_position_snapshot (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    time         INTEGER NOT NULL, -- unix seconds
    wallet       BLOB    NOT NULL, -- 32 raw bytes, the trading child wallet
    protocol     TEXT    NOT NULL, -- 'solend' or 'kamino'
    trade_type   TEXT    NOT NULL, -- 'pair', 'directional', or 'hawkes'
    obligation_id INTEGER NOT NULL, -- the bot's own per-trade-type id byte
    kind         TEXT    NOT NULL, -- 'deposit' or 'borrow'
    reserve      BLOB    NOT NULL, -- 32 raw bytes, the reserve this entry is against
    amount       INTEGER NOT NULL  -- raw on-chain unit, see this table's own doc comment
);
CREATE INDEX IF NOT EXISTS idx_obligation_position_snapshot_lookup
    ON obligation_position_snapshot(wallet, protocol, trade_type, reserve, kind, time);
