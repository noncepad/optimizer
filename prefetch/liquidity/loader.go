package liquidity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
	sgo "github.com/gagliardetto/solana-go"
)

// routerJSON is the exact layout that build.rs RouterInput deserialises.
type routerJSON struct {
	Lambda              float32          `json:"lambda"`
	MinClusterLiquidity float64          `json:"min_cluster_liquidity"`
	TokenCount          int              `json:"token_count"`
	CoreMints           [5]sgo.PublicKey `json:"core_mints"`
}

// Args passes through cargo build arguments unchanged.
func (l *Liquidity) Args(args []string) ([]string, error) {
	return args, nil
}

// Load writes router.json to targetDirectory.
func (l *Liquidity) Load(targetDirectory string, tp *trading.TradingPair) error {
	l.cfg.TokenCount = tp.Len()
	out := routerJSON{
		Lambda:              l.cfg.Lambda,
		MinClusterLiquidity: l.cfg.MinClusterLiquidity,
		TokenCount:          l.cfg.TokenCount,
		CoreMints:           l.cfg.CoreMints,
	}
	fp := filepath.Join(targetDirectory, "router.json")
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create router.json: %w", err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err = enc.Encode(out); err != nil {
		return fmt.Errorf("failed to serialize router.json: %w", err)
	}
	return nil
}

// Env makes no changes to the build environment.
func (l *Liquidity) Env(mEnv map[string]string) error {
	return nil
}
