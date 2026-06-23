package sanctum

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
	sgo "github.com/gagliardetto/solana-go"
)

type (
	SanctumConfiguration struct {
		List []*LstConfig `json:"list"`
	}
	LstConfig struct {
		Mint               sgo.PublicKey   `json:"mint"`
		SolValueCalculator sgo.PublicKey   `json:"sol_value_calculator"`
		// value_calc_accounts left empty; fill manually per LST type
		ValueCalcAccounts  []sgo.PublicKey `json:"value_calc_accounts"`
	}
)

func (s *Sanctum) Args(args []string) ([]string, error) {
	return args, nil
}

// Load writes sanctum.json to targetDirectory for use by build.rs.
// value_calc_accounts is left empty for each LST; the operator fills
// them in manually once the LST sol-value calculator type is known.
func (s *Sanctum) Load(targetDirectory string, tp *trading.TradingPair) error {
	fp := filepath.Join(targetDirectory, "sanctum.json")
	out := &SanctumConfiguration{
		List: make([]*LstConfig, 0, len(s.Lsts)),
	}
	for _, x := range s.Lsts {
		out.List = append(out.List, &LstConfig{
			Mint:               x.Mint,
			SolValueCalculator: x.SolValueCalculator,
			ValueCalcAccounts:  []sgo.PublicKey{},
		})
	}
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create sanctum.json: %s", err)
	}
	defer func() {
		_ = f.Close()
	}()
	err = json.NewEncoder(f).Encode(out)
	if err != nil {
		return fmt.Errorf("failed to serialize sanctum.json: %s", err)
	}
	return nil
}

func (s *Sanctum) Env(mEnv map[string]string) error {
	return nil
}
