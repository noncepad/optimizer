-- pumpswap_pool is the source of truth for discovered PumpSwap AMM pools,
-- so re-running Create() can resume from this table instead of re-walking
-- the chain.
CREATE TABLE IF NOT EXISTS pumpswap_pool (
    pool          BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes
    base_mint     BLOB    NOT NULL,               -- 32 raw bytes
    quote_mint    BLOB    NOT NULL,               -- 32 raw bytes
    base_vault    BLOB    NOT NULL,               -- 32 raw bytes
    quote_vault   BLOB    NOT NULL,               -- 32 raw bytes
    coin_creator  BLOB    NOT NULL,               -- 32 raw bytes
    base_balance  INTEGER NOT NULL DEFAULT 0,
    quote_balance INTEGER NOT NULL DEFAULT 0
);
