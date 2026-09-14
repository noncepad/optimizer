-- pumpfun_bonding_curve is the source of truth for discovered Pump.fun
-- bonding curves, so re-running Create() can resume from this table
-- instead of re-walking the chain.
CREATE TABLE IF NOT EXISTS pumpfun_bonding_curve (
    mint                   BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes
    bonding_curve          BLOB    NOT NULL,               -- 32 raw bytes
    creator                BLOB    NOT NULL,               -- 32 raw bytes
    quote_mint             BLOB    NOT NULL,               -- 32 raw bytes
    virtual_token_reserves INTEGER NOT NULL,
    virtual_sol_reserves   INTEGER NOT NULL,
    real_token_reserves    INTEGER NOT NULL,
    real_sol_reserves      INTEGER NOT NULL,
    complete               INTEGER NOT NULL DEFAULT 0
);
