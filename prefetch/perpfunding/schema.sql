-- perp_funding_target_allocation is the persisted, latest-write-wins
-- target portfolio allocation per symbol for the perpfundingv1 bot --
-- e.g. allocation_pct=0.30 for SOL means "target 30% of portfolio value
-- in SOL", with the remainder implicitly USD/stable (1.0 minus the sum
-- of every tracked symbol's allocation_pct, no explicit USD row).
-- Rebalancing toward this target is what realizes profit/loss. The
-- optimizer writes here whenever it computes/sends an updated target
-- via CustomMessageInbound (see brain/perpfundingv1/message.go's
-- DoTargetAllocation), and the Rust bot's build.rs reads this same
-- table to bake in the compile-time default a fresh bot starts with
-- before any runtime update arrives.
CREATE TABLE IF NOT EXISTS perp_funding_target_allocation (
    symbol          TEXT NOT NULL PRIMARY KEY,
    allocation_pct  REAL NOT NULL, -- fraction of total portfolio value, 0.0-1.0
    updated_at_unix INTEGER NOT NULL
);
