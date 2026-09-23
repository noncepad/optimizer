package prefetch

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.noncepad.com/pkg/optimizer/prefetch/alt"
	"git.noncepad.com/pkg/optimizer/prefetch/drift"
	"git.noncepad.com/pkg/optimizer/prefetch/jet"
	"git.noncepad.com/pkg/optimizer/prefetch/kamino"
	"git.noncepad.com/pkg/optimizer/prefetch/marginfi"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/perpfunding"
	"git.noncepad.com/pkg/optimizer/prefetch/phoenix"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpfun"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpswap"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	"git.noncepad.com/pkg/optimizer/prefetch/solend"
	"git.noncepad.com/pkg/optimizer/prefetch/trading"
)

type StaticLoader interface {
	// modify arguments to cargo build
	Args([]string) ([]string, error)
	// put in files that can be statically compiled into the wasm bot
	Load(string, *trading.TradingPair) error
	// Env set environmental variables
	Env(map[string]string) error
}

// Router/pool subscription budget env vars read by catscope-rust-bot's
// build.rs. Set here explicitly (not just left to build.rs's own smaller
// fallback values) so the optimizer is the one source of truth for them
// going forward, and so a future caller can override any of them by
// setting the env var on the optimizer process itself (picked up by the
// os.Environ() merge below) or via a StaticLoader.Env implementation
// (which runs after these defaults, so it always wins).
const (
	EnvNameOrcaPoolBudget        = "ORCA_POOL_BUDGET"
	EnvNameRaydiumAmmPoolBudget  = "RAYDIUM_AMM_POOL_BUDGET"
	EnvNameRaydiumClmmPoolBudget = "RAYDIUM_CLMM_POOL_BUDGET"
	EnvNameRaydiumCpmmPoolBudget = "RAYDIUM_CPMM_POOL_BUDGET"
	EnvNamePumpswapPoolBudget    = "PUMPSWAP_POOL_BUDGET"
	EnvNameTrackedAccountsBudget = "TRACKED_ACCOUNTS_BUDGET"
	EnvNameRouterTokenBudget     = "ROUTER_TOKEN_BUDGET"
	// Not a budget (doesn't affect what gets admitted/subscribed at
	// build time) -- the probe size, in raw lamports, arbv1's periodic
	// "trade router check" diagnostic uses at runtime. Grouped into the
	// same default-env mechanism below since it needs the same
	// "optimizer sets a default, still overridable" treatment. See
	// catscope-rust-bot's build.rs trade_router_probe_lamports doc
	// comment for the full reasoning (100 SOL was too large a probe --
	// legitimately craters against thin-but-real pools -- lowered to a
	// more realistic 0.1 SOL).
	EnvNameTradeRouterProbeLamports = "TRADE_ROUTER_PROBE_LAMPORTS"
	// Which bundler `Wallet::drain_and_send()` requests from the host
	// when a single `evaluate()` tick produces more than one
	// transaction (`transactionprocessor::batch`'s own `bundler:
	// option<u8>` parameter) -- real bundler identity (Jito vs
	// Astralane, etc.) is a host-side concern catscope-rust-bot never
	// interprets, just plumbs through as a raw u8. See build.rs's
	// `bundler` doc comment.
	EnvNameBundler = "BUNDLER"
	// Temporary kill switch (2026-09-04): when "true", catscope-rust-bot's
	// build.rs generates an empty PHOENIX_MARKETS regardless of what's in
	// prefetch.db's phoenix_market table, so no build has any real
	// Phoenix market to find -- disables dispersion's short-index leg,
	// perpfundingv1, phoenixperpsv1, and testperpv1's perp checks alike.
	// Added after a real, live-confirmed incident: the trading wallet's
	// Phoenix Eternal trader account was found frozen on-chain
	// (TraderCapabilityFlags denies DepositCollateral/WithdrawCollateral/
	// RiskIncreasingTrade), with no documented self-service unfreeze
	// path, so every Phoenix-touching cycle was doomed to fail. See
	// build.rs's phoenix_disabled doc comment for the full reasoning and
	// the real, on-chain-confirmed trader account
	// (DbbMev69Fr5ha3Q6M3vmfQxtzmFsy2MFA2jG387TDzRv). Revert (remove this
	// default, or override it to "false") once the trader account is
	// confirmed unfrozen.
	EnvNameDisablePhoenix = "DISABLE_PHOENIX"
)

