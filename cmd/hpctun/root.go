package main

import (
	"strings"

	"github.com/spf13/cobra"
)

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "hpctun",
		Short: "Open, close and monitor ssh tunnels through a gateway",
		Args:  cobra.NoArgs,
		// fang.Execute sets these too; setting them here as well keeps
		// tests that call root.Execute() directly free of cobra's own
		// "Error: ..." and usage output, matching real hpctun runs.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// a.configPath is the flag's default (not "") so a value set before newRootCmd, as tests do, survives unless --config overrides it.
	root.PersistentFlags().StringVar(&a.configPath, "config", a.configPath, "config file (default $HPCTUN_CONFIG, else ~/.config/hpctun/config.toml; %APPDATA%\\hpctun\\config.toml on Windows)")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "print every command hpctun runs")
	root.PersistentFlags().BoolVar(&a.askpass, "askpass", false, "ssh asks for passwords in a window instead of the terminal (also HPCTUN_ASKPASS=1)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		// Completion and askpass (which ssh runs mid-login, its stderr
		// going to ssh's log) leave moving files to other commands.
		if !strings.HasPrefix(cmd.Name(), "__complete") && cmd.Name() != "askpass" && a.home() != "" {
			a.configFlagChanged = root.PersistentFlags().Changed("config")
			a.migrateSkipHint = cmd.Name() == "sync" && cmd.Parent() != nil && cmd.Parent().Name() == "ssh-config"
			a.migrate(true)
		}
		return nil
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error { return runStatus(cmd.Context(), a, false, false) }
	root.AddCommand(newStatusCmd(a), newUpCmd(a), newDownCmd(a), newBrowseCmd(a), newSaveCmd(a), newAddCmd(a), newInitCmd(a), newSSHConfigCmd(a), newCodeCmd(a), newVSCodeSettingsCmd(a), newSelfUpdateCmd(a), newAskpassCmd(a))
	root.AddCommand(newCopyCmds(a)...)
	root.AddCommand(newLoginCmds(a)...)
	return root
}
