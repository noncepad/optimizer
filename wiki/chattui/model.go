// Package chattui is a terminal UI for chat.v1.ChatService, built on
// chatclient (the same gRPC client cmd/wiki-client's HTTP chat routes
// use - see wiki/remote/chat.go) - so this is a second, terminal-based
// front end for the same chat feature, not a reimplementation of it.
//
// Like wiki/remote's chatState, this is a single-user-per-process,
// entirely in-memory view: rooms/invitations/message history are lost
// when the program exits, same as the chat package's own in-memory
// design (see its package doc comment) and wiki/remote/chat.go's own
// doc comment on chatState.
package chattui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"git.noncepad.com/pkg/optimizer/grpcauth"
	"git.noncepad.com/pkg/optimizer/wiki/chat"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"google.golang.org/grpc/metadata"
)

const helpText = `commands:
  /create <title>   create a room and join it
  /join <room-id>   join a room (from an invite, or one you created)
  /switch <room-id> switch the active room among ones you've already joined
  /invite <user>    invite user to the active room
  /rooms            list rooms you've joined
  /invites          list pending invitations
  /help             show this help
  /quit             exit
anything else is sent as a message to the active room.`

// roomState is one joined room's live state - the TUI's counterpart of
// wiki/remote/chat.go's chatRoomState.
type roomState struct {
	id       uint64
	title    string
	room     chat.Room
	messages []chat.Message
	cancel   context.CancelFunc
}

// Model is a tea.Model for a chat session against one ChatService
// backend, as one authenticated login. Construct with New and run with
// Run (run.go) or your own tea.NewProgram.
type Model struct {
	ctx    context.Context
	client *chat.Client
	login  string

	invites   []chat.Invitation
	rooms     map[uint64]*roomState
	roomOrder []uint64
	active    uint64 // 0 = no active room

	inviteListener chat.InviteListener

	input    textinput.Model
	viewport viewport.Model
	status   string
	ready    bool
}

// New builds a Model for login, authenticated against client. ctx
// should be long-lived - canceling it tears down every background
// stream (the invite listener, every joined room) - see Run, which
// derives one scoped to the tea.Program's own lifetime.
func New(ctx context.Context, client *chat.Client, login string) Model {
	ti := textinput.New()
	ti.Placeholder = "message, or /help"
	ti.Prompt = "> "
	ti.CharLimit = 2000
	ti.Focus()

	m := Model{
		ctx:    ctx,
		client: client,
		login:  login,
		rooms:  make(map[uint64]*roomState),
		input:  ti,
		status: fmt.Sprintf("logged in as %s - /help for commands", login),
	}
	m.viewport = viewport.New(80, 20)
	if client != nil {
		m.inviteListener = client.ListenForInvites(m.authCtx())
	}
	return m
}

func withLogin(ctx context.Context, login string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, grpcauth.MetadataKey, login)
}

func (m Model) authCtx() context.Context {
	return withLogin(m.ctx, m.login)
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, m.windowTitleCmd()}
	if m.client != nil {
		cmds = append(cmds, waitForInvite(m.inviteListener))
	} else {
		m.status = "chat is not configured for this backend (no ChatService)"
	}
	return tea.Batch(cmds...)
}

// windowTitleCmd sets the real terminal window/tab title (via
// tea.SetWindowTitle's OSC escape sequence - a genuinely different
// thing from the in-content header line View() renders, though both
// show the same active room's title) to reflect the active room, or a
// generic title when none is active. Called whenever m.active changes.
func (m Model) windowTitleCmd() tea.Cmd {
	if rs, ok := m.rooms[m.active]; ok {
		return tea.SetWindowTitle(rs.title + " — wiki chat")
	}
	return tea.SetWindowTitle("wiki chat — " + m.login)
}

// --- async messages ---

type (
	inviteReceivedMsg       chat.Invitation
	inviteListenerErrMsg    struct{ err error }
	inviteListenerClosedMsg struct{}
)

