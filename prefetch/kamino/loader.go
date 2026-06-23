package kamino

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
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

// Load writes kamino.json to targetDirectory for use by build.rs.
func (k *Kamino) Load(targetDirectory string, tp *trading.TradingPair) error {
	fp := filepath.Join(targetDirectory, "kamino.json")
	out := &ReserveConfiguration{
		List: make([]*ReserveConfig, 0, len(k.Reserves)),
	}
	for _, x := range k.Reserves {
		out.List = append(out.List, &ReserveConfig{
			Pubkey: x.Pubkey,
		})
	}
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create kamino.json: %s", err)
	}
	defer func() {
		_ = f.Close()
	}()
	err = json.NewEncoder(f).Encode(out)
	if err != nil {
		return fmt.Errorf("failed to serialize kamino.json: %s", err)
	}
	return nil
}

func (k *Kamino) Env(mEnv map[string]string) error {
	return nil
}
