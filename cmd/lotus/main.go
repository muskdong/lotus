package main

import (
	"context"
	"log"
	"os"

	logging "github.com/ipfs/go-log"
	"go.opencensus.io/trace"
	"gopkg.in/urfave/cli.v2"

	"github.com/filecoin-project/go-lotus/build"
	lcli "github.com/filecoin-project/go-lotus/cli"
	"github.com/filecoin-project/go-lotus/tracing"
)

func main() {
	logging.SetLogLevel("*", "INFO")
	local := []*cli.Command{
		DaemonCmd,
	}
	jaeger := tracing.SetupJaegerTracing("lotus")
	defer func() {
		if jaeger != nil {
			jaeger.Flush()
		}
	}()

	for _, cmd := range local {
		cmd := cmd
		originBefore := cmd.Before
		cmd.Before = func(cctx *cli.Context) error {
			trace.UnregisterExporter(jaeger)
			jaeger = tracing.SetupJaegerTracing("lotus/" + cmd.Name)

			if originBefore != nil {
				return originBefore(cctx)
			}
			return nil
		}
	}
	ctx, span := trace.StartSpan(context.Background(), "/cli")
	defer span.End()

	app := &cli.App{
		Name:    "lotus",
		Usage:   "Filecoin decentralized storage network client",
		Version: build.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "repo",
				EnvVars: []string{"LOTUS_PATH"},
				Hidden:  true,
				Value:   "~/.lotus", // TODO: Consider XDG_DATA_HOME
			},
		},

		Commands: append(local, lcli.Commands...),
	}
	app.Setup()
	app.Metadata["traceContext"] = ctx

	if err := app.Run(os.Args); err != nil {
		span.SetStatus(trace.Status{
			Code:    trace.StatusCodeFailedPrecondition,
			Message: err.Error(),
		})
		log.Printf("%+v\n", err)
	}
	return
}