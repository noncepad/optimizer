package orca

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"git.noncepad.com/pkg/bot/state"
	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	"git.noncepad.com/pkg/solpipe-util/graph"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

func fetchWhirlpoolConfig(ctx context.Context, stateClient state.Client) (*dslist.Generic[sgo.PublicKey], error) {
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
		return nil, fmt.Errorf("query failed: %s", err)
	}
	list := dslist.CreateGeneric[sgo.PublicKey]()
	// em.Lock()
	configs := em.EdgeDown(ProgramID)
	var checkDisc [8]uint8
	for k := range configs {
		a := em.UnsafeAccount(k)
		data := a.Data()
		if len(data) < 8 {
			continue
		}
		copy(checkDisc[:], data[0:8])
		if checkDisc != DiscriminatorWhirlpoolConfig {
			continue
		}
		list.Append(k)
	}
	// em.Unlock()
	return list, nil
}

func (orca *Orca) fetchWhirlpoolV4(ctx context.Context, stateClient state.Client, _ *slog.Logger) error {
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         ProgramID,
				FilterWeight: graph.WeightAll,
				Depth:        4,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("query failed: %s", err)
	}
	em.Lock()
	configs := em.EdgeDown(ProgramID)
	var checkDisc [8]uint8
	mPool := make(map[sgo.PublicKey]*Whirlpool)
	d := new(sgotkn.Account)
	for k := range configs {
		a := em.UnsafeAccount(k)
		data := a.Data()
		if len(data) < 8 {
			continue
		}
		copy(checkDisc[:], data[0:8])
		if checkDisc != DiscriminatorWhirlpoolConfig {
			continue
		}
		mDown := em.EdgeDown(k)
		for poolPubkey := range mDown {
			_, present := mPool[poolPubkey]
			if present {
				continue
			}
			a2 := em.UnsafeAccount(poolPubkey)
			if a2 == nil {
				continue
			}
			data := a2.Data()
			if len(data) < 8 {
				continue
			}
			copy(checkDisc[:], data[0:8])
			if checkDisc != DiscriminatorWhirlpool {
				continue
			}
			pool, err := parseWhirlpool(poolPubkey, data)
			if err != nil {
				continue
			}
			{
				vaultA := em.UnsafeAccount(pool.VaultA)
				if vaultA == nil {
					continue
				}
				err = bin.UnmarshalBorsh(d, vaultA.Data())
				if err != nil {
					em.Unlock()
					return fmt.Errorf("failed to parse vault %s: %s", pool.VaultA, err)
				}
				if d.Amount < 10 {
					continue
				}
			}
			mPool[poolPubkey] = pool
		}
	}
	em.Unlock()
	orca.Pools = make([]*Whirlpool, len(mPool))
	orca.mPool = make(map[sgo.PublicKey]int, len(mPool))
	{
		i := 0
		for k, v := range mPool {
			orca.Pools[i] = v
			if v == nil {
				panic("cannot have blank whirlpool")
			}
			orca.mPool[k] = i
			i++
		}
	}
	return nil
}

