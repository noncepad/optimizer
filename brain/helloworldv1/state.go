package helloworldv1

import (
	"sync"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
)

type pendingState struct {
	mx            *sync.Mutex
	listTxLatency *dslist.Generic[Latency]
}

func createPendingState() *pendingState {
	return &pendingState{
		mx:            &sync.Mutex{},
		listTxLatency: dslist.CreateGeneric[Latency](),
	}
}
