package cmd

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/natesales/pathvector/pkg/report"
)

var reportPost bool

func init() {
	reportCmd.Flags().BoolVar(&reportPost, "post", false, "POST the report to report-url (else print JSON)")
	rootCmd.AddCommand(reportCmd)
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Collect session state and optionally POST it to report-url",
	Run: func(cmd *cobra.Command, args []string) {
		c, err := loadConfig()
		if err != nil {
			log.Fatal(err)
		}
		router := c.ReportRouter
		if router == "" {
			router = c.Hostname
		}
		r, err := report.Collect(c, router, c.ReportPrefixCap)
		if err != nil {
			log.Fatal(err)
		}
		if !reportPost {
			j, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(string(j))
			return
		}
		if c.ReportURL == "" {
			log.Fatal("report-url is not configured")
		}
		if err := report.Post(r, c.ReportURL, c.ReportKey); err != nil {
			log.Fatal(err)
		}
		log.Infof("Reported %d sessions to %s", len(r.Sessions), c.ReportURL)
	},
}
