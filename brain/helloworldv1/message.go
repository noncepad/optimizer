package helloworldv1

import (
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest     uint8 = 1
	KeyFlagEchoResponse    uint8 = 2
	KeyFlagWallet          uint8 = 3
	KeyFlagTxLatency       uint8 = 4
	KeyFlagLatencyReportV1 uint8 = 5
)

func DoEchoRequest(payload string) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagEchoRequest}, []byte(payload))
	if err != nil {
		panic(err)
	}
	return x
}

func DoWallet(key sgo.PrivateKey) catmsg.FixedPair {
	if len(key) != 64 {
		panic(fmt.Errorf("bad key length: %d %d", len(key), 64))
	}
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagWallet}, key[:])
	if err != nil {
		panic(err)
	}
	return x
}

type TxLatency struct {
	N     uint64
	P50Us uint64
	P99Us uint64
}

func (l *TxLatency) Parse(value []byte) (int, error) {
	if len(value) < 24 {
		return 0, fmt.Errorf("tx latency value too short: %d < 24", len(value))
	}
	l.N = uint64(value[0]) | uint64(value[1])<<8 | uint64(value[2])<<16 | uint64(value[3])<<24 | uint64(value[4])<<32 | uint64(value[5])<<40 | uint64(value[6])<<48 | uint64(value[7])<<56
	l.P50Us = uint64(value[8]) | uint64(value[9])<<8 | uint64(value[10])<<16 | uint64(value[11])<<24 | uint64(value[12])<<32 | uint64(value[13])<<40 | uint64(value[14])<<48 | uint64(value[15])<<56
	l.P99Us = uint64(value[16]) | uint64(value[17])<<8 | uint64(value[18])<<16 | uint64(value[19])<<24 | uint64(value[20])<<32 | uint64(value[21])<<40 | uint64(value[22])<<48 | uint64(value[23])<<56
	return 24, nil
}

type LatencyReportV1 struct {
	ProcessedDiff     time.Duration `json:"processed_diff"`
	RootDiff          time.Duration `json:"commit_diff"`
	AccountProcessed  uint64        `json:"account_processed"`
	AccountRoot       uint64        `json:"account_root"`
	TxProcessedFilter uint64        `json:"tx_processed_filter"`
	TxProcessed       uint64        `json:"tx_processed"`
	TxN               uint64        `json:"tx_n"`
	TxP50Us           uint64        `json:"tx_p50_us"`
	TxP99Us           uint64        `json:"tx_p99_us"`
}

func (r *LatencyReportV1) String() string {
	perSec := func(count uint64, d time.Duration) float64 {
		if d <= 0 {
			return 0
		}
		return float64(count) / d.Seconds()
	}
	return fmt.Sprintf(
		"processed: acct=%.0f/s tx_filter=%.0f/s tx=%.0f/s | root: acct=%.0f/s | tx: n=%d p50=%dµs p99=%dµs",
		perSec(r.AccountProcessed, r.ProcessedDiff),
		perSec(r.TxProcessedFilter, r.ProcessedDiff),
		perSec(r.TxProcessed, r.ProcessedDiff),
		perSec(r.AccountRoot, r.RootDiff),
		r.TxN, r.TxP50Us, r.TxP99Us,
	)
}

func (r *LatencyReportV1) Parse(value []byte) (int, error) {
	if len(value) < 72 {
		return 0, fmt.Errorf("latency report v1 value too short: %d < 72", len(value))
	}
	u64 := func(b []byte) uint64 {
		return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
			uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
	}
	i := 0
	r.ProcessedDiff = time.Duration(u64(value[i:(i + 8)]))
	i += 8
	r.RootDiff = time.Duration(u64(value[i:(i + 8)]))
	i += 8
	r.AccountProcessed = u64(value[i:(i + 8)])
	i += 8
	r.AccountRoot = u64(value[i:(i + 8)])
	i += 8
	r.TxProcessedFilter = u64(value[i:(i + 8)])
	i += 8
	r.TxProcessed = u64(value[i:(i + 8)])
	i += 8
	r.TxN = u64(value[i:(i + 8)])
	i += 8
	r.TxP50Us = u64(value[i:(i + 8)])
	i += 8
	r.TxP99Us = u64(value[i:(i + 8)])
	return 72, nil
}