// Default values for the budget env vars above -- see this file's Build
// for how they're applied. Kept as a map (rather than inlined into Build)
// so a future caller can read/override this table directly instead of
// only being able to override individual env vars after the fact.
//
// Second push, roughly another 2x across the board: a live-deployed bot
// on the first round of increases (Orca 2000->4000, Raydium AMM/CLMM
// 2000->5000) confirmed via the regenerated OUT_DIR pool lists that
// Raydium CLMM/CPMM and Pumpswap fully satisfied their new caps (all
// router-admitted pools fit, e.g. CLMM landed at 2874/5000) while Orca
// and Raydium AMM landed exactly on their new ceilings (4000/4000,
// 5000/5000) -- meaning both still have more router-admitted liquidity
// waiting. Pushed those two harder again; raised the others too for
// headroom as the admitted-pool set grows over time, not because they
// were confirmed still-capped. Also raised ROUTER_TOKEN_BUDGET (the
// shared mint-admission budget every DEX's pool budget draws from,
// unchanged in the first round since it wasn't the bottleneck then) --
// with every per-dex pool budget this much higher, the shared mint
// ceiling is the more likely thing to become the next binding
// constraint, so it needs headroom too, not just the per-dex budgets.
var defaultRouterBudgetEnv = map[string]string{
	EnvNameOrcaPoolBudget:           "8000",
	EnvNameRaydiumAmmPoolBudget:     "10000",
	EnvNameRaydiumClmmPoolBudget:    "8000",
	EnvNameRaydiumCpmmPoolBudget:    "5000",
	EnvNamePumpswapPoolBudget:       "5000",
	EnvNameTrackedAccountsBudget:    "150000",
	EnvNameRouterTokenBudget:        "8000",
	EnvNameTradeRouterProbeLamports: "100000000", // 0.1 SOL
	EnvNameBundler:                  "1",
	EnvNameDisablePhoenix:           "true",
}

