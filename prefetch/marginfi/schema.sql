-- marginfi_bank is the source of truth for discovered marginfi-v2 Bank
-- accounts, so re-running Create() can resume from this table instead of
-- re-querying the chain.
CREATE TABLE IF NOT EXISTS marginfi_bank (
    pubkey       BLOB NOT NULL PRIMARY KEY, -- 32 raw bytes: the bank account
    group_pubkey BLOB NOT NULL,             -- 32 raw bytes: the MarginfiGroup
    mint         BLOB NOT NULL,             -- 32 raw bytes: the bank's asset mint
    oracle_setup INTEGER NOT NULL,          -- OracleSetup enum ordinal; NOT always Pyth
    oracle_key   BLOB NOT NULL,             -- 32 raw bytes: primary oracle account (blank for "Fixed" setups)
    last_slot    INTEGER NOT NULL DEFAULT 0 -- slot this row was last (re)written at
);

-- marginfi_group_seen tracks which MarginfiGroup accounts have already been
-- subscribed to (group -> bank), purely for freshness-based dedup -- groups
-- carry no data this codebase needs, so there's no reason to store more than
-- "we've walked this group's banks as of last_slot".
CREATE TABLE IF NOT EXISTS marginfi_group_seen (
    pubkey    BLOB NOT NULL PRIMARY KEY,
    last_slot INTEGER NOT NULL DEFAULT 0
);
