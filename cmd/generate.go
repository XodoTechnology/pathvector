package cmd

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/natesales/pathvector/pkg/process"
)

var (
	withdraw bool
	skipPDB  bool
	skipIRR  bool
	offline  bool
)

func init() {
	generateCmd.Flags().BoolVarP(&withdraw, "withdraw", "w", false, "Withdraw all routes")
	generateCmd.Flags().BoolVar(&skipPDB, "skip-peeringdb", false, "Skip PeeringDB queries (auto-import-limits, auto-as-set, NVRS)")
	generateCmd.Flags().BoolVar(&skipIRR, "skip-irr", false, "Skip bgpq4/IRR queries (filter-irr, auto-as-set-members)")
	generateCmd.Flags().BoolVar(&offline, "offline", false, "Don't query IRR or PeeringDB, only use data cached by previous runs")
	rootCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:     "generate",
	Short:   "Generate router configuration",
	Aliases: []string{"gen", "g"},
	Run: func(cmd *cobra.Command, args []string) {
		if err := process.Run(configFile, lockFile, version, process.RunOptions{
			NoConfigure: noConfigure,
			DryRun:      dryRun,
			Withdraw:    withdraw,
			SkipPDB:     skipPDB,
			SkipIRR:     skipIRR,
			Offline:     offline,
		}); err != nil {
			log.Fatal(err)
		}
	},
}
