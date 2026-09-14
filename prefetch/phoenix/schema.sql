-- phoenix_market is the source of truth for discovered Phoenix perpetuals
-- markets, so re-running Create() can resume from this table instead of
-- re-walking the chain. Deliberately just identity (market_account,
-- symbol, asset_id) -- live pricing/risk fields are fetched by the bot
-- directly from PerpAssetMap at runtime, not cached here.
CREATE TABLE IF NOT EXISTS phoenix_market (
    market_account BLOB    NOT NULL PRIMARY KEY, -- 32 raw bytes
    symbol         TEXT    NOT NULL,
    asset_id       INTEGER NOT NULL
);
