package pyth

import (
	"testing"
	"time"
)

func testFeed(b byte) [32]byte {
	var f [32]byte
	f[0] = b
	return f
}

func TestHubDeliversToMultipleSubscribers(t *testing.T) {
	h := NewHub()
	ch1, unsub1 := h.Subscribe()
	defer unsub1()
	ch2, unsub2 := h.Subscribe()
	defer unsub2()

	want := PriceUpdate{Feed: testFeed(1), PriceUpdateV2: PriceUpdateV2{PriceUSD: 150.0}}
	h.Publish(want)

	for i, ch := range []<-chan PriceUpdate{ch1, ch2} {
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("subscriber %d: got %+v, want %+v", i, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for update", i)
		}
	}
}

func TestHubSubscribeReceivesLastKnownPriceImmediately(t *testing.T) {
	h := NewHub()
	first := PriceUpdate{Feed: testFeed(1), PriceUpdateV2: PriceUpdateV2{PriceUSD: 100.0}}
	h.Publish(first)

	ch, unsub := h.Subscribe()
	defer unsub()

	select {
	case got := <-ch:
		if got != first {
			t.Errorf("got %+v, want %+v", got, first)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the cached price on Subscribe")
	}
}

func TestHubUnsubscribeStopsDeliveryAndClosesChannel(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe()
	unsub()

	if _, ok := <-ch; ok {
		t.Fatal("expected the channel to be closed after unsubscribe")
	}

	// Publishing after unsubscribe must not panic (e.g. a send on a
	// closed/removed channel) -- confirms Publish only ever touches
	// still-registered subscribers.
	h.Publish(PriceUpdate{Feed: testFeed(1)})
}

func TestHubPublishDropsRatherThanBlocksOnASlowSubscriber(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe() // never read from
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer+10; i++ {
			h.Publish(PriceUpdate{Feed: testFeed(1), PriceUpdateV2: PriceUpdateV2{PriceUSD: float64(i)}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a slow/unread subscriber instead of dropping")
	}
	_ = ch
}

func TestHubLatestReflectsMostRecentPublish(t *testing.T) {
	h := NewHub()
	feed := testFeed(1)
	if _, ok := h.Latest(feed); ok {
		t.Fatal("expected no known price before any Publish")
	}
	h.Publish(PriceUpdate{Feed: feed, PriceUpdateV2: PriceUpdateV2{PriceUSD: 1}})
	h.Publish(PriceUpdate{Feed: feed, PriceUpdateV2: PriceUpdateV2{PriceUSD: 2}})
	got, ok := h.Latest(feed)
	if !ok || got.PriceUSD != 2 {
		t.Fatalf("Latest = %+v, %v; want PriceUSD=2, true", got, ok)
	}
}

func TestHubRunPublishesEverythingUntilClosed(t *testing.T) {
	h := NewHub()
	feed := testFeed(1)
	updateC := make(chan PriceUpdate, 2)
	updateC <- PriceUpdate{Feed: feed, PriceUpdateV2: PriceUpdateV2{PriceUSD: 1}}
	updateC <- PriceUpdate{Feed: feed, PriceUpdateV2: PriceUpdateV2{PriceUSD: 2}}
	close(updateC)

	done := make(chan struct{})
	go func() {
		h.Run(updateC)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after its input channel closed")
	}

	got, ok := h.Latest(feed)
	if !ok || got.PriceUSD != 2 {
		t.Fatalf("Latest after Run = %+v, %v; want PriceUSD=2, true", got, ok)
	}
}
