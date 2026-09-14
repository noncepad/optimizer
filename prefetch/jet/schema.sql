-- jet_reserve is the source of truth for discovered Jet Protocol V1
-- reserves, so re-running Create() can resume from this table instead of
-- re-querying the chain.
CREATE TABLE IF NOT EXISTS jet_reserve (
    pubkey       BLOB NOT NULL PRIMARY KEY, -- 32 raw bytes: the reserve account
    market       BLOB NOT NULL,             -- 32 raw bytes
    mint         BLOB NOT NULL,             -- 32 raw bytes: liquidity mint
    vault        BLOB NOT NULL              -- 32 raw bytes
);
