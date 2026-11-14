// Package tui renders the workspace loop with opentui-go. Network work runs
// off the render goroutine and posts results through Renderer.Update.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/client"
	"github.com/tolaniverse/lanternfolk/internal/client/core"
	"github.com/tolaniverse/lanternfolk/protocol"
	tui "github.com/tolaniverse/opentui-go"
)

var (
	bg     = tui.MustHex("#1b1a24")
	panel  = tui.MustHex("#262331")
	accent = tui.MustHex("#f2c66d")
	muted  = tui.MustHex("#aaa2b5")
)

type App struct {
	r      *tui.Renderer
	s      *client.Session
	ctx    context.Context
	cancel context.CancelFunc
	screen *tui.Node
	status *tui.TextNode
	chat   *tui.TextNode
	roster *tui.TextNode
	conn   *tui.TextNode
	last   core.State
}

func Run(parent context.Context, s *client.Session) error {
	r, err := tui.New(tui.Options{Background: bg, Title: "Lanternfolk"})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	a := &App{r: r, s: s, ctx: ctx, cancel: cancel}
	defer cancel()
	a.signIn()
	unsub := s.Subscribe(func(st core.State) { r.Update(func() { a.render(st) }) })
	defer unsub()
	return r.Run(ctx, nil)
}

func (a *App) replace(root *tui.Node, focus tui.NodeHandle) {
	if a.screen != nil {
		a.r.Root().Remove(a.screen)
	}
	a.screen = root
	a.r.Root().Style = tui.Style{Padding: tui.All(1), Gap: 1}
	a.r.Root().Add(root)
	if focus != nil {
		a.r.Focus(focus)
	}
}

func (a *App) label(text string) *tui.TextNode {
	return tui.Text(text, tui.TextOptions{FG: muted, Wrap: tui.WrapWord})
}
func (a *App) field(name string, limit int, onSubmit func(string)) *tui.InputNode {
	return tui.Input(tui.InputOptions{Placeholder: name, MaxLength: limit, FG: tui.White, BG: panel, FocusedFG: bg, FocusedBG: accent, Style: tui.Style{Height: tui.Cells(1), Width: tui.Pct(100)}, OnSubmit: onSubmit})
}
func (a *App) box(title string, children ...tui.NodeHandle) *tui.Node {
	return tui.Box(tui.BoxOptions{Border: tui.BorderAll, BorderStyle: tui.BorderRounded, BorderColor: accent, Title: title, TitleColor: accent, Style: tui.Style{Padding: tui.All(1), Gap: 1, FlexGrow: 1}}, children...)
}
func (a *App) busy(message string) { a.status.SetText(message) }
func (a *App) work(fn func(context.Context) error, onSuccess func()) {
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
		defer cancel()
		err := fn(ctx)
		a.r.Update(func() {
			if err != nil {
				a.busy(err.Error())
			} else if onSuccess != nil {
				onSuccess()
			}
		})
	}()
}

func (a *App) signIn() {
	a.status = a.label("Enter a handle to sign in · local demo/server must enable dev auth")
	handle := a.field("handle (a-z, 0-9, _)", 24, func(v string) {
		a.busy("Signing in…")
		a.work(func(ctx context.Context) error { _, err := a.s.SignIn(ctx, strings.TrimSpace(v)); return err }, a.choose)
	})
	a.replace(a.box(" ✦ Lanternfolk · sign in ", a.label("A workspace for humans and agents"), handle, a.status), handle)
}

func (a *App) choose() {
	a.status = a.label("↑/↓ choose · Enter continue")
	choices := tui.Select(tui.SelectOptions{Options: []tui.SelectOption{{Name: "Create a workspace", Value: "create"}, {Name: "Join a workspace", Value: "join"}}, Style: tui.Style{Height: tui.Cells(4)}, FG: tui.White, BG: panel, OnSelect: func(_ int, o tui.SelectOption) {
		if o.Value == "create" {
			a.create()
		} else {
			a.joinSlug()
		}
	}})
	a.replace(a.box(" The crossroads ", a.label("Start a new guild or enter an existing one."), choices, a.status), choices)
}

func (a *App) create() {
	a.status = a.label("Tab moves between fields · Enter on the last field creates the guild")
	var slug, kind, prompt, answer *tui.InputNode
	name := a.field("workspace name", 64, func(string) { a.r.Focus(slug) })
	slug = a.field("slug (e.g. lantern-guild)", 32, func(string) { a.r.Focus(kind) })
	kind = a.field("gate kind: riddle / secret_phrase / invite_code (blank = open)", 32, func(string) { a.r.Focus(prompt) })
	prompt = a.field("gate prompt (optional)", 128, func(string) { a.r.Focus(answer) })
	answer = a.field("gate answer (optional; visible on screen)", 128, func(string) {
		req := protocol.CreateWorkspace{Name: name.Value(), Slug: slug.Value()}
		if strings.TrimSpace(kind.Value()) != "" {
			req.Gates = []protocol.GateSpec{{Kind: protocol.GateKind(strings.TrimSpace(kind.Value())), Prompt: prompt.Value(), Answer: answer.Value()}}
		}
		if err := req.Validate(); err != nil {
			a.busy(err.Error())
			return
		}
		a.busy("Creating…")
		a.work(func(ctx context.Context) error { _, err := a.s.Create(ctx, req); return err }, a.chatScreen)
	})
	a.replace(a.box(" Found a guild ", a.label("Name"), name, a.label("Slug"), slug, a.label("Optional gate"), kind, prompt, answer, a.status), name)
}

