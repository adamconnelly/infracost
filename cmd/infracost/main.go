package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/infracost/infracost/internal/config"
	"github.com/infracost/infracost/internal/events"
	"github.com/infracost/infracost/internal/ui"
	"github.com/infracost/infracost/internal/update"
	"github.com/infracost/infracost/internal/version"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/fatih/color"
)

var spinner *ui.Spinner

func main() {
	var appErr error
	updateMessageChan := make(chan *update.Info)

	cfg := config.DefaultConfig()
	appErr = cfg.LoadFromEnv()

	defer func() {
		if appErr != nil {
			handleAppErr(cfg, appErr)
		}

		unexpectedErr := recover()
		if unexpectedErr != nil {
			handleUnexpectedErr(cfg, unexpectedErr)
		}

		handleUpdateMessage(updateMessageChan)

		if appErr != nil || unexpectedErr != nil {
			os.Exit(1)
		}
	}()

	startUpdateCheck(cfg, updateMessageChan)

	rootCmd := &cobra.Command{
		Use:     "infracost",
		Version: version.Version,
		Short:   "Cloud cost estimates for Terraform",
		Long: `Infracost - cloud cost estimates for Terraform

Generate a cost diff from terraform directory with any required terraform flags:

  infracost diff --path /path/to/code --terraform-plan-flags "-var-file=myvars.tfvars"

Generate a full cost breakdown from terraform directory with any required terraform flags:

  infracost breakdown --path /path/to/code --terraform-plan-flags "-var-file=myvars.tfvars"

Docs:
  https://infracost.io/docs`,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			return loadGlobalFlags(cfg, cmd)
		},
		PreRun: func(cmd *cobra.Command, args []string) {
			msg := ui.WarningString("┌────────────────────────────────────────────────────────────────────────┐\n")
			msg += fmt.Sprintf("%s %s %s %s\n",
				ui.WarningString("│"),
				ui.WarningString("Warning:"),
				"The root command is deprecated and will be removed in v0.9.0.",
				ui.WarningString("│"),
			)

			msg += fmt.Sprintf("%s %s %s                                         %s\n",
				ui.WarningString("│"),
				"Please use",
				ui.PrimaryString("infracost breakdown"),
				ui.WarningString("│"),
			)

			msg += fmt.Sprintf("%s %s %s             %s\n",
				ui.WarningString("│"),
				"Migration details:",
				ui.LinkString("https://www.infracost.io/v0.8-migration"),
				ui.WarningString("│"),
			)
			msg += ui.WarningString("└────────────────────────────────────────────────────────────────────────┘")

			if cfg.IsLogging() {
				for _, l := range strings.Split(ui.StripColor(msg), "\n") {
					log.Warn(l)
				}
			} else {
				fmt.Fprintln(os.Stderr, msg)
			}

			processDeprecatedEnvVars(cfg)
			processDeprecatedFlags(cmd)

			fmt.Fprintln(os.Stderr, "")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// The root command will be deprecated
			return breakdownCmd(cfg).RunE(cmd, args)
		},
	}

	// Add deprecated flags since the root command is deprecated
	addRootDeprecatedFlags(rootCmd)

	rootCmd.PersistentFlags().Bool("no-color", false, "Turn off colored output")
	rootCmd.PersistentFlags().String("log-level", "", "Log level (trace, debug, info, warn, error, fatal)")

	rootCmd.AddCommand(registerCmd(cfg))
	rootCmd.AddCommand(diffCmd(cfg))
	rootCmd.AddCommand(breakdownCmd(cfg))
	rootCmd.AddCommand(outputCmd(cfg))
	rootCmd.AddCommand(reportCmd(cfg))

	rootCmd.SetVersionTemplate("Infracost {{.Version}}\n")

	appErr = rootCmd.Execute()
}

func startUpdateCheck(cfg *config.Config, c chan *update.Info) {
	go func() {
		updateInfo, err := update.CheckForUpdate(cfg)
		if err != nil {
			log.Debugf("error checking for update: %v", err)
		}
		c <- updateInfo
		close(c)
	}()
}

func checkAPIKey(apiKey string, apiEndpoint string, defaultEndpoint string) error {
	if apiEndpoint == defaultEndpoint && apiKey == "" {
		return errors.New(fmt.Sprintf(
			"No INFRACOST_API_KEY environment variable is set.\nWe run a free Cloud Pricing API, to get an API key run %s",
			ui.PrimaryString("infracost register"),
		))
	}

	return nil
}

func handleAppErr(cfg *config.Config, err error) {
	if spinner != nil {
		spinner.Fail()
		fmt.Fprintln(os.Stderr, "")
	}

	if err.Error() != "" {
		ui.PrintError(err.Error())
	}

	msg := ui.StripColor(err.Error())
	var eventsError *events.Error
	if errors.As(err, &eventsError) {
		msg = ui.StripColor(eventsError.Label)
	}
	events.SendReport(cfg, "error", msg)
}

func handleUnexpectedErr(cfg *config.Config, unexpectedErr interface{}) {
	if spinner != nil {
		spinner.Fail()
		fmt.Fprintln(os.Stderr, "")
	}

	stack := string(debug.Stack())

	ui.PrintUnexpectedError(unexpectedErr, stack)

	events.SendReport(cfg, "error", fmt.Sprintf("%s\n%s", unexpectedErr, stack))
}

func handleUpdateMessage(updateMessageChan chan *update.Info) {
	updateInfo := <-updateMessageChan
	if updateInfo != nil {
		msg := fmt.Sprintf("\n%s %s %s → %s\n%s\n",
			ui.WarningString("Update:"),
			"A new version of Infracost is available:",
			ui.PrimaryString(version.Version),
			ui.PrimaryString(updateInfo.LatestVersion),
			ui.Indent(updateInfo.Cmd, "  "),
		)
		fmt.Fprint(os.Stderr, msg)
	}
}

func loadGlobalFlags(cfg *config.Config, cmd *cobra.Command) error {
	if cmd.Flags().Changed("no-color") {
		cfg.NoColor, _ = cmd.Flags().GetBool("no-color")
	}
	color.NoColor = cfg.NoColor

	if cmd.Flags().Changed("log-level") {
		cfg.LogLevel, _ = cmd.Flags().GetString("log-level")
		err := cfg.ConfigureLogger()
		if err != nil {
			return err
		}
	}

	if cmd.Flags().Changed("pricing-api-endpoint") {
		cfg.PricingAPIEndpoint, _ = cmd.Flags().GetString("pricing-api-endpoint")
	}

	cfg.Environment.IsDefaultPricingAPIEndpoint = cfg.PricingAPIEndpoint == cfg.DefaultPricingAPIEndpoint

	flagNames := make([]string, 0)

	cmd.Flags().Visit(func(f *pflag.Flag) {
		flagNames = append(flagNames, f.Name)
	})

	cfg.Environment.Flags = flagNames

	return nil
}
