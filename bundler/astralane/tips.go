package astralane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

const streamURL = "wss://tips.astralane.io/tip_stream"

// TipSnapshot represents the JSON payload received from the WebSocket endpoint.
type TipSnapshot struct {
	Time                     time.Time `json:"time"`
	LandedTips25thPercentile uint64    `json:"landed_tips_25th_percentile"`
	LandedTips50thPercentile uint64    `json:"landed_tips_50th_percentile"`
	LandedTips75thPercentile uint64    `json:"landed_tips_75th_percentile"`
	LandedTips95thPercentile uint64    `json:"landed_tips_95th_percentile"`
	LandedTips99thPercentile uint64    `json:"landed_tips_99th_percentile"`
}

// tipConnect streams the tip distribution
func TipConnect(ctx context.Context, entry *slog.Logger, dataC chan<- TipSnapshot, errorC chan<- error) {
	errorC <- connectAndListen(ctx, streamURL, entry, dataC)
}

func connectAndListen[T any](ctx context.Context, url string, entry *slog.Logger, dataC chan<- T) error {
	entry.Debug(fmt.Sprintf("Connecting to %s...", url))
	doneC := ctx.Done()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	entry.Info("connected successfully! Listening for tip stream data...")

	// Spawn a worker to gracefully close the WS connection on context cancellation
	go func() {
		<-ctx.Done()
		entry.Info("closing WebSocket connection gracefully...")
		_ = conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
	}()

out:
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		// The server sends each message as a JSON array of snapshots,
		// not a single object.
		var snapshots []T
		if err := json.Unmarshal(message, &snapshots); err != nil {
			entry.Warn(fmt.Sprintf("failed to unmarshal JSON: %v. raw message: %s", err, string(message)))
			continue
		}
		for _, snapshot := range snapshots {
			select {
			case <-doneC:
				break out
			case dataC <- snapshot:
			}
		}
	}
	return nil
}