func (a *App) joinSlug() {
	a.status = a.label("Enter the workspace slug")
	slug := a.field("workspace slug", 32, func(v string) {
		a.busy("Asking the dungeon keeper…")
		a.work(func(ctx context.Context) error { _, err := a.s.Gates(ctx, v); return err }, func() { a.answerGates(v) })
	})
	a.replace(a.box(" Find the guild ", slug, a.status), slug)
}

func (a *App) answerGates(slug string) {
	gates := a.s.State().Gates.Prompts
	if len(gates) == 0 {
		a.join(slug, nil)
		return
	}
	a.status = a.label("Answer each gate in order · answers appear on screen")
	fields := make([]*tui.InputNode, len(gates))
	children := []tui.NodeHandle{a.label("The dungeon keeper asks:")}
	for i, g := range gates {
		i := i
		children = append(children, a.label(fmt.Sprintf("%d. %s", i+1, g.Prompt)))
		fields[i] = a.field("answer", 256, func(string) {
			if i+1 < len(fields) {
				a.r.Focus(fields[i+1])
				return
			}
			answers := make([]string, len(fields))
			for j, f := range fields {
				answers[j] = f.Value()
			}
			a.join(slug, answers)
		})
		children = append(children, fields[i])
	}
	children = append(children, a.status)
	a.replace(a.box(" The gate ", children...), fields[0])
}

func (a *App) join(slug string, answers []string) {
	a.busy("Entering…")
	a.work(func(ctx context.Context) error { _, err := a.s.Join(ctx, slug, answers); return err }, a.chatScreen)
}

func (a *App) chatScreen() {
	a.chat = tui.Text("Waiting for the world…", tui.TextOptions{FG: tui.White, Wrap: tui.WrapWord})
	a.roster = tui.Text("", tui.TextOptions{FG: accent, Wrap: tui.WrapWord})
	a.conn = tui.Text("connecting", tui.TextOptions{FG: muted})
	a.status = a.label("Enter sends · Tab changes focus · Ctrl+C quits")
	var input *tui.InputNode
	input = a.field("say something to #general", protocol.MaxMessageRunes, func(v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		input.SetValue("")
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		go func() {
			defer cancel()
			if err := a.s.Send(ctx, v); err != nil {
				a.r.Update(func() { a.status.SetText(err.Error()) })
			}
		}()
	})
	scroll := tui.ScrollBox(tui.ScrollBoxOptions{BoxOptions: tui.BoxOptions{Style: tui.Style{FlexGrow: 1, FlexBasis: tui.Cells(0), Padding: tui.All(1)}}, ScrollY: true, StickyEnd: true}, a.chat)
	left := a.box(" #general ", scroll)
	right := tui.Box(tui.BoxOptions{Border: tui.BorderAll, BorderColor: accent, Title: " online ", Style: tui.Style{Width: tui.Cells(23), Padding: tui.All(1)}}, a.roster)
	row := tui.NewNode(tui.Style{Direction: tui.Row, FlexGrow: 1, FlexBasis: tui.Cells(0), Gap: 1}, left, right)
	root := tui.NewNode(tui.Style{FlexGrow: 1, Gap: 1}, a.label("✦ Lanternfolk · "+a.s.State().Workspace.Name), row, a.conn, input, a.status)
	a.replace(root, input)
	a.render(a.s.State())
}

func (a *App) render(st core.State) {
	a.last = st
	if a.chat == nil {
		return
	}
	var lines []string
	for _, entry := range st.Timeline(st.DefaultChannel()) {
		suffix := ""
		if entry.Status == core.InFlight {
			suffix = " (sending…)"
		}
		if entry.Status == core.Rejected {
			suffix = " (failed: " + entry.Err + ")"
		}
		lines = append(lines, fmt.Sprintf("%s  %s: %s%s", entry.Message.CreatedAt.Format("15:04"), st.Handle(entry.Message.AuthorID), entry.Message.Body, suffix))
	}
	if len(lines) == 0 {
		lines = []string{"The room is quiet. Say hello."}
	}
	a.chat.SetText(strings.Join(lines, "\n"))
	var roster []string
	for _, m := range st.Roster() {
		icon := "○"
		if m.Online {
			icon = "●"
		}
		roster = append(roster, icon+" "+m.Handle)
	}
	a.roster.SetText(strings.Join(roster, "\n"))
	a.conn.SetText(fmt.Sprintf("%s · seq %d", st.Conn, st.Seq))
}
