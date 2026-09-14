-- address_lookup_table stores the account list of each on-chain Address
-- Lookup Table the bot's transactions may reference, in on-chain index
-- order. That order is load-bearing: catscope-rust-bot's
-- AddressLookupTable::load_default (src/wallet.rs) assigns each account
-- its lookup index by this table's row order, which must exactly match
-- the real on-chain table's storage order -- see cmd/alt.go, the only
-- writer of this table, which always deletes and re-inserts a table's
-- full row set together so position never drifts out of sync with a
-- partial update.
CREATE TABLE IF NOT EXISTS address_lookup_table (
    table_pubkey   BLOB    NOT NULL,  -- 32 raw bytes
    position       INTEGER NOT NULL, -- 0-based on-chain index
    account_pubkey BLOB    NOT NULL, -- 32 raw bytes
    PRIMARY KEY (table_pubkey, position)
);

-- account_usage is the bot-reported replacement for an RPC-based
-- transaction-history scan: each running bot periodically reports its
-- most-referenced non-signer accounts (MessageSend::CommonAddressUpdate,
-- catscope-rust-bot's src/message.rs) via the shared
-- KeyFlagCommonAccountUsage dispatch case in every brain/*/instance.go.
-- `count` is the bot's own cumulative-since-process-start count as of its
-- latest report, not a delta -- RecordUsage overwrites rather than sums,
-- see that function's doc comment. cmd/alt.go reads this table directly
-- instead of calling getSignaturesForAddress/getTransaction.
CREATE TABLE IF NOT EXISTS account_usage (
    account_pubkey BLOB    NOT NULL PRIMARY KEY, -- 32 raw bytes
    count          INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL -- unix seconds
);
