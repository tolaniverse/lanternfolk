// Command lanternfolk-desktop is the Lanternfolk desktop client (Asuka:
// native widgets, no cgo on desktop). It follows the terminal client and
// waits on the world/input spike (Decisions 2026-11-14).
//
//	go run ./dev desktop -p mac            # one platform, into cmd/lanternfolk-desktop/dist
//	VERSION=v1.2.3 go run ./dev desktop-dist  # every platform, zipped, into dist/desktop
package main

import (
	"github.com/egoist/asuka/app"
	"github.com/egoist/asuka/fonts"
	"github.com/egoist/asuka/sdk/update"
	"github.com/egoist/asuka/ui"

	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
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
	return ui.SafeArea(ui.Style{Justify: ui.JustifyCenter, Align: ui.AlignCenter, Gap: 12},
		ui.StaticText("Lanternfolk", ui.Style{Font: fonts.LargeTitle}),
		ui.StaticText(version()),
	)
}

func main() {
	app.RunWindow(ui.WindowOptions{Title: ui.Val("Lanternfolk"), Width: 960, Height: 640}, App)
}
