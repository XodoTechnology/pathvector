package cmd

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/natesales/pathvector/pkg/process"
)

var (
	withdraw bool
)

func init() {
	generateCmd.Flags().BoolVarP(&withdraw, "withdraw", "w", false, "Withdraw all routes")
	rootCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:     "generate",
	Short:   "Generate router configuration",
	Aliases: []string{"gen", "g"},
	Run: func(cmd *cobra.Command, args []string) {
		if err := process.Run(configFile, lockFile, version, noConfigure, dryRun, withdraw); err != nil {
			log.Fatal(err)
		}
	},
}
