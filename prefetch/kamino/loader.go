package kamino

import (
	sgo "github.com/gagliardetto/solana-go"
)

type (
	ReserveConfiguration struct {
		List []*ReserveConfig `json:"list"`
	}
	ReserveConfig struct {
		Pubkey sgo.PublicKey `json:"pubkey"`
	}
)

func (k *Kamino) Args(args []string) ([]string, error) {
	return args, nil
}

func (k *Kamino) Env(mEnv map[string]string) error {
	return nil
}
