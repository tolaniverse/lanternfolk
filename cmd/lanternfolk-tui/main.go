// Command lanternfolk-tui is the keyboard-first Lanternfolk client.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
	"github.com/tolaniverse/lanternfolk/internal/client"
	"github.com/tolaniverse/lanternfolk/internal/client/transport"
	"github.com/tolaniverse/lanternfolk/internal/ui/tui"
)

func main() {
	server := flag.String("server", "http://localhost:8080", "lanternfolkd URL")
	demo := flag.Bool("demo", false, "run against an in-memory fake server")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.Get())
		return
	}
	var api transport.API = transport.NewHTTP(*server)
	if *demo {
		api = transport.NewFake()
	}
	s := client.New(api)
	defer s.Close()
	if err := tui.Run(context.Background(), s); err != nil {
		fmt.Fprintln(os.Stderr, "lanternfolk-tui:", err)
		os.Exit(1)
	}
}