// Build an image. Take the data from prefetch (e.g. Orca pool data) and
// write the Json files into the Rust code base so that those files can be
// statically compiled into the web assembly binary.
func (pf *Prefetcher) Build(ctx context.Context, targetRepositoryPath string, liquidityLoader StaticLoader) (*BotImage, error) {
	tmpdir, err := os.MkdirTemp(pf.tmpdir, "prefactor*")
	if err != nil {
		return nil, fmt.Errorf("failed to create logging directory: %s", err)
	}
	botF, err := os.CreateTemp(pf.tmpdir, "bot*")
	if err != nil {
		return nil, fmt.Errorf("failed to create bot blob: %s", err)
	}

	stdoutLog, err := os.Create(filepath.Join(tmpdir, "stdout.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %s", err)
	}
	stderrLog, err := os.Create(filepath.Join(tmpdir, "stderr.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %s", err)
	}
	_ = os.Mkdir(filepath.Join(targetRepositoryPath, "target"), 0o755)
	args := []string{
		"build", "--target", "wasm32-wasip2", "--release",
	}
	args, err = liquidityLoader.Args(args)
	if err != nil {
		return nil, fmt.Errorf("failed to build cargo args: %s", err)
	}

	mEnv := make(map[string]string)
	maps.Copy(mEnv, defaultRouterBudgetEnv)
	for _, v := range os.Environ() {
		y := strings.Split(v, "=")
		if len(y) < 2 {
			return nil, fmt.Errorf("failed to split %s; %d %d", v, len(y), 2)
		}
		mEnv[y[0]] = strings.Join(y[1:], "=")
	}
	mEnv["RUSTFLAGS"] = "-C target-feature=+simd128"
	if err = liquidityLoader.Env(mEnv); err != nil {
		return nil, fmt.Errorf("failed to set liquidity env: %s", err)
	}
	err = pf.trading.Export(filepath.Join(targetRepositoryPath, "target", "trading.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to write trading.json: %s", err)
	}
	err = liquidityLoader.Load(filepath.Join(targetRepositoryPath, "target"), pf.trading)
	if err != nil {
		return nil, fmt.Errorf("failed to write router.json: %w", err)
	}
	// Per-table JSON snapshots -- catscope-rust-bot's build.rs reads these
	// instead of opening prefetch.db directly (see that repo's build.rs
	// doc comments on each table). The five DEX pool tables
	// (raydium_amm/clmm/cpmm_pool, orca_whirlpool_pool, pumpswap_pool) plus
	// sanctum_lst and pumpfun_bonding_curve feed build.rs's router-graph
	// BFS, admission budgets, and top_pools_data.rs -- unlike the simple
	// full-dump tables above, build.rs itself still does all the
	// filtering/ranking/budget logic on the exported rows; only the
	// acquisition (SQL vs JSON) changed. raydium_amm_pool is filtered to
	// pools whose mints both have a known mint_info decimals row (see
	// amm.knownDecimalsPoolQuery's doc comment for why -- raw or
	// decimals-normalized balance is NOT safe to rank-and-truncate this
	// table by, verified against a real prefetch.db) -- every other table
	// here is exported in full.
	targetDir := filepath.Join(targetRepositoryPath, "target")
	rawDB := pf.storeDB.Raw()
	for _, exp := range []struct {
		name string
		fn   func(string) error
	}{
		{"phoenix_market.json", func(p string) error { return phoenix.ExportJSON(rawDB, p) }},
		{"kamino_reserve.json", func(p string) error { return kamino.ExportJSON(rawDB, p) }},
		{"solend_reserve.json", func(p string) error { return solend.ExportJSON(rawDB, p) }},
		{"drift_spot_market.json", func(p string) error { return drift.ExportJSON(rawDB, p) }},
		{"jet_reserve.json", func(p string) error { return jet.ExportJSON(rawDB, p) }},
		{"sanctum_lst.json", func(p string) error { return sanctum.ExportJSON(rawDB, p) }},
		{"marginfi_bank.json", func(p string) error { return marginfi.ExportJSON(rawDB, p) }},
		{"mint_info.json", func(p string) error { return mintinfo.ExportJSON(rawDB, p) }},
		{"perp_funding_target_allocation.json", func(p string) error { return perpfunding.ExportJSON(rawDB, p) }},
		{"address_lookup_table.json", func(p string) error { return alt.ExportJSON(rawDB, p) }},
		{"raydium_amm_pool.json", func(p string) error { return amm.ExportJSON(rawDB, p) }},
		{"raydium_clmm_pool.json", func(p string) error { return clmm.ExportJSON(rawDB, p) }},
		{"raydium_cpmm_pool.json", func(p string) error { return cpmm.ExportJSON(rawDB, p) }},
		{"orca_whirlpool_pool.json", func(p string) error { return orca.ExportJSON(rawDB, p) }},
		{"pumpswap_pool.json", func(p string) error { return pumpswap.ExportJSON(rawDB, p) }},
		{"pumpfun_bonding_curve.json", func(p string) error { return pumpfun.ExportJSON(rawDB, p) }},
	} {
		if err := exp.fn(filepath.Join(targetDir, exp.name)); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", exp.name, err)
		}
	}
	// create the command
	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Env = make([]string, len(mEnv))
	{
		i := 0
		for k, v := range mEnv {
			cmd.Env[i] = fmt.Sprintf("%s=%s", k, v)
			i++
		}
	}
	cmd.Dir = targetRepositoryPath
	cmd.Stdin = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to pipe: %s", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to pipe: %s", err)
	}
	entry := pf.logger
	go func() {
		_, err2 := io.Copy(stdoutLog, stdout)
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	go func() {
		_, err2 := io.Copy(stdoutLog, stdout)
		_ = stdout.Close()
		_ = stdoutLog.Close()
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	go func() {
		_, err2 := io.Copy(stderrLog, stderr)
		_ = stderr.Close()
		_ = stderrLog.Close()
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	//	pf.logger.Warn(fmt.Sprintf("have mEnv %+v", mEnv))
	if len(mEnv) == 0 {
		return nil, fmt.Errorf("empty mEnv %+v", mEnv)
	}
	pf.logger.Info(fmt.Sprintf("compiling - 1 - wasm bot at %s", targetRepositoryPath))
	err = cmd.Run()
	pf.logger.Info(fmt.Sprintf("compiling - 2 - wasm bot at %s", targetRepositoryPath))
	if err != nil {
		lastErr := err
		f, err := os.Open(filepath.Join(tmpdir, "stderr.log"))
		if err == nil {
			_, _ = io.Copy(os.Stderr, f)
			_ = f.Close()
		}
		return nil, fmt.Errorf("program failed: %s", lastErr)
	}
	pf.logger.Info(fmt.Sprintf("compiling - 3 - wasm bot at %s", targetRepositoryPath))
	_ = os.RemoveAll(tmpdir)
	var botFilePath string
	{
		outF, err := os.Open(filepath.Join(targetRepositoryPath, "target", "wasm32-wasip2", "release", "catscope_rust_bot.wasm"))
		if err != nil {
			return nil, fmt.Errorf("failed to copy blob: %s", err)
		}
		pf.logger.Info(fmt.Sprintf("compiling - 4 - wasm bot at %s", targetRepositoryPath))
		_, err = io.Copy(botF, outF)
		botFilePath = botF.Name()
		_ = botF.Close()
		_ = outF.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to copy blob: %s", err)
		}

	}
	pf.logger.Info(fmt.Sprintf("compiled wasm %s", targetRepositoryPath))
	bi := &BotImage{path: botFilePath}
	//	runtime.AddCleanup(bi, func(fp string) {
	//		_ = os.RemoveAll(fp)
	//	}, botFilePath)
	return bi, nil
}

type BotImage struct {
	path string
}

func (bi *BotImage) Path() string {
	return bi.path
}
