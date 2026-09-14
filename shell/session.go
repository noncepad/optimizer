package shell

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
)

func (in *internal) onConn(ci *clientInfo) {
	ci.id = in.clientIndex
	in.mClient[ci.id] = ci
	go handleClient(in.ctx, ci.id, ci.ctx, in.sessionC, ci.conn, ci.chat)
}

func (in *internal) onSession(session sessionUpdate) {
	ci, present := in.mClient[session.id]
	if !present {
		return
	}
	switch session.signal {
	case sessionSignalError:
		ci.close(session.err)
	default:
	}
}

// close the listener in loopIntenral
func loopListen(
	ctx context.Context,
	listener net.Listener,
	errorC chan<- error,
	connC chan<- *clientInfo,
	entry *slog.Logger,
	chatBuilder ChatBuilder,
) {
	errorC <- insideLoopListen(ctx, connC, listener, entry, chatBuilder)
}

type clientInfo struct {
	id     int
	ctx    context.Context
	cancel context.CancelCauseFunc
	chat   ChatPrompter
	conn   net.Conn
	logger *slog.Logger
}

func (ci *clientInfo) close(err error) {
	ci.cancel(err)
	if err != nil {
		_, _ = fmt.Fprintf(ci.conn, "chat build failed: %s", err)
	}
	ci.logger.With("err", err).Info("client exiting")
	_ = ci.conn.Close()
}

func insideLoopListen(
	ctx context.Context,
	connC chan<- *clientInfo,
	listener net.Listener,
	entry *slog.Logger,
	chatBuilder ChatBuilder,
) error {
	entry.Info(fmt.Sprintf("Telnet custom shell server listening on port %s...\n", listener.Addr().String()))
	doneC := ctx.Done()
	for {
		// Accept incoming client connections
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accepting connection failed: %s", err)
		}
		ci := new(clientInfo)
		ci.conn = conn
		ci.ctx, ci.cancel = context.WithCancelCause(ctx)
		ci.logger = entry.With("f", "clientInfo")
		ci.chat, err = chatBuilder.Create(ci.ctx)
		if err != nil {
			ci.close(err)
			continue
		}
		if ci.chat == nil {
			panic("have nil chat")
		}
		// Handle each connection concurrently in a goroutine
		select {
		case <-doneC:
			return ctx.Err()
		case connC <- ci:
		}
	}
}

type (
	sessionUpdate struct {
		id     int
		signal sessionSignal
		err    error
	}
	sessionSignal = int
)

const (
	// loopInternal telling session to exit
	sessionSignalClose sessionSignal = -1
	// session has ended
	sessionSignalError sessionSignal = -2
)

func handleClient(parentCtx context.Context, id int, ctx context.Context, sessionC chan<- sessionUpdate, conn net.Conn, chatter ChatPrompter) {
	doneC := parentCtx.Done()
	err := insidehandleClient(ctx, sessionC, conn, chatter)
	select {
	case <-doneC:
	case sessionC <- sessionUpdate{err: err, signal: sessionSignalError, id: id}:
	}
}

func insidehandleClient(ctx context.Context, sessionC chan<- sessionUpdate, conn net.Conn, chatter ChatPrompter) error {
	// Welcome message
	var err error
	_, err = conn.Write([]byte("Welcome to Custom Go Telnet Shell!\r\n"))
	if err != nil {
		return fmt.Errorf("welcome failed: %s", err)
	}
	_, err = conn.Write([]byte("Type 'help' for available commands, or 'exit' to quit.\r\n\r\n"))
	if err != nil {
		return fmt.Errorf("welcome failed: %s", err)
	}

	reader := bufio.NewReader(conn)
	listWriteQueue := make([]string, 100)
	listWriteI := 0
	isChatMode := false
out:
	for ctx.Err() == nil {
		listWriteI = 0
		// Send prompt
		_, err = conn.Write([]byte("myshell> "))
		if err != nil {
			err = fmt.Errorf("failed to cycle to new prompt: %s", err)
			break out
		}
		// Read input until newline
		var input string
		input, err = reader.ReadString('\n')
		if err != nil {
			// Client disconnected or network error
			err = fmt.Errorf("client disconnected: %s", err)
			break out
		}

		// Clean up input carriage returns and newlines
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if !isChatMode {
			// Command evaluation logic
			args := strings.Fields(input)
			command := args[0]

			switch command {
			case "ping":
				listWriteQueue[listWriteI] = "pong"
				listWriteI++
			case "echo":
				if len(args) > 1 {
					message := strings.Join(args[1:], " ")
					listWriteQueue[listWriteI] = message
					listWriteI++
				} else {
					listWriteQueue[listWriteI] = "Usage: echo <text>"
					listWriteI++
				}
			case "chat":
				listWriteQueue[listWriteI] = "Switching to chat (LLM) mode"
				listWriteI++
				isChatMode = true
			case "info":
				listWriteQueue[listWriteI] = "System: Custom Go Telnet Daemon v1.0"
				listWriteI++
			case "help":
				listWriteQueue[listWriteI] = "Available commands:"
				listWriteI++
				listWriteQueue[listWriteI] = "  ping  - Responds with pong"
				listWriteI++
				listWriteQueue[listWriteI] = "  echo  - Repeats user message"
				listWriteI++
				listWriteQueue[listWriteI] = "  info  - Displays shell information"
				listWriteI++
				listWriteQueue[listWriteI] = "  exit  - Closes the connection"
				listWriteI++
			case "exit":
				listWriteQueue[listWriteI] = "Goodbye!"
				// listWriteI++
				break out
			default:
				response := fmt.Sprintf("Unknown command: %s", command)
				listWriteQueue[listWriteI] = response
				listWriteI++
			}
		} else if input == "exit" {
			listWriteQueue[listWriteI] = "Goodbye!"
			// listWriteI++
			break out
		} else {
			listWriteQueue[listWriteI], err = chatter.Prompt(input)
			listWriteI++
			if err != nil {
				err = fmt.Errorf("prompt failed: %s", err)
				break out
			}
		}
		var message string
		for i := range listWriteI {
			message = listWriteQueue[i]
			_, err = conn.Write([]byte(message + "\r\n"))
			if err != nil {
				err = fmt.Errorf("failed to write: %s", err)
				break out
			}
		}
	}
	_ = sessionC
	return err
}
