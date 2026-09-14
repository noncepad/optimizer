CREATE TABLE IF NOT EXISTS raydium_cpmm_config (
    pubkey            BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes (Solana public key)
    bump              INTEGER NOT NULL,
    disable_create_pool INTEGER NOT NULL,            -- boolean: 0 or 1
    config_index      INTEGER NOT NULL,
    trade_fee_rate    INTEGER NOT NULL,
    protocol_fee_rate INTEGER NOT NULL,
    fund_fee_rate     INTEGER NOT NULL,
    create_pool_fee   INTEGER NOT NULL,
    protocol_owner    BLOB    NOT NULL,              -- 32 raw bytes
    fund_owner        BLOB    NOT NULL,              -- 32 raw bytes
    creator_fee_rate  INTEGER NOT NULL,
    last_slot         INTEGER NOT NULL DEFAULT 0     -- slot this row was last (re)written at
);

CREATE TABLE IF NOT EXISTS raydium_cpmm_pool (
    pubkey          BLOB    NOT NULL PRIMARY KEY,
    amm_config      BLOB    NOT NULL REFERENCES raydium_cpmm_config(pubkey),
    token0_mint     BLOB    NOT NULL,
    token1_mint     BLOB    NOT NULL,
    token0_vault    BLOB    NOT NULL,
    token1_vault    BLOB    NOT NULL,
    lp_mint         BLOB    NOT NULL,
    token0_program  BLOB    NOT NULL,
    token1_program  BLOB    NOT NULL,
    observation_key BLOB    NOT NULL,
    lp_supply       INTEGER NOT NULL,
    token0_balance  INTEGER,           -- NULL when vault account not fetched
    token1_balance  INTEGER,           -- NULL when vault account not fetched
    open_time       INTEGER NOT NULL,
    last_slot       INTEGER NOT NULL DEFAULT 0 -- slot this row was last (re)written at
);

CREATE INDEX IF NOT EXISTS idx_cpmm_pool_mints
    ON raydium_cpmm_pool(token0_mint, token1_mint);

-- Supports TopPools' "ORDER BY (token0_balance + token1_balance) DESC LIMIT ?"
-- without a full table scan + sort.
CREATE INDEX IF NOT EXISTS idx_cpmm_liquidity
    ON raydium_cpmm_pool ((token0_balance + token1_balance));

-- Support findPoolByVault's per-account "which pool owns this vault"
-- lookup without a full table scan. Two single-column indexes (rather than
-- one composite) let SQLite satisfy "token0_vault = ?1 OR token1_vault =
-- ?1" via its OR-optimization, using each index for its half of the OR.
CREATE INDEX IF NOT EXISTS idx_cpmm_pool_token0_vault
    ON raydium_cpmm_pool (token0_vault);
CREATE INDEX IF NOT EXISTS idx_cpmm_pool_token1_vault
    ON raydium_cpmm_pool (token1_vault);
