-- bundler_tip persists the most recent real tip update seen for each
-- bundler (Bundler.Code(), e.g. bundler.BundlerAstralane) -- RunTipBroadcaster
-- writes here whenever Tip()/Distribution() both succeed, and reads back
-- from here as a fallback whenever they don't (no live websocket snapshot
-- yet), so a freshly-connected bot doesn't have to wait through the
-- ~15s+ it can take Astralane's own tip stream to deliver a first real
-- snapshot before it has any tip data to size a bundler send with.
-- addresses is a flat concatenation of 32-byte raw pubkeys, same layout
-- bundler.TipUpdate.Addresses/DoBundlerTipUpdate already use on the wire.
CREATE TABLE IF NOT EXISTS bundler_tip (
    bundler_code INTEGER NOT NULL PRIMARY KEY,
    addresses    BLOB    NOT NULL, -- N * 32 raw bytes
    p25          INTEGER NOT NULL,
    p50          INTEGER NOT NULL,
    p75          INTEGER NOT NULL,
    p95          INTEGER NOT NULL,
    p99          INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL -- unix seconds
);
