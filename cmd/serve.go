package cmd

import (
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/natesales/pathvector/pkg/api"
	"github.com/natesales/pathvector/pkg/report"
)

var serveListen string

func init() {
	serveCmd.Flags().StringVar(&serveListen, "listen", "", "Listen address (host:port or unix:///path), overrides api-listen in config")
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the management API (session CRUD, status, reporting)",
	Run: func(cmd *cobra.Command, args []string) {
		c, err := loadConfig()
		if err != nil {
			log.Fatal(err)
		}

		listen := serveListen
		if listen == "" {
			listen = c.APIListen
		}
		if listen == "" {
			log.Fatal("api-listen is not set in the config and --listen was not given")
		}

		sessionsDir := c.APISessionsDir
		if sessionsDir == "" {
			sessionsDir = filepath.Join(filepath.Dir(configFile), "sessions.d")
		}

		// Warn if API-managed fragments won't be picked up by Load
		if len(c.Include) == 0 {
			log.Warn("No include patterns in config - sessions created via the API " +
				"will not be loaded. Add e.g. `include: [\"sessions.d/*.yml\"]` to pathvector.yml")
		}

		if c.ReportURL != "" {
			done := make(chan struct{})
			go report.Loop(configFile, done)
		}

		srv := api.New(configFile, sessionsDir, version, commit, date)
		if err := srv.ListenAndServe(listen, c.APIKey); err != nil {
			log.Fatal(err)
		}
	},
}
