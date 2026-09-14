package util

import (
	"context"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/solpipe-util/ds/list"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type ackReceiveChan struct {
	ackC   <-chan struct{}
	cancel context.CancelFunc
	root   sgo.PublicKey
}

type pendingSubscription struct {
	ctx    context.Context
	root   sgo.PublicKey
	depth  graph.Depth
	weight graph.Weight
}
type PendingSubscriptionStatus struct {
	ctx    context.Context
	g      graph.Graph
	logger *slog.Logger
	// count is the total number of roots ever queued that haven't ack'd yet,
	// whether still sitting in listSub or already sent and sitting in
	// listAck. Used only for the Check()==0 "everything is done" signal.
	count int
	// inFlight is how many are currently sent and sitting in listAck,
	// awaiting ack. This — not count — gates how many more may be sent.
	inFlight int
	listSub  *list.Generic[pendingSubscription]
	listAck  *list.Generic[ackReceiveChan]
	max      int
	mDedup   map[sgo.PublicKey]struct{}
	// quietSlots is how many consecutive slot-commits must pass with the
	// queue empty before Check reports done. A depth-2 program-account
	// discovery delivers in bursts across separate slot-commits with quiet
	// gaps in between — without this, the instant the first small burst
	// drains, Check would report done long before the stream actually
	// finishes.
	quietSlots graph.Slot
	// doneSlot is the slot at which the queue first went empty; 0 means the
	// queue isn't currently (continuously) empty.
	doneSlot graph.Slot
}

// CreatePendingSubscriptionList creates a subscription tracker that reports
// done only after quietSlots consecutive slot-commits have passed with
// nothing pending — see PendingSubscriptionStatus.quietSlots.
func CreatePendingSubscriptionList(ctx context.Context, g graph.Graph, max int, quietSlots graph.Slot) *PendingSubscriptionStatus {
	listAck := list.CreateGeneric[ackReceiveChan]()
	listSub := list.CreateGeneric[pendingSubscription]()
	return &PendingSubscriptionStatus{listAck: listAck, listSub: listSub, g: g, max: max, ctx: ctx, mDedup: make(map[sgo.PublicKey]struct{}), quietSlots: quietSlots, logger: logger.FromContext(ctx)}
}

// Count returns [count, inFlight, queued] — queued is how many are still
// sitting in listSub, not yet sent. count should always equal
// queued+inFlight; a mismatch means the two are out of sync.
func (pss *PendingSubscriptionStatus) Count() [3]int {
	return [3]int{pss.count, pss.inFlight, int(pss.listSub.Size)}
}

// Check returns true once there have been no pending subscriptions for at
// least quietSlots consecutive slot-commits. slot is the current commit's
// slot (as reported by CommitStart).
func (pss *PendingSubscriptionStatus) Check(slot graph.Slot) bool {
	{
		// Inspect each currently-pending entry exactly once per Check()
		// call. Must dequeue from the opposite end Append() re-inserts
		// into: Shift() takes the head while Append() adds to the tail,
		// so a not-ready entry cycles to the back of a real FIFO and the
		// next iteration advances to a different entry. Using Pop()
		// (tail) here instead was a real bug -- Pop and Append both
		// operate on the tail, so re-appending a not-ready entry left it
		// sitting in the exact same spot it was just removed from, and
		// the next iteration popped that same entry right back out. With
		// even one slow-to-ack entry, it would perpetually re-occupy the
		// tail and every other pending entry behind it would never get
		// inspected, this call or any later one (n is recomputed from
		// the same unchanged listAck.Size next time). n is still
		// captured before the loop so a full lap always terminates
		// instead of spinning indefinitely if every entry happens to be
		// not-ready.
		n := pss.listAck.Size
		for range n {
			x, present := pss.listAck.Shift()
			if !present {
				break
			}

			select {
			case <-x.ackC:
				pss.count--
				pss.inFlight--
				// Deliberately NOT calling x.cancel() here anymore. Ack only
				// means "registered" -- cancelling this subscription's
				// context on ack sends an explicit Cancel to the server
				// (bot/state/graph.go's loopSubscribe watches this exact
				// context and forwards its cancellation as a
				// SubscriptionRequest_Cancel), which was cutting the server
				// off mid-walk for large/depth=0 subscriptions well before
				// it finished streaming matches (confirmed server-side: "grpc
				// client is prematurely dropping"). Leaving the context alive
				// lets the server keep streaming for this root until pss.ctx
				// itself ends (the overall fetch completing/being cancelled),
				// instead of unsubscribing the instant registration is
				// confirmed.
				pss.logger.Debug(fmt.Sprintf("pss ack: ack %d; sub %d; have ack from root %s", pss.listAck.Size, pss.listSub.Size, x.root))
			default:
				pss.listAck.Append(x)
			}
		}
	}
	for pss.inFlight < pss.max {
		x, present := pss.listSub.Pop()
		if !present {
			break
		}
		ctx, cancel := context.WithCancel(pss.ctx)
		ackC := pss.g.Subscribe(ctx, x.root, x.weight, x.depth)
		pss.listAck.Append(ackReceiveChan{ackC: ackC, cancel: cancel, root: x.root})
		pss.inFlight++
	}
	if pss.count != 0 || pss.inFlight != 0 || 0 < pss.listAck.Size || 0 < pss.listSub.Size {
		pss.doneSlot = 0
		return false
	}
	if pss.doneSlot == 0 {
		pss.doneSlot = slot
	}
	return pss.doneSlot+pss.quietSlots <= slot
}

// PendingRoots returns the roots currently sent and awaiting ack (a snapshot
// of listAck), for diagnosing subscriptions that never ack. Non-destructive.
func (pss *PendingSubscriptionStatus) PendingRoots() []sgo.PublicKey {
	roots := make([]sgo.PublicKey, 0, pss.inFlight)
	_ = pss.listAck.Iterate(func(x ackReceiveChan, _ uint32, _ func()) error {
		roots = append(roots, x.root)
		return nil
	})
	return roots
}

func (pss *PendingSubscriptionStatus) Subscribe(root sgo.PublicKey, weight graph.Weight, depth graph.Depth) {
	_, present := pss.mDedup[root]
	if present {
		return
	}
	pss.mDedup[root] = struct{}{}
	pss.count++
	pss.listSub.Append(pendingSubscription{root: root, weight: weight, depth: depth})
}
