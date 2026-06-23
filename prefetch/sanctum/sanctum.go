// Package sanctum preloads Sanctum S Controller LST pool state.
package sanctum

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("5ocnV1qiCgaQR8Jb8xWnVbApfaygJ8tNoZfgPwsgx9kx")
)

// Sanctum holds pool state and LST entries loaded at startup.
type Sanctum struct {
	Lsts []*LstEntry
}

const sanctumFilePath = "sanctum_fetch.json"

// Create queries the Sanctum S Controller PDAs and parses LST entries.
func Create(ctx context.Context, stateClient state.Client, workingDir string) (*Sanctum, error) {
	entry := logger.FromContext(ctx)
	s := new(Sanctum)
	fp := filepath.Join(workingDir, sanctumFilePath)
	f, err := os.Open(fp)
	if err != nil {
		err = s.fetch(ctx, stateClient, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to load sanctum data: %s", err)
		}
		f, err = os.Create(fp)
		if err != nil {
			return nil, fmt.Errorf("failed to save sanctum data to %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(s)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to save sanctum to file %s: %s", fp, err)
		}
	} else {
		err = json.NewDecoder(f).Decode(s)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to load sanctum from %s: %s", fp, err)
		}
	}
	return s, nil
}
