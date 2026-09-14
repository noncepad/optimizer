package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

type RunConfig struct {
	Ctx      context.Context
	Cancel   context.CancelCauseFunc
	Wait     *sync.WaitGroup
	StateURL string
}

func getDBFilePath() string {
	return filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db")
}
