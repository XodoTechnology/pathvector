package cmd

import (
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/natesales/pathvector/pkg/bird"
)

var commentMessage string

var allowedProtocolCommands = []string{"restart", "reload", "enable", "disable", "r", "l", "e", "d"}

func init() {
	protocolCmd.Flags().StringVarP(&commentMessage, "message", "m", "", "disable message")
	rootCmd.AddCommand(protocolCmd)
}

var protocolCmd = &cobra.Command{
	Use:     "protocol <(r)estart|re(l)oad|(e)nable|(d)isable> <protocol name>",
	Aliases: []string{"p", "protocols"},
	Short:   "Protocol command (restart, reload, enable or disable protocols)",
	Long:    "Restart, reload, enable or disable a protocol. Use \"all\" as the protocol name to run the command for all protocols.",
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("requires a command <restart|reload|enable|disable>")
		} else if !allowCommand(args[0]) {
			return fmt.Errorf("command %s is not allowed", args[0])
		} else if len(args) < 2 {
			return fmt.Errorf("requires protocol name")
		}
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		c, err := loadConfig()
		if err != nil {
			log.Warnf("Error loading config, falling back to default BIRD socket: %s", err)
		}

		birdSocket := "/run/bird/bird.ctl"
		if c != nil && c.BIRDSocket != "" {
			birdSocket = c.BIRDSocket
		}

		birdCommand := protocolCommand(args, commentMessage)
		log.Debugf("Running BIRD command: %s", birdCommand)

		var commandTimeout time.Duration
		if c != nil {
			commandTimeout = time.Duration(c.BIRDTimeout) * time.Second
		}
		commandOutput, _, err := bird.RunCommand(birdCommand, birdSocket, commandTimeout)
		if err != nil {
			log.Fatal(err)
		}
		if strings.Contains(commandOutput, "syntax error") || strings.Contains(commandOutput, "unexpected CF_SYM_UNDEFINED") {
			log.Fatal("The protocol name was not found")
		}
		log.Debugf("Command output: %s", commandOutput)

		fmt.Printf("Command %s succeeded for protocol %s\n", args[0], args[1])
	},
}

// allowCommand checks whether a protocol command is allowed
func allowCommand(command string) bool {
	for _, allowed := range allowedProtocolCommands {
		if allowed == command {
			return true
		}
	}
	return false
}

// protocolCommand builds the BIRD control command
func protocolCommand(args []string, message string) string {
	command := map[string]string{
		"r": "restart",
		"l": "reload",
		"e": "enable",
		"d": "disable",
	}[args[0]]
	if command == "" {
		command = args[0]
	}

	// BIRD only accepts a message argument on disable
	if command == "disable" {
		if message == "" {
			message = "Protocol manually disabled by pathvector"
		}
		return fmt.Sprintf("disable %s %q", args[1], message)
	}
	if message != "" {
		log.Warnf("-m/--message is only supported with disable; ignoring message for %s", command)
	}

	return command + " " + args[1]
}