func (orca *Orca) fetchWhirlpoolV3(ctx context.Context, stateClient state.Client, logger *slog.Logger) error {
	_ = ctx.Done()
	listConfig, err := fetchWhirlpoolConfig(ctx, stateClient)
	if err != nil {
		return fmt.Errorf("failed to fetch whirlpool config: %s", err)
	}
	var pools []*Whirlpool
	var disc [8]uint8
	mPool := make(map[sgo.PublicKey]*Whirlpool)
	gotOrcaConfigCheck := false
	gotOrcaPoolCheck := false
	configArray := listConfig.Array()
	listChunked := chunkUint64s(configArray, 30)
	for chunkI, chunk := range listChunked {
		logger.Info(fmt.Sprintf("fetchWhirlpool config: %d/%d", chunkI, len(configArray)))
		listRequest := make([]state.QueryRequest, len(chunk))
		// map token account to pool account
		localMTokenVaultLookup := make(map[sgo.PublicKey]sgo.PublicKey, 25)
		for i, configPubkey := range chunk {
			if configPubkey.Equals(CheckConfigID) {
				gotOrcaConfigCheck = true
			}
			listRequest[i] = state.QueryRequest{
				Root:         configPubkey,
				FilterWeight: graph.WeightAll,
				Depth:        2,
			}
		}
		{
			// fetch whirlpools for each configuration file
			em, err := stateClient.QuerySingleShot(
				ctx, listRequest,
			)
			if err != nil {
				return fmt.Errorf("query failed: %s", err)
			}
			// depth-2 children of each config are Whirlpool pool accounts
			// em.Lock()
			for _, configPubkey := range chunk {
				poolCandidates := em.EdgeDown(configPubkey)
				for poolPubkey := range poolCandidates {
					if poolPubkey.Equals(CheckPoolID) {
						gotOrcaPoolCheck = true
					}
					a := em.UnsafeAccount(poolPubkey)
					if a == nil {
						continue
					}
					data := a.Data()
					if len(data) < 8 {
						continue
					}
					copy(disc[:], data[:8])
					if disc != DiscriminatorWhirlpool {
						continue
					}
					pool, err := parseWhirlpool(poolPubkey, data)
					if err != nil {
						continue
					}
					localMTokenVaultLookup[pool.VaultA] = poolPubkey
					localMTokenVaultLookup[pool.VaultB] = poolPubkey
					mPool[poolPubkey] = pool
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			// em.Unlock()
		}
		if true {
			// fetch token balances to figure out if we need to delete any whirlpools
			// fetch whirlpools for each configuration file
			listToken := make([]state.QueryRequest, len(localMTokenVaultLookup))
			{
				i := 0
				for k := range localMTokenVaultLookup {
					listToken[i] = state.QueryRequest{
						Root:         k,
						FilterWeight: 0,
						Depth:        1,
					}
					i++
				}
			}
			chunkToken := chunkUint64s(listToken, 128)
			for _, ct := range chunkToken {
				logger.Info(fmt.Sprintf("fetchWhirlpool config: %d/%d-----fetching tokens %d", chunkI, len(configArray), len(ct)))
				em, err := stateClient.QuerySingleShot(
					ctx, ct,
				)
				if err != nil {
					return fmt.Errorf("query failed: %s", err)
				}
				// em.Lock()
				var doDelete bool
				for _, req := range listToken {
					doDelete = false
					a := em.UnsafeAccount(req.Root)
					if a == nil {
						doDelete = true
					} else {
						d := new(sgotkn.Account)
						err = bin.UnmarshalBorsh(d, a.Data())
						if err != nil {
							em.Unlock()
							return fmt.Errorf("failed to parse token account %s: %s", req.Root, err)
						}
						if d.Amount == 0 {
							doDelete = true
						}
					}
					if doDelete {
						poolPublicKey, present := localMTokenVaultLookup[req.Root]
						if present {
							delete(mPool, poolPublicKey)
						}
					}
				}
				// em.Unlock()
			}

		}
	}
	if !gotOrcaConfigCheck {
		return fmt.Errorf("missing config %s", CheckConfigID)
	}
	if !gotOrcaPoolCheck {
		return fmt.Errorf("missing pool %s", CheckPoolID)
	}
	pools = make([]*Whirlpool, len(mPool))
	simpleMPool := make(map[sgo.PublicKey]int, len(mPool))
	i := 0
	for poolPubkey, pool := range mPool {
		pools[i] = pool
		simpleMPool[poolPubkey] = i
		i++
	}
	orca.mPool = simpleMPool
	orca.Pools = pools
	return nil
}

func chunkUint64s[T any](input []T, chunkSize int) [][]T {
	if chunkSize <= 0 {
		panic("chunkSize must be positive")
	}

	result := make([][]T, 0, (len(input)+chunkSize-1)/chunkSize)

	for i := 0; i < len(input); i += chunkSize {
		end := i + chunkSize
		if end > len(input) {
			end = len(input)
		}
		result = append(result, input[i:end])
	}

	return result
}
