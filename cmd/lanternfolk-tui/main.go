// Command lanternfolk-tui is the Lanternfolk terminal client (opentui-go).
// It leads every vertical slice. For now it shows a placeholder screen;
// slice 1 wires it to internal/client through the Session facade (LLD §4.6).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tui "github.com/tolaniverse/opentui-go"

	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
)

func main() {
	server := flag.String("server", "http://localhost:8080", "lanternfolkd URL")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.Get())
		return
	}
	if err := run(*server); err != nil {
		fmt.Fprintln(os.Stderr, "lanternfolk-tui:", err)
		os.Exit(1)
	}
}

func run(server string) error {
	r, err := tui.New(tui.Options{Background: tui.MustHex("#1b1a24"), Title: "Lanternfolk"})
	if err != nil {
		return err
	}

	body := fmt.Sprintf("Lanternfolk %s\n\nserver: %s\n\nNothing to do yet. Press Ctrl+C to quit.",
		buildinfo.Get().Version, server)
	card := tui.Box(tui.BoxOptions{
		Border: tui.BorderAll, BorderStyle: tui.BorderRounded, BorderColor: tui.MustHex("#f2c66d"),
		Title: " lanternfolk ", TitleAlign: tui.TitleCenter,
		Style: tui.Style{Padding: tui.XY(2, 1)},
	}, tui.Text(body, tui.TextOptions{}))

	root := r.Root()
	root.Style = tui.Style{Justify: tui.JustifyCenter, AlignItems: tui.AlignCenter}
	root.Add(card)
	return r.Run(context.Background(), nil)
}
