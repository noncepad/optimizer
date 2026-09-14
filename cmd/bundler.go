package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/optimizer/bundler/astralane"
	"git.noncepad.com/pkg/optimizer/bundler/jito"
)

// startBundlerTipBroadcaster wires up every known Bundler (astralane live,
// jito a not-yet-implemented placeholder -- see bundler/jito's doc
// comment, its Tip()/Distribution() always error so RunTipBroadcaster
// reports it down on every poll) and runs bundler.RunTipBroadcaster in its
// own goroutine for the life of ctx, pushing live tip state to send
// (typically a bot mode's own Hook.SendBundlerTipUpdate). db is the
// already-open prefetch.db connection (pass prefetchDB.Raw()) that
// RunTipBroadcaster persists real updates to and falls back to when a live
// poll fails; may be nil to disable persistence/fallback. Logs and
// continues if a bundler fails to construct, rather than failing the
// whole command over what's meant to be a best-effort feature -- shared
// across every cmd/*.go entry point that wires up a real bot mode.
func startBundlerTipBroadcaster(ctx context.Context, entry *slog.Logger, db *sql.DB, send func(bundler.TipUpdate) error) {
	var bundlers []bundler.Bundler
	astralaneBundler, err := astralane.Create(ctx, entry)
	if err != nil {
		entry.Warn(fmt.Sprintf("bundler: failed to create astralane bundler: %s", err))
	} else {
		bundlers = append(bundlers, astralaneBundler)
	}
	jitoBundler, err := jito.Create(ctx)
	if err != nil {
		entry.Warn(fmt.Sprintf("bundler: failed to create jito bundler: %s", err))
	} else {
		bundlers = append(bundlers, jitoBundler)
	}
	if len(bundlers) == 0 {
		return
	}
	go bundler.RunTipBroadcaster(ctx, entry, db, bundlers, send)
}
