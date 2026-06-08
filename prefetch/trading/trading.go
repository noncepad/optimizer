// Package trading holds common structs
package trading

import (
	"sort"

	sgo "github.com/gagliardetto/solana-go"
)

type Configuration struct {
	Pair [][]sgo.PublicKey `json:"pair"`
}

type TradingPair struct {
	mToken map[sgo.PublicKey]sgo.PublicKey
}

func Create() *TradingPair {
	tp := new(TradingPair)
	tp.mToken = make(map[sgo.PublicKey]sgo.PublicKey)
	return tp
}

func (tp *TradingPair) Add(mintA, mintB sgo.PublicKey) {
	list := sortPubkey([]sgo.PublicKey{mintA, mintB})
	a, b := list[0], list[1]
	tp.mToken[a] = b
}

func sortPubkey(list []sgo.PublicKey) []sgo.PublicKey {
	sort.Slice(list, func(i, j int) bool {
		for k := range sgo.PublicKeyLength {
			if list[i][k] != list[j][k] {
				return list[i][k] < list[j][k]
			}
		}
		return false
	})
	return list
}

func (tp *TradingPair) Export(outPath string) error {
	c := new(Configuration)
	c.Pair = make([][]sgo.PublicKey, len(tp.mToken))
	i := 0
	for k, v := range tp.mToken {
		c.Pair[i] = make([]sgo.PublicKey, 2)
		c.Pair[i][0] = k
		c.Pair[i][1] = v
		i++
	}
	return nil
}
