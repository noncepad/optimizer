-- mint_info is a shared cache of SPL Mint decimals, populated opportunistically
-- by every prefetcher that discovers a mint (Orca, Raydium AMM/CPMM/CLMM).
-- Decimals never change once a mint is created, so this table only ever grows
-- and is safe to resume from across runs.
CREATE TABLE IF NOT EXISTS mint_info (
    mint     BLOB    NOT NULL PRIMARY KEY, -- 32 raw bytes
    decimals INTEGER NOT NULL
);
