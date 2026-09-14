// Package jito wraps jito
package jito

import (
	"context"
	"errors"

	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type external struct {
	ctx context.Context
}

func Create(ctx context.Context) (bundler.Bundler, error) {
	return external{ctx: ctx}, nil
}

var errNotImplemented = errors.New("not implemented yet")

func (e1 external) CloseSignal() <-chan error {
	signalC := make(chan error, 1)
	signalC <- errNotImplemented
	return signalC
}

func (e1 external) Close() error {
	return errNotImplemented
}

func (e1 external) Tip() ([]sgo.PublicKey, error) {
	return nil, errNotImplemented
}

func (e1 external) Distribution() ([5]graph.Lamports, error) {
	var list [5]graph.Lamports
	return list, errNotImplemented
}

func (e1 external) Code() uint8 {
	return bundler.BundlerJito
}
