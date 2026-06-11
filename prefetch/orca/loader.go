package orca

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
	sgo "github.com/gagliardetto/solana-go"
)

type (
	PoolConfiguration struct {
		List []*WhirlpoolConfiguration `json:"list"`
	}
	WhirlpoolConfiguration struct {
		Pubkey sgo.PublicKey `json:"pubkey"`
		MintA  sgo.PublicKey `json:"mint_a"`
		MintB  sgo.PublicKey `json:"mint_b"`
	}
)

func (orca *Orca) Args(args []string) ([]string, error) {
	return args, nil
}

// minVaultBalance is the minimum raw token amount each vault must hold for a
// pool to be considered active enough to trade. Tune this per-deployment.
const minVaultBalance = 1_000_000

// vaultBalance reads the SPL token account amount field (offset 64, u64 LE).
// Returns 0 if the data is too short.
func vaultBalance(data []byte) uint64 {
	const amountOffset = 64
	if len(data) < amountOffset+8 {
		return 0
	}
	return binary.LittleEndian.Uint64(data[amountOffset : amountOffset+8])
}

// Load put in files that can be statically compiled into the wasm bot
func (orca *Orca) Load(targetDirectory string, tp *trading.TradingPair) error {
	fp := filepath.Join(targetDirectory, "orca.json")
	out := new(PoolConfiguration)
	maxList := make([]*WhirlpoolConfiguration, len(orca.Pools))
	k := 0
	for _, x := range orca.Pools {
		maxList[k] = &WhirlpoolConfiguration{
			Pubkey: x.Pubkey,
			MintA:  x.TokenMintA,
			MintB:  x.TokenMintB,
		}
		k++
	}
	out.List = maxList[:k]
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create orca.json file: %s", err)
	}
	defer func() {
		_ = f.Close()
	}()
	err = json.NewEncoder(f).Encode(out)
	if err != nil {
		return fmt.Errorf("failed to serialized orca.json: %s", err)
	}
	return nil
}

func (orca *Orca) Env(mEnv map[string]string) error {
	return nil
}
