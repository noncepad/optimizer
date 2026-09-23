package helloworldv1

// pendingState is a thread-safe buffer shared between loopInstance (writer)
// and Evaluate (reader). loopInstance appends LatencyReportV1 records as they
// arrive from the bot's stdout; Evaluate drains the list each tick and writes
// the reports to the latency log file.

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
