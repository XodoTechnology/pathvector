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
		// Resolve relative paths against the config file directory, matching
		// how include: globs are resolved
		if !filepath.IsAbs(sessionsDir) {
			var err error
			if sessionsDir, err = filepath.Abs(filepath.Join(filepath.Dir(configFile), sessionsDir)); err != nil {
				log.Fatalf("resolving api-sessions-dir: %s", err)
			}
		}

		// Refuse to start if API-managed fragments won't be picked up by Load:
		// check that a probe file inside the sessions dir matches at least one
		// include glob (resolved against the config dir)
		probe := filepath.Join(sessionsDir, "probe.yml")
		matched := false
		for _, pattern := range c.Include {
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(filepath.Dir(configFile), pattern)
			}
			if ok, _ := filepath.Match(pattern, probe); ok {
				matched = true
				break
			}
		}
		if !matched {
			log.Fatalf("api-sessions-dir %s is not matched by any include: pattern - "+
				"API-written sessions would never be loaded. Add e.g. `include: [\"%s/*.yml\"]` to pathvector.yml",
				sessionsDir, sessionsDir)
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
