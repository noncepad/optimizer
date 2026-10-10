package chattui

import (
	"context"

	chatclient "git.noncepad.com/pkg/optimizer/wiki/chat"
	tea "github.com/charmbracelet/bubbletea"
)

// Run drives a Model in a real terminal until the user quits (/quit,
// ctrl+c) or ctx is canceled. It derives its own cancelable context from
// ctx and cancels it on return, which is what actually tears down the
// invite listener and every joined room's stream (see New/joinRoomCmd) -
// callers don't need to do that themselves.
func Run(ctx context.Context, client *chatclient.Client, login string) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := New(runCtx, client, login)
	p := tea.NewProgram(m, tea.WithContext(runCtx), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