type (
	roomCreatedMsg   chat.RoomInfo
	roomCreateErrMsg struct{ err error }
)

type roomJoinedMsg struct {
	id     uint64
	title  string
	room   chat.Room
	cancel context.CancelFunc
}
type roomJoinErrMsg struct {
	id  uint64
	err error
}

type roomMessageMsg struct {
	roomID uint64
	msg    chat.Message
}
type (
	roomClosedMsg struct{ roomID uint64 }
	roomErrMsg    struct {
		roomID uint64
		err    error
	}
)

type (
	inviteSentMsg     struct{ user string }
	inviteSendErrMsg  struct{ err error }
	sendMessageErrMsg struct{ err error }
)

// --- async commands ---

// waitForInvite blocks for exactly one invitation/error off l, then
// returns - the caller re-issues this after handling the result to keep
// listening, the standard bubbletea "bridge a channel into Cmds" idiom.
func waitForInvite(l chat.InviteListener) tea.Cmd {
	return func() tea.Msg {
		select {
		case inv, ok := <-l.InviteC:
			if !ok {
				return inviteListenerClosedMsg{}
			}
			return inviteReceivedMsg(inv)
		case err, ok := <-l.ErrorC:
			if !ok {
				return inviteListenerClosedMsg{}
			}
			return inviteListenerErrMsg{err: err}
		}
	}
}

func waitForRoomMessage(id uint64, room chat.Room) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-room.RecvC:
			if !ok {
				return roomClosedMsg{roomID: id}
			}
			return roomMessageMsg{roomID: id, msg: msg}
		case err, ok := <-room.ErrorC:
			if !ok {
				return roomClosedMsg{roomID: id}
			}
			return roomErrMsg{roomID: id, err: err}
		}
	}
}

func (m Model) createRoomCmd(title string) tea.Cmd {
	client := m.client
	ctx := m.authCtx()
	return func() tea.Msg {
		info, err := client.CreateRoom(ctx, title)
		if err != nil {
			return roomCreateErrMsg{err: err}
		}
		return roomCreatedMsg(info)
	}
}

// joinRoomCmd derives its own cancelable context from m.ctx (not tied
// to any one tea.Cmd invocation) so the resulting stream stays open for
// as long as the room stays joined, independent of anything else
// happening in the program - canceled explicitly only if this package
// ever grows a /leave command, and implicitly whenever ctx (the
// program's own root context - see Run) is canceled.
func (m Model) joinRoomCmd(id uint64, title string) tea.Cmd {
	client := m.client
	login := m.login
	parent := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(parent)
		room, err := client.JoinRoom(withLogin(ctx, login), id)
		if err != nil {
			cancel()
			return roomJoinErrMsg{id: id, err: err}
		}
		return roomJoinedMsg{id: id, title: title, room: room, cancel: cancel}
	}
}

func (m Model) inviteCmd(roomID uint64, user string) tea.Cmd {
	client := m.client
	ctx := m.authCtx()
	return func() tea.Msg {
		if err := client.Invite(ctx, roomID, user); err != nil {
			return inviteSendErrMsg{err: err}
		}
		return inviteSentMsg{user: user}
	}
}

// sendMessageCmd deliberately returns no success message - the server
// echoes a sent message back to its own sender (see chatclient's own
// doc comment/tests), so it arrives through the ordinary
// waitForRoomMessage flow like any other message, and appending it here
// too would show it twice.
func (m Model) sendMessageCmd(roomID uint64, text string) tea.Cmd {
	client := m.client
	ctx := m.authCtx()
	return func() tea.Msg {
		if err := client.SendMessage(ctx, roomID, text); err != nil {
			return sendMessageErrMsg{err: err}
		}
		return nil
	}
}

