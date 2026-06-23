package kamino

import (
	"context"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// Kamino Lending reserve account offsets (Anchor, 8-byte discriminator prepended).
const (
	offLendingMarket  = 32  // Pubkey
	offMint           = 128 // Pubkey - liquidity mint
	offSupplyVault    = 160 // Pubkey
	offFeeVault       = 192 // Pubkey
	minReserveLen     = offFeeVault + 32 // 224
)

// Reserve is the parsed state of a Kamino Lending reserve account.
type Reserve struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	LendingMarket sgo.PublicKey `json:"lending_market"`
	Mint          sgo.PublicKey `json:"mint"`
	SupplyVault   sgo.PublicKey `json:"supply_vault"`
	FeeVault      sgo.PublicKey `json:"fee_vault"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseReserve parses a Kamino Lending reserve account body (8-byte discriminator stripped).
func parseReserve(pubkey sgo.PublicKey, body []byte) *Reserve {
	if len(body) < minReserveLen {
		return nil
	}
	return &Reserve{
		Pubkey:        pubkey,
		LendingMarket: readPubkey(body, offLendingMarket),
		Mint:          readPubkey(body, offMint),
		SupplyVault:   readPubkey(body, offSupplyVault),
		FeeVault:      readPubkey(body, offFeeVault),
	}
}

func (k *Kamino) fetch(ctx context.Context, stateClient state.Client, logger *slog.Logger) error {
	// Query depth=2: program → lending markets → reserve accounts
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         ProgramID,
				FilterWeight: graph.WeightAll,
				Depth:        2,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("query failed: %s", err)
	}

	em.Lock()
	programChildren := em.EdgeDown(ProgramID)
	var checkDisc [8]byte
	mReserve := make(map[sgo.PublicKey]*Reserve)

	for lmPubkey := range programChildren {
		a := em.UnsafeAccount(lmPubkey)
		if a == nil {
			continue
		}
		data := a.Data()
		if len(data) < 8 {
			continue
		}
		copy(checkDisc[:], data[0:8])
		if checkDisc != DiscLendingMarket {
			continue
		}
		// Walk depth-2 children (reserve accounts) of this lending market
		reserveCandidates := em.EdgeDown(lmPubkey)
		for reservePubkey := range reserveCandidates {
			_, present := mReserve[reservePubkey]
			if present {
				continue
			}
			a2 := em.UnsafeAccount(reservePubkey)
			if a2 == nil {
				continue
			}
			rdata := a2.Data()
			if len(rdata) < 8 {
				continue
			}
			copy(checkDisc[:], rdata[0:8])
			if checkDisc != DiscReserve {
				continue
			}
			// Body is data after the 8-byte discriminator
			reserve := parseReserve(reservePubkey, rdata[8:])
			if reserve == nil {
				continue
			}
			mReserve[reservePubkey] = reserve
		}
	}
	em.Unlock()

	logger.Info(fmt.Sprintf("kamino: found %d reserves", len(mReserve)))
	k.Reserves = make([]*Reserve, 0, len(mReserve))
	k.mReserve = make(map[sgo.PublicKey]int, len(mReserve))
	for pk, v := range mReserve {
		k.mReserve[pk] = len(k.Reserves)
		k.Reserves = append(k.Reserves, v)
	}
	return nil
}
