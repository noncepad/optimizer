-- multimodel_residual_snapshot persists multimodelv1's real residual/
-- z-score warm-up state, one row per mint, so a restart doesn't need the
-- same multi-minute climb back to real z-scores every time -- see
-- catscope-rust-bot's src/trader/residual_snapshot.rs module doc
-- comment for the full reasoning.
--
-- `payload` is a single-entry trader::residual_snapshot::ResidualSnapshot
-- (encode's output, `n_entries == 1`) -- opaque to this package for its
-- *residual data*, but `mint` (this row's key) is extracted from a fixed
-- byte offset within it (see store.go's mintFromPayload) so writes can be
-- upserted per-mint without a full parse.
--
-- One message/row per mint, not one big row for the whole universe, is
-- load-bearing, not a style choice: the Go<->bot Custom-message channel
-- (github.com/noncepad/catmsg) has a hard MaxValueSize cap (3904 bytes)
-- with no chunking of its own -- a single message covering all ~48
-- curated mints at close to full history genuinely exceeds it
-- (live-confirmed: crashed the bot connection with a "bad value: 3904 vs
-- 4035" deserialize error once enough mints had real history). A single
-- mint's own worst-case payload is ~291 bytes, comfortably under the cap
-- regardless of how many curated symbols this mode ever grows to.
CREATE TABLE IF NOT EXISTS multimodel_residual_snapshot (
    mint            BLOB    NOT NULL PRIMARY KEY, -- 32 raw bytes
    payload         BLOB    NOT NULL,
    updated_at_unix INTEGER NOT NULL
);
