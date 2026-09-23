package api

import (
	"context"
	"io"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

type BotCompiler interface {
	// Compile the wasm binary.
	Compile(ctx context.Context, sourceDir string) (io.ReadCloser, error)
}

// BotRuntimeManager manages endpoints to Catscope WASM runtimes via Bot Solpipe market place.
type BotRuntimeManager interface {
	// Connect to a remote Bot runtime
	Connect(ctx context.Context, pipelineID sgo.PublicKey) (BotRuntime, error)
}

type BotRuntime interface {
	Status() StatusType
	// set the budget in raw units of the Solpipe market mint over some slot count
	Budget(amount uint64, slot graph.Slot)
	BotList() []Bot
}

type Bot interface {
	Hash() sgo.Hash
	Stderr() io.ReadCloser
	Send() chan<- catmsg.FixedPair
	Recv() <-chan catmsg.FixedPair
}

type StatusType = uint8

const (
	StatusTypeOffline   StatusType = 0
	StatusTypeNonbidder StatusType = 1
	StatusTypeBidder    StatusType = 1
)
