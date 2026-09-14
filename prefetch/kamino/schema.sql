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
