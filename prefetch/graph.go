package prefetch

import (
	"database/sql"
	"fmt"
	"math"

	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	sgo "github.com/gagliardetto/solana-go"
)

// Edge is one directed hop in a PriceGraph: crossing it converts 1 unit of
// the source mint into exp(-Weight) units of To (raw token units, no
// decimals normalization).
type Edge struct {
	To      sgo.PublicKey
	Weight  float64
	Pool    sgo.PublicKey
	Program string
}

// PriceGraph is a directed multigraph of mints connected by pool prices.
// Edge weight is -log(price), so summing weights around a cycle is negative
// exactly when compounding the trades around that cycle is profitable — a
// Bellman-Ford negative cycle in this graph is an arbitrage loop.
type PriceGraph struct {
	edges map[sgo.PublicKey][]Edge
}

// NewPriceGraph returns an empty graph.
func NewPriceGraph() *PriceGraph {
	return &PriceGraph{edges: make(map[sgo.PublicKey][]Edge)}
}

func (g *PriceGraph) addNode(n sgo.PublicKey) {
	if _, present := g.edges[n]; !present {
		g.edges[n] = nil
	}
}

// AddPool adds both directions of a two-sided pool: mintA -> mintB at
// priceAToB (units of mintB per unit of mintA, raw token units), plus the
// reciprocal edge back. Pools with a non-positive or non-finite price are
// silently skipped.
func (g *PriceGraph) AddPool(mintA, mintB, pool sgo.PublicKey, program string, priceAToB float64) {
	if priceAToB <= 0 || math.IsInf(priceAToB, 0) || math.IsNaN(priceAToB) {
		return
	}
	g.addNode(mintA)
	g.addNode(mintB)
	g.edges[mintA] = append(g.edges[mintA], Edge{To: mintB, Weight: -math.Log(priceAToB), Pool: pool, Program: program})
	g.edges[mintB] = append(g.edges[mintB], Edge{To: mintA, Weight: -math.Log(1 / priceAToB), Pool: pool, Program: program})
}

// NodeCount and EdgeCount report the graph's size.
func (g *PriceGraph) NodeCount() int { return len(g.edges) }
func (g *PriceGraph) EdgeCount() int {
	n := 0
	for _, es := range g.edges {
		n += len(es)
	}
	return n
}

// BellmanFord looks for a negative-weight cycle anywhere in the graph — not
// just ones reachable from one chosen node — by seeding every node's
// distance at 0 before the standard relaxation passes. That's equivalent to
// adding a virtual source with a zero-weight edge to every node and running
// Bellman-Ford from it, so a cycle is found regardless of which mints it
// touches. If found, ok is true and cycle lists the mints around the loop in
// trade order (cycle[0] == cycle[len(cycle)-1]).
func (g *PriceGraph) BellmanFord() (cycle []sgo.PublicKey, ok bool) {
	dist := make(map[sgo.PublicKey]float64, len(g.edges))
	prev := make(map[sgo.PublicKey]sgo.PublicKey, len(g.edges))
	for n := range g.edges {
		dist[n] = 0
	}
	numNodes := len(g.edges)
	for i := 0; i < numNodes-1; i++ {
		changed := false
		for from, edges := range g.edges {
			for _, e := range edges {
				if nd := dist[from] + e.Weight; nd < dist[e.To] {
					dist[e.To] = nd
					prev[e.To] = from
					changed = true
				}
			}
		}
		if !changed {
			// Fixed point reached before the theoretical worst case — since
			// Bellman-Ford distances are monotonically non-increasing, no
			// further pass could relax anything either, so there's no
			// negative cycle.
			return nil, false
		}
	}

	var onCycle sgo.PublicKey
	found := false
	for from, edges := range g.edges {
		for _, e := range edges {
			if dist[from]+e.Weight < dist[e.To] {
				prev[e.To] = from
				onCycle = e.To
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return nil, false
	}

	// Walking prev pointers numNodes times from a node relaxed on this extra
	// pass is guaranteed to land inside the negative cycle itself.
	x := onCycle
	for i := 0; i < numNodes; i++ {
		x = prev[x]
	}
	start := x
	path := []sgo.PublicKey{start}
	for x = prev[start]; x != start; x = prev[x] {
		path = append(path, x)
	}
	path = append(path, start)
	// path was built walking backwards along prev, so reverse it into trade
	// order (path[i] -> path[i+1] follows a real edge).
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, true
}

// BuildUnifiedGraph builds a PriceGraph from the top n liquidity pools of
// Raydium's AMM v4, CPMM, and CLMM programs, plus Orca's Whirlpools.
//
// Prices are raw-token-unit spot prices (no decimals normalization, no fee
// adjustment) — good enough to rank pools and hunt for gross arbitrage
// cycles, not a substitute for a real execution-price simulation.
func BuildUnifiedGraph(db *sql.DB, n int) (*PriceGraph, error) {
	g := NewPriceGraph()

	ammPools, err := amm.TopPools(db, n)
	if err != nil {
		return nil, fmt.Errorf("build graph: amm: %w", err)
	}
	for _, p := range ammPools {
		if p.CoinBalance == 0 || p.PcBalance == 0 {
			continue
		}
		price := float64(p.PcBalance) / float64(p.CoinBalance)
		g.AddPool(p.CoinMint, p.PcMint, p.Pubkey, "amm", price)
	}

	cpmmPools, err := cpmm.TopPools(db, n)
	if err != nil {
		return nil, fmt.Errorf("build graph: cpmm: %w", err)
	}
	for _, p := range cpmmPools {
		if p.Token0Balance == 0 || p.Token1Balance == 0 {
			continue
		}
		price := float64(p.Token1Balance) / float64(p.Token0Balance)
		g.AddPool(p.Token0Mint, p.Token1Mint, p.Pubkey, "cpmm", price)
	}

	clmmPools, err := clmm.TopPools(db, n)
	if err != nil {
		return nil, fmt.Errorf("build graph: clmm: %w", err)
	}
	for _, p := range clmmPools {
		if p.Token0Balance == 0 || p.Token1Balance == 0 {
			continue
		}
		price := float64(p.Token1Balance) / float64(p.Token0Balance)
		g.AddPool(p.TokenMint0, p.TokenMint1, p.Pubkey, "clmm", price)
	}

	orcaPools, err := orca.TopPools(db, n)
	if err != nil {
		return nil, fmt.Errorf("build graph: orca: %w", err)
	}
	for _, p := range orcaPools {
		g.AddPool(p.TokenMintA, p.TokenMintB, p.Pubkey, "orca", p.SpotPrice())
	}

	return g, nil
}
