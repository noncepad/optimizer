package helloworldv1

import (
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest  uint8 = 1
	KeyFlagEchoResponse uint8 = 2
	KeyFlagWallet       uint8 = 3
	KeyFlagTxLatency    uint8 = 4
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

type Latency struct {
	N     uint64
	P50Us uint64
	P99Us uint64
}

func (l *Latency) Parse(value []byte) (int, error) {
	if len(value) < 24 {
		return 0, fmt.Errorf("tx latency value too short: %d < 24", len(value))
	}
	l.N = uint64(value[0]) | uint64(value[1])<<8 | uint64(value[2])<<16 | uint64(value[3])<<24 | uint64(value[4])<<32 | uint64(value[5])<<40 | uint64(value[6])<<48 | uint64(value[7])<<56
	l.P50Us = uint64(value[8]) | uint64(value[9])<<8 | uint64(value[10])<<16 | uint64(value[11])<<24 | uint64(value[12])<<32 | uint64(value[13])<<40 | uint64(value[14])<<48 | uint64(value[15])<<56
	l.P99Us = uint64(value[16]) | uint64(value[17])<<8 | uint64(value[18])<<16 | uint64(value[19])<<24 | uint64(value[20])<<32 | uint64(value[21])<<40 | uint64(value[22])<<48 | uint64(value[23])<<56
	return 24, nil
}
