// Package bundler defines account bundling schemes
package bundler

import (
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type Base interface {
	CloseSignal() <-chan error
	Close() error
}

type Bundler interface {
	Base
	// specify the code needed to tell the bot runtime which bundler to use
	Code() uint8
	// Tip returns an array of tipping accounts. Make sure to strategically pick the tip account with the least chance of writes
	// A tip is rust is specified with:
	//	let tip_ix = system_instruction::transfer(&signer.pubkey(), &TIP, MIN_TIP_AMOUNT);
	//	ixs.push(tip_ix);
	Tip() ([]sgo.PublicKey, error)
	// Distribution specified the 25th, 50th, 75th, 95th, 99th percentile required types to get a transaction into the block
	Distribution() ([5]graph.Lamports, error)
}

type BundlerCode = uint8

const (
	BundlerDefault   BundlerCode = 0
	BundlerAstralane BundlerCode = 1
	BundlerJito      BundlerCode = 2
)
