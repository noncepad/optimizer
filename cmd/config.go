package main

import (
	"context"
	"sync"
)

type RunConfig struct {
	Ctx    context.Context
	Cancel context.CancelCauseFunc
	Wait   *sync.WaitGroup
}
