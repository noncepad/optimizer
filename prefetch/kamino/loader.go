package kamino

import (
	sgo "github.com/solana-foundation/solana-go/v2"
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
