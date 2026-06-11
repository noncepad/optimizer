package helloworldv1

import (
	"sync"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
)

type pendingState struct {
	mx                  *sync.Mutex
	listLatencyReportV1 *dslist.Generic[LatencyReportV1]
}

func createPendingState() *pendingState {
	return &pendingState{
		mx:                  &sync.Mutex{},
		listLatencyReportV1: dslist.CreateGeneric[LatencyReportV1](),
	}
}
