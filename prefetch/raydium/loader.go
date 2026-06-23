package raydium

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
	sgo "github.com/gagliardetto/solana-go"
)

type (
	PoolConfiguration struct {
		List []*PoolConfig `json:"list"`
	}
	PoolConfig struct {
		Pubkey            sgo.PublicKey `json:"pubkey"`
		MarketBids        sgo.PublicKey `json:"market_bids"`
		MarketAsks        sgo.PublicKey `json:"market_asks"`
		MarketEventQueue  sgo.PublicKey `json:"market_event_queue"`
		MarketCoinVault   sgo.PublicKey `json:"market_coin_vault"`
		MarketPcVault     sgo.PublicKey `json:"market_pc_vault"`
		MarketVaultSigner sgo.PublicKey `json:"market_vault_signer"`
	}
)

func (r *Raydium) Args(args []string) ([]string, error) {
	return args, nil
}

// Load writes raydium_amm.json to targetDirectory for use by build.rs.
func (r *Raydium) Load(targetDirectory string, tp *trading.TradingPair) error {
	{
		fp := filepath.Join(targetDirectory, "raydium_amm.json")
		f, err := os.Create(fp)
		if err != nil {
			return fmt.Errorf("failed amm: %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(r.Amm)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("failed amm 2: %s", err)
		}
	}
	{
		fp := filepath.Join(targetDirectory, "raydium_clmm.json")
		f, err := os.Create(fp)
		if err != nil {
			return fmt.Errorf("failed clmm: %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(r.Clmm)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("failed clmm 2: %s", err)
		}
	}
	{
		fp := filepath.Join(targetDirectory, "raydium_cpmm.json")
		f, err := os.Create(fp)
		if err != nil {
			return fmt.Errorf("failed cpmm: %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(r.Cpmm)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("failed cpmm 2: %s", err)
		}
	}
	return nil
}

func (r *Raydium) Env(mEnv map[string]string) error {
	return nil
}
