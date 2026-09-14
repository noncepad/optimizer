-- sanctum_lst is the source of truth for discovered Sanctum S Controller
-- LSTs, so re-running Create() can resume from this table instead of
-- re-querying the chain.
CREATE TABLE IF NOT EXISTS sanctum_lst (
    mint                 BLOB    NOT NULL PRIMARY KEY,  -- 32 raw bytes
    sol_value_calculator BLOB    NOT NULL,              -- 32 raw bytes
    sol_value            INTEGER NOT NULL,
    pool_state           BLOB,                          -- 32 raw bytes; NULL if unknown, see registry.go
    reserve              INTEGER NOT NULL DEFAULT 0      -- raw token balance of the pool-reserves ATA, see event.go
);
