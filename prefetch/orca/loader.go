package orca

import (
	"encoding/binary"

	_ "modernc.org/sqlite"
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

func (orca *Orca) Env(mEnv map[string]string) error {
	return nil
}
