-- solend_reserve is the source of truth for discovered Solend/Save
-- reserves, so re-running Create() can resume from this table instead of
-- re-querying the chain.
CREATE TABLE IF NOT EXISTS solend_reserve (
    pubkey         BLOB NOT NULL PRIMARY KEY, -- 32 raw bytes: the reserve account
    lending_market BLOB NOT NULL,             -- 32 raw bytes
    mint           BLOB NOT NULL,             -- 32 raw bytes: liquidity mint
    supply_vault   BLOB NOT NULL,             -- 32 raw bytes
    last_slot      INTEGER NOT NULL DEFAULT 0 -- slot this row was last (re)written at
);

-- solend_lending_market_seen tracks which lending markets have already been
-- subscribed to (lending_market -> reserve), purely for freshness-based
-- dedup -- lending markets carry no data this codebase needs, so there's no
-- reason to store more than "we've walked this market's reserves as of
-- last_slot".
CREATE TABLE IF NOT EXISTS solend_lending_market_seen (
    pubkey    BLOB NOT NULL PRIMARY KEY,
    last_slot INTEGER NOT NULL DEFAULT 0
);
