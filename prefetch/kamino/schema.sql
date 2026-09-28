-- kamino_reserve is the source of truth for discovered Kamino Lending
-- reserves, so re-running Create() can resume from this table instead of
-- re-querying the chain.
CREATE TABLE IF NOT EXISTS kamino_reserve (
    pubkey         BLOB NOT NULL PRIMARY KEY,  -- 32 raw bytes: the reserve account
    lending_market BLOB NOT NULL,              -- 32 raw bytes
    mint           BLOB NOT NULL,              -- 32 raw bytes: liquidity mint
    supply_vault   BLOB NOT NULL,              -- 32 raw bytes
    fee_vault      BLOB NOT NULL               -- 32 raw bytes
);

-- kamino_xstocks_obligation is the source of truth for discovered
-- Obligation accounts in Kamino's isolated xStocks lending market, found
-- via CreateXstocksObligations's getProgramAccounts scan and filtered to
-- obligations with at least one real deposit or borrow. Re-running the
-- scan can resume/skip via this table, same as kamino_reserve above --
-- see xstocks_obligation.go's own doc comments for why this needs a
-- separate scan (a raw RPC call, not the Catscope graph like every other
-- table in this package).
CREATE TABLE IF NOT EXISTS kamino_xstocks_obligation (
    pubkey BLOB NOT NULL PRIMARY KEY  -- 32 raw bytes: the obligation account
);
