package api

import (
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// ChainState maintains pool, dex data relevant for compiling static information into trading bots.
type ChainState interface {
	Base
	// Database accesses the underlying database
	Database() *store.DB
	// Access an account
	Account(sgo.PublicKey) graph.Account
}
