-- drift_spot_market is the source of truth for discovered Drift v2
-- SpotMarket accounts, so re-running Create() can resume from this table
-- instead of re-querying the chain.
CREATE TABLE IF NOT EXISTS drift_spot_market (
    pubkey BLOB NOT NULL PRIMARY KEY, -- 32 raw bytes: the spot market account
    mint   BLOB NOT NULL,             -- 32 raw bytes
    vault  BLOB NOT NULL              -- 32 raw bytes: the spot market's token vault
);
