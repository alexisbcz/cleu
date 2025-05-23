package main

import (
	"context"
	"os"

	"github.com/alexisbcz/cleu/cmd"
	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:           "cleu",
		Usage:          "Command-Line Emailing Utility",
		Commands:       []*cli.Command{cmd.Read, cmd.Send},
		DefaultCommand: "read",
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		panic(err)
	}
}
