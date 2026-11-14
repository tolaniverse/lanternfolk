// Command lanternfolk-app is the Lanternfolk GUI app, built with Asuka: native
// widgets on macOS, Windows, and Linux (no cgo), and the same code on iOS,
// Android, and the web. It follows the terminal client and waits on the
// world/input spike (Decisions 2026-11-14).
//
//	go run ./dev asuka dev                         # run on this computer, reloading on save
//	go run ./dev asuka build -p ios --simulator    # needs Xcode
//	VERSION=v1.2.3 go run ./dev desktop-dist       # desktop packages into dist/desktop
package main

import (
	"github.com/egoist/asuka/app"
	"github.com/egoist/asuka/fonts"
	"github.com/egoist/asuka/sdk/update"
	"github.com/egoist/asuka/ui"
	"os"

	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
	"github.com/tolaniverse/lanternfolk/internal/ui/worldspike"
)

// version prefers the version asuka built in (asuka build --version, from the
// release tag) over buildinfo, because asuka sets its own -ldflags.
func version() string {
	info := buildinfo.Get()
	if v := update.Version(); v != "" {
		info.Version = v
	}
	return info.String()
}

func App() ui.Node {
	if os.Getenv("LANTERNFOLK_WORLD_SPIKE") == "1" {
		return worldspike.World()
	}
	return ui.SafeArea(ui.Style{Justify: ui.JustifyCenter, Align: ui.AlignCenter, Gap: 12},
		ui.StaticText("Lanternfolk", ui.Style{Font: fonts.LargeTitle}),
		ui.StaticText(version()),
	)
}

func main() {
	app.RunWindow(ui.WindowOptions{Title: ui.Val("Lanternfolk"), Width: 960, Height: 640}, App)
}
