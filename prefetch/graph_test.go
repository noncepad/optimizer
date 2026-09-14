package prefetch

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

func testMint(b byte) sgo.PublicKey {
	var buf [32]byte
	for i := range buf {
		buf[i] = b
	}
	return sgo.PublicKeyFromBytes(buf[:])
}

// TestBellmanFordDetectsArbitrage builds a deliberate 3-mint arbitrage
// triangle (1 X -> 2 Y -> 4 Z -> 1.2 X, a 20% round-trip profit) and checks
// BellmanFord finds it as a negative cycle.
func TestBellmanFordDetectsArbitrage(t *testing.T) {
	x, y, z := testMint(1), testMint(2), testMint(3)
	pool1, pool2, pool3 := testMint(10), testMint(11), testMint(12)

	g := NewPriceGraph()
	g.AddPool(x, y, pool1, "test", 2.0) // 1 X -> 2 Y
	g.AddPool(y, z, pool2, "test", 2.0) // 1 Y -> 2 Z
	g.AddPool(z, x, pool3, "test", 0.3) // 1 Z -> 0.3 X (round trip: 1 -> 2 -> 4 -> 1.2)

	cycle, ok := g.BellmanFord()
	if !ok {
		t.Fatal("expected a negative cycle (arbitrage loop) to be found")
	}
	if len(cycle) < 2 || !cycle[0].Equals(cycle[len(cycle)-1]) {
		t.Fatalf("cycle should start and end at the same mint, got %v", cycle)
	}
	t.Logf("found cycle: %v", cycle)
}

// TestBellmanFordNoArbitrageWithConsistentPrices checks that a single pool
// (an edge and its exact reciprocal) never looks like a negative cycle —
// -log(p) + -log(1/p) == 0 exactly, not negative.
func TestBellmanFordNoArbitrageWithConsistentPrices(t *testing.T) {
	x, y := testMint(1), testMint(2)
	pool := testMint(10)

	g := NewPriceGraph()
	g.AddPool(x, y, pool, "test", 1.5)

	if _, ok := g.BellmanFord(); ok {
		t.Fatal("expected no arbitrage cycle from a single consistent-price pool")
	}
}

// TestBuildUnifiedGraph exercises BuildUnifiedGraph against a real,
// already-downloaded prefetch.db if one exists, combining Orca and Raydium
// (AMM v4 + CPMM + CLMM) top pools into one graph and running BellmanFord
// over it.
func TestBuildUnifiedGraph(t *testing.T) {
	dbPath := filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("no prefetch.db at %s; run the download command first", dbPath)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	g, err := BuildUnifiedGraph(db, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("unified graph (orca + raydium amm/cpmm/clmm): %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
	if g.NodeCount() == 0 {
		t.Fatal("expected at least one node in the unified graph")
	}

	cycle, ok := g.BellmanFord()
	if !ok {
		t.Log("no negative cycle found (no arbitrage loop among today's top pools)")
		return
	}
	t.Logf("found a negative cycle (potential arbitrage loop) with %d hops:", len(cycle)-1)
	for _, mint := range cycle {
		t.Logf("  -> %s", mint)
	}
}
