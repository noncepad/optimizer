-- orca_whirlpool_pool is the source of truth for discovered Whirlpool pools:
-- the full parsed account state (so re-running Create() can resume from this
-- table instead of re-querying the chain), plus it's what build.rs's compact
-- pubkey/mint_a/mint_b pool list is derived from.
CREATE TABLE IF NOT EXISTS orca_whirlpool_pool (
    pubkey             BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes: the Whirlpool account
    whirlpools_config  BLOB    NOT NULL,              -- 32 raw bytes
    mint_a             BLOB    NOT NULL,              -- 32 raw bytes
    mint_b             BLOB    NOT NULL,              -- 32 raw bytes
    vault_a            BLOB    NOT NULL,              -- 32 raw bytes
    vault_b            BLOB    NOT NULL,              -- 32 raw bytes
    vault_a_balance    INTEGER,                       -- NULL until vault fetched; real SPL token amount, NOT the CLMM virtual reserve
    vault_b_balance    INTEGER,                       -- NULL until vault fetched
    sqrt_price_lo      INTEGER NOT NULL,              -- low  64 bits of u128 sqrt_price_x64 (stored as i64)
    sqrt_price_hi      INTEGER NOT NULL,              -- high 64 bits of u128 sqrt_price_x64
    liquidity_lo       INTEGER NOT NULL,              -- low  64 bits of u128 active liquidity (stored as i64)
    liquidity_hi       INTEGER NOT NULL,              -- high 64 bits of u128 active liquidity
    tick_current_index INTEGER NOT NULL,
    tick_spacing       INTEGER NOT NULL,
    fee_rate           INTEGER NOT NULL,
    last_slot          INTEGER NOT NULL DEFAULT 0     -- slot this row was last (re)written at
);

-- Supports TopPools' descending u128-liquidity ORDER BY (see top.go for why
-- it's expressed as two "(x < 0), x" pairs rather than a plain column sort)
-- without a full table scan + sort.
CREATE INDEX IF NOT EXISTS idx_orca_liquidity
    ON orca_whirlpool_pool (
        (liquidity_hi < 0), liquidity_hi,
        (liquidity_lo < 0), liquidity_lo
    );

-- Support findPoolByVault's per-account reverse lookup (vault pubkey ->
-- owning pool) without a full table scan. Two single-column indexes
-- (rather than one composite) let SQLite satisfy "vault_a = ?1 OR
-- vault_b = ?1" via its OR-optimization -- see raydium_amm_pool's
-- idx_amm_pool_coin_vault/idx_amm_pool_pc_vault for the same pattern.
CREATE INDEX IF NOT EXISTS idx_orca_pool_vault_a
    ON orca_whirlpool_pool (vault_a);
CREATE INDEX IF NOT EXISTS idx_orca_pool_vault_b
    ON orca_whirlpool_pool (vault_b);

-- orca_whirlpool_config_seen tracks which WhirlpoolConfig accounts have
-- already been subscribed to (config -> pool), purely for freshness-based
-- dedup -- configs carry no data this codebase needs, so there's no reason
-- to store more than "we've walked this config's pools as of last_slot".
CREATE TABLE IF NOT EXISTS orca_whirlpool_config_seen (
    pubkey    BLOB NOT NULL PRIMARY KEY,
    last_slot INTEGER NOT NULL DEFAULT 0
);
