CREATE TABLE IF NOT EXISTS raydium_clmm_config (
    pubkey            BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes
    bump              INTEGER NOT NULL,
    config_index      INTEGER NOT NULL,
    owner             BLOB    NOT NULL,              -- 32 raw bytes
    protocol_fee_rate INTEGER NOT NULL,
    trade_fee_rate    INTEGER NOT NULL,
    tick_spacing      INTEGER NOT NULL,
    fund_fee_rate     INTEGER NOT NULL,
    fund_owner        BLOB    NOT NULL,              -- 32 raw bytes
    last_slot         INTEGER NOT NULL DEFAULT 0     -- slot this row was last (re)written at
);

CREATE TABLE IF NOT EXISTS raydium_clmm_pool (
    pubkey            BLOB    NOT NULL PRIMARY KEY,
    amm_config        BLOB    NOT NULL REFERENCES raydium_clmm_config(pubkey),
    owner             BLOB    NOT NULL,
    token_mint0       BLOB    NOT NULL,
    token_mint1       BLOB    NOT NULL,
    token_vault0      BLOB    NOT NULL,
    token_vault1      BLOB    NOT NULL,
    observation_key   BLOB    NOT NULL,
    mint_decimals0    INTEGER NOT NULL,
    mint_decimals1    INTEGER NOT NULL,
    tick_spacing      INTEGER NOT NULL,
    liquidity_lo      INTEGER NOT NULL,  -- low  64 bits of u128 liquidity (stored as i64)
    liquidity_hi      INTEGER NOT NULL,  -- high 64 bits of u128 liquidity
    sqrt_price_lo     INTEGER NOT NULL,  -- low  64 bits of u128 sqrt_price_x64
    sqrt_price_hi     INTEGER NOT NULL,  -- high 64 bits of u128 sqrt_price_x64
    tick_current      INTEGER NOT NULL,
    protocol_fees0    INTEGER NOT NULL,
    protocol_fees1    INTEGER NOT NULL,
    status            INTEGER NOT NULL,
    open_time         INTEGER NOT NULL,
    token0_balance    INTEGER,           -- NULL when vault account not fetched
    token1_balance    INTEGER,           -- NULL when vault account not fetched
    last_slot         INTEGER NOT NULL DEFAULT 0 -- slot this row was last (re)written at
);

CREATE INDEX IF NOT EXISTS idx_clmm_pool_mints
    ON raydium_clmm_pool(token_mint0, token_mint1);

-- Supports TopPools' "ORDER BY (token0_balance + token1_balance) DESC LIMIT ?"
-- without a full table scan + sort.
CREATE INDEX IF NOT EXISTS idx_clmm_liquidity
    ON raydium_clmm_pool ((token0_balance + token1_balance));

-- Support findPoolByVault's per-account "which pool owns this vault"
-- lookup without a full table scan. Two single-column indexes (rather than
-- one composite) let SQLite satisfy "token_vault0 = ?1 OR token_vault1 =
-- ?1" via its OR-optimization, using each index for its half of the OR.
CREATE INDEX IF NOT EXISTS idx_clmm_pool_token_vault0
    ON raydium_clmm_pool (token_vault0);
CREATE INDEX IF NOT EXISTS idx_clmm_pool_token_vault1
    ON raydium_clmm_pool (token_vault1);
