package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

var Send = &cli.Command{
	Name:   "send",
	Action: func(ctx context.Context, c *cli.Command) error { return nil },
}
