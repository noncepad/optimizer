package sanctum

import (
	sgo "github.com/gagliardetto/solana-go"
)

type (
	SanctumConfiguration struct {
		List []*LstConfig `json:"list"`
	}
	LstConfig struct {
		Mint               sgo.PublicKey `json:"mint"`
		SolValueCalculator sgo.PublicKey `json:"sol_value_calculator"`
		// value_calc_accounts left empty; fill manually per LST type
		ValueCalcAccounts []sgo.PublicKey `json:"value_calc_accounts"`
	}
)

func (s *Sanctum) Args(args []string) ([]string, error) {
	return args, nil
}

func (s *Sanctum) Env(mEnv map[string]string) error {
	return nil
}
