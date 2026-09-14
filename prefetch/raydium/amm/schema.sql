-- raydium_amm_pool stores the compact Summary produced by the AMM v4 downloader,
-- plus the OpenBook/Serum market accounts needed to build a swap instruction
-- (fetched by subscribing to the pool's `market` account after it's discovered).
CREATE TABLE IF NOT EXISTS raydium_amm_pool (
    pubkey               BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes: the AMM pool account
    coin_vault           BLOB    NOT NULL,              -- 32 raw bytes: base-token vault
    pc_vault             BLOB    NOT NULL,              -- 32 raw bytes: quote-token vault
    coin_balance         INTEGER,                       -- NULL when vault not fetched
    pc_balance           INTEGER,                       -- NULL when vault not fetched
    coin_mint            BLOB,                          -- 32 raw bytes: coin_vault's mint, NULL until vault fetched
    pc_mint              BLOB,                          -- 32 raw bytes: pc_vault's mint, NULL until vault fetched
    market               BLOB,                          -- 32 raw bytes: OpenBook market account
    market_bids          BLOB,                          -- 32 raw bytes: NULL until market account fetched
    market_asks          BLOB,                          -- 32 raw bytes
    market_event_queue   BLOB,                          -- 32 raw bytes
    market_coin_vault    BLOB,                          -- 32 raw bytes: OpenBook market's own base vault
    market_pc_vault      BLOB,                          -- 32 raw bytes: OpenBook market's own quote vault
    market_vault_signer  BLOB,                          -- 32 raw bytes: derived PDA, not read from chain
    last_slot            INTEGER NOT NULL DEFAULT 0     -- slot this row was last (re)written at
);

-- Supports TopPools' "ORDER BY (coin_balance + pc_balance) DESC LIMIT ?"
-- without a full table scan + sort.
CREATE INDEX IF NOT EXISTS idx_amm_liquidity
    ON raydium_amm_pool ((coin_balance + pc_balance));

-- Support findPoolByVault/findPoolByMarket's per-account reverse lookups
-- (vault or market pubkey -> owning pool) without a full table scan. Two
-- single-column indexes (rather than one composite) let SQLite satisfy
-- "coin_vault = ?1 OR pc_vault = ?1" via its OR-optimization, using each
-- index for its half of the OR.
CREATE INDEX IF NOT EXISTS idx_amm_pool_coin_vault
    ON raydium_amm_pool (coin_vault);
CREATE INDEX IF NOT EXISTS idx_amm_pool_pc_vault
    ON raydium_amm_pool (pc_vault);
CREATE INDEX IF NOT EXISTS idx_amm_pool_market
    ON raydium_amm_pool (market);
