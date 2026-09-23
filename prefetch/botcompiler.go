package prefetch

import (
	"context"
	"fmt"
	"io"
	"os"

	"git.noncepad.com/pkg/optimizer/api"
)

// botCompiler is the one concrete api.BotCompiler: Compile's narrow
// (ctx, sourceDir) -> (io.ReadCloser, error) signature has no room for
// Build's own required StaticLoader argument (router/liquidity config,
// e.g. liquidity.Create(liquidity.DefaultConfig()) -- see every cmd/*.go
// caller of pf.Build), so it's bound once at construction time instead,
// the same way every existing pf.Build call site already builds its
// staticLiquidity once and reuses it.
type botCompiler struct {
	pf     *Prefetcher
	loader StaticLoader
}

// NewBotCompiler wraps pf as an api.BotCompiler, compiling against
// loader's router/liquidity configuration on every Compile call -- pass
// e.g. liquidity.Create(liquidity.DefaultConfig()), the same StaticLoader
// every cmd/*.go bot-mode command already builds before calling pf.Build
// directly.
func NewBotCompiler(pf *Prefetcher, loader StaticLoader) api.BotCompiler {
	return &botCompiler{pf: pf, loader: loader}
}

// Compile runs Prefetcher.Build (export prefetch.db's tables to JSON, then
// `cargo build --target wasm32-wasip2 --release` against sourceDir) and
// streams back the resulting catscope_rust_bot.wasm blob. The returned
// ReadCloser's Close also removes the underlying temp file Build wrote it
// to (BotImage.Path()) -- unlike callers of pf.Build directly (which keep
// using that path afterward, e.g. to hand to a bot-mode Configuration),
// nothing else holds a reference to it once it's been read out here, so
// this is the one place responsible for cleaning it up.
func (bc *botCompiler) Compile(ctx context.Context, sourceDir string) (io.ReadCloser, error) {
	botImage, err := bc.pf.Build(ctx, sourceDir, bc.loader)
	if err != nil {
		return nil, fmt.Errorf("botcompiler: build failed: %w", err)
	}
	f, err := os.Open(botImage.Path())
	if err != nil {
		return nil, fmt.Errorf("botcompiler: open compiled wasm %s: %w", botImage.Path(), err)
	}
	return &compiledBot{File: f}, nil
}

// compiledBot deletes its own backing temp file on Close, once the caller
// is done reading the compiled wasm out of it.
type compiledBot struct {
	*os.File
}

func (c *compiledBot) Close() error {
	path := c.File.Name()
	err := c.File.Close()
	_ = os.Remove(path)
	return err
}
