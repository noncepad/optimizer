package astralane_test

import (
	"context"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/bundler/astralane"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

func TestTip(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	entry := logger.FromContext(ctx)
	dataC := make(chan astralane.TipSnapshot, 100)
	errorC := make(chan error, 1)
	go astralane.TipConnect(ctx, entry, dataC, errorC)
	var err error
	var data astralane.TipSnapshot
done:
	for range 100 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Minute):
		case err = <-errorC:
		case data = <-dataC:
			if data.LandedTips25thPercentile == 0 {
				continue
			}
			break done
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if data.LandedTips25thPercentile == 0 {
		t.Fatal("have blank data")
	}
	t.Fatalf("got data %+v", data)
}