// --- update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		const headerLines, footerLines = 1, 3
		vh := msg.Height - headerLines - footerLines
		if vh < 3 {
			vh = 3
		}
		m.viewport.Width = msg.Width
		m.viewport.Height = vh
		m.input.Width = msg.Width - len(m.input.Prompt) - 1
		m.ready = true
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			return m.handleSubmit()
		case tea.KeyPgUp:
			m.viewport.LineUp(m.viewport.Height / 2)
			return m, nil
		case tea.KeyPgDown:
			m.viewport.LineDown(m.viewport.Height / 2)
			return m, nil
		}

	case inviteReceivedMsg:
		inv := chat.Invitation(msg)
		m.invites = append(m.invites, inv)
		m.status = fmt.Sprintf("invited to %q (#%d) by %s - /join %d to accept", inv.Title, inv.RoomID, inv.From, inv.RoomID)
		return m, waitForInvite(m.inviteListener)

	case inviteListenerErrMsg:
		m.status = fmt.Sprintf("invite listener error: %v", msg.err)
		return m, nil

	case inviteListenerClosedMsg:
		m.status = "invite listener closed"
		return m, nil

	case roomCreatedMsg:
		m.status = fmt.Sprintf("created %q (#%d), joining...", msg.Title, msg.ID)
		return m, m.joinRoomCmd(msg.ID, msg.Title)

	case roomCreateErrMsg:
		m.status = fmt.Sprintf("create room failed: %v", msg.err)
		return m, nil

	case roomJoinedMsg:
		title := msg.title
		if title == "" {
			// e.g. /join <id> for a room with no cached invite title
			// (joined by a raw ID nobody ever invited us with a title
			// for) - fall back to something other than a blank string
			// everywhere this title is displayed.
			title = fmt.Sprintf("room #%d", msg.id)
		}
		m.rooms[msg.id] = &roomState{id: msg.id, title: title, room: msg.room, cancel: msg.cancel}
		if !containsUint64(m.roomOrder, msg.id) {
			m.roomOrder = append(m.roomOrder, msg.id)
		}
		m.active = msg.id
		m.status = fmt.Sprintf("joined %q (#%d)", title, msg.id)
		m.refreshViewport()
		return m, tea.Batch(waitForRoomMessage(msg.id, msg.room), m.windowTitleCmd())

	case roomJoinErrMsg:
		m.status = fmt.Sprintf("join room #%d failed: %v", msg.id, msg.err)
		return m, nil

	case roomMessageMsg:
		if rs, ok := m.rooms[msg.roomID]; ok {
			rs.messages = append(rs.messages, msg.msg)
			if msg.roomID == m.active {
				m.refreshViewport()
			}
			return m, waitForRoomMessage(msg.roomID, rs.room)
		}
		return m, nil

	case roomClosedMsg:
		m.status = fmt.Sprintf("room #%d disconnected", msg.roomID)
		return m, nil

	case roomErrMsg:
		m.status = fmt.Sprintf("room #%d error: %v", msg.roomID, msg.err)
		return m, nil

	case inviteSentMsg:
		m.status = fmt.Sprintf("invited %s", msg.user)
		return m, nil

	case inviteSendErrMsg:
		m.status = fmt.Sprintf("invite failed: %v", msg.err)
		return m, nil

	case sendMessageErrMsg:
		m.status = fmt.Sprintf("send failed: %v", msg.err)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handleSubmit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	if m.active == 0 {
		m.status = "no active room - /create <title> or /join <room-id> first"
		return m, nil
	}
	return m, m.sendMessageCmd(m.active, text)
}

