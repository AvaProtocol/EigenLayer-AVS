package cmd

import (
	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest"
)

var (
	runAggregatorCmd = &cobra.Command{
		Use:   "aggregator",
		Short: "Run aggregator",
		Long: `Initialize and run aggregator.

Use --config=path-to-your-config-file. default is=./config/aggregator.yaml `,
		Run: func(cmd *cobra.Command, args []string) {
			// The REST API surface (api/openapi.yaml) is mounted here at
			// the cmd layer rather than inside the aggregator package so
			// the aggregator core never imports aggregator/rest. That
			// boundary is what lets the REST surface move to its own
			// (private) repo without dragging the aggregator core along.
			//
			// The mounter runs from inside startHttpServer once the
			// engine, rpcServer, smartWalletRpc, and priceService have
			// been populated — so calling agg.AvsClientSurface() at mount
			// time returns a fully-wired Surface.
			mountRest := func(agg *aggregator.Aggregator, e *echo.Echo) {
				srv := rest.NewServer(agg.Engine(), agg.Logger(), agg.Config(), agg.AvsClientSurface())
				srv.Mount(e)
			}

			aggregator.RunWithConfig(config, aggregator.WithHTTPMount(mountRest))
		},
	}
)

func init() {
	runAggregatorCmd.Flags().StringVar(&config, "config", "./config/aggregator.yaml", "path to aggregator config file")
	rootCmd.AddCommand(runAggregatorCmd)
}