func (m Model) handleCommand(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	name := fields[0]
	args := fields[1:]

	switch name {
	case "/quit", "/exit":
		return m, tea.Quit

	case "/help":
		m.status = helpText
		return m, nil

	case "/create":
		if len(args) == 0 {
			m.status = "usage: /create <title>"
			return m, nil
		}
		return m, m.createRoomCmd(strings.Join(args, " "))

	case "/invite":
		if len(args) != 1 {
			m.status = "usage: /invite <user>"
			return m, nil
		}
		if m.active == 0 {
			m.status = "no active room - /create or /join first"
			return m, nil
		}
		return m, m.inviteCmd(m.active, args[0])

	case "/join", "/switch":
		if len(args) != 1 {
			m.status = fmt.Sprintf("usage: %s <room-id>", name)
			return m, nil
		}
		id, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil {
			m.status = fmt.Sprintf("invalid room id %q", args[0])
			return m, nil
		}
		if rs, ok := m.rooms[id]; ok {
			m.active = id
			m.status = fmt.Sprintf("switched to %q (#%d)", rs.title, id)
			m.refreshViewport()
			return m, m.windowTitleCmd()
		}
		return m, m.joinRoomCmd(id, m.titleForInvite(id))

	case "/rooms":
		m.status = m.roomsSummary()
		return m, nil

	case "/invites":
		m.status = m.invitesSummary()
		return m, nil

	default:
		m.status = fmt.Sprintf("unknown command %q - /help for a list", name)
		return m, nil
	}
}

func (m Model) titleForInvite(id uint64) string {
	for _, inv := range m.invites {
		if uint64(inv.RoomID) == id {
			return inv.Title
		}
	}
	return ""
}

func (m Model) roomsSummary() string {
	if len(m.roomOrder) == 0 {
		return "no rooms joined yet - /create <title> or /join <room-id>"
	}
	var b strings.Builder
	b.WriteString("joined rooms: ")
	for i, id := range m.roomOrder {
		if i > 0 {
			b.WriteString(", ")
		}
		marker := ""
		if id == m.active {
			marker = "*"
		}
		fmt.Fprintf(&b, "%s#%d %q", marker, id, m.rooms[id].title)
	}
	return b.String()
}

func (m Model) invitesSummary() string {
	if len(m.invites) == 0 {
		return "no pending invitations"
	}
	var b strings.Builder
	b.WriteString("invitations: ")
	for i, inv := range m.invites {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "#%d %q from %s", inv.RoomID, inv.Title, inv.From)
	}
	return b.String()
}

// refreshViewport rebuilds the active room's own message history into
// the viewport, word-wrapping each rendered line to the viewport's
// current width - real, live-confirmed gap this fixes: the viewport
// widget (bubbles/viewport) never wraps long lines on its own, so a
// message with no embedded newlines (a common shape for a chat bot's own
// prose reply) previously just ran off the right edge of the terminal
// instead of wrapping, regardless of how narrow the actual window was.
// lipgloss.NewStyle().Width(w).Render wraps at word boundaries and is
// ANSI/rune-width aware, and - critically for multi-paragraph markdown
// replies - preserves any newlines already in msg.Text, only wrapping
// individual lines that are themselves too wide, rather than collapsing
// the whole message onto one line.
func (m *Model) refreshViewport() {
	rs, ok := m.rooms[m.active]
	if !ok {
		m.viewport.SetContent("no active room - /create <title> or /join <room-id>")
		return
	}
	width := m.viewport.Width
	if width <= 0 {
		width = 80
	}
	wrap := lipgloss.NewStyle().Width(width)
	var b strings.Builder
	for _, msg := range rs.messages {
		line := fmt.Sprintf("[%s] %s: %s", msg.SentAt.Local().Format("15:04:05"), msg.From, msg.Text)
		b.WriteString(wrap.Render(line))
		b.WriteString("\n")
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

func containsUint64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- view ---

func (m Model) View() string {
	if !m.ready {
		return "initializing...\n"
	}
	header := fmt.Sprintf(" no active room — logged in as %s ", m.login)
	if rs, ok := m.rooms[m.active]; ok {
		header = fmt.Sprintf(" %s (#%d) — logged in as %s ", rs.title, rs.id, m.login)
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n",
		headerStyle.Render(header),
		m.viewport.View(),
		statusStyle.Render(m.status),
		helpStyle.Render("/help for commands · ctrl+c to quit"),
		m.input.View(),
	)
}
