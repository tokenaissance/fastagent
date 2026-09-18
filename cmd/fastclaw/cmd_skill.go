package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// skillCmd handles skill management subcommands.
func skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage skills",
	}
	cmd.AddCommand(skillListCmd())
	cmd.AddCommand(skillSearchCmd())
	cmd.AddCommand(skillInstallCmd())
	cmd.AddCommand(skillUpdateCmd())
	cmd.AddCommand(skillRemoveCmd())
	cmd.AddCommand(skillInfoCmd())
	cmd.AddCommand(skillReconcileCmd())
	return cmd
}

// skillReconcileCmd aligns skill directories with the name their SKILL.md
// declares. A skill has two names today — the directory it was installed under
// (a slug, a repo, a zip folder) and the frontmatter `name` its author wrote —
// and only the second one is publishable over MCP, because a served skill's URI
// must end in it. New installs are aligned as they land; this command is for
// everything that was installed before that rule existed.
//
// It defaults to a dry run. The renamed directories are only one of the three
// places the old name lives: the report also prints the object-store key prefix
// to move and the config paths that name the skill, so an operator can finish
// the job knowing exactly what else refers to it.
func skillReconcileCmd() *cobra.Command {
	var apply, global, asJSON bool
	var agentID, userID string
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Align skill directories with the name their SKILL.md declares",
		Long: "Align skill directories with the name their SKILL.md declares.\n\n" +
			"Dry run by default: nothing moves until --apply. Skills whose frontmatter name\n" +
			"is missing, invalid, or already taken are reported, never renamed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var layers []skills.LayerSpec
			var err error
			switch {
			case agentID != "":
				var one skills.LayerSpec
				if one, err = skills.AgentLayer(agentID); err == nil {
					layers = []skills.LayerSpec{one}
				}
			case userID != "":
				var one skills.LayerSpec
				if one, err = skills.UserLayer(userID); err == nil {
					layers = []skills.LayerSpec{one}
				}
			case global:
				var one skills.LayerSpec
				if one, err = skills.GlobalLayer(); err == nil {
					layers = []skills.LayerSpec{one}
				}
			default:
				layers, err = skills.FilesystemLayers()
			}
			if err != nil {
				return err
			}

			report, err := skills.ReconcileLayout(layers, !apply)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			mode := "DRY RUN"
			if apply {
				mode = "APPLIED"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s — %d skill director(ies) scanned\n\n", mode, len(report.Items))
			for _, item := range report.Items {
				line := fmt.Sprintf("  %-8s %-9s %-28s", item.Action, item.Layer, item.DirName)
				if item.DeclaredName != "" && item.DeclaredName != item.DirName {
					line += fmt.Sprintf(" -> %s", item.DeclaredName)
				}
				if item.Reason != "" {
					line += "   (" + item.Reason + ")"
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
				if item.Action != skills.ActionRename {
					continue
				}
				if item.RemoteKeyTarget != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "           object store: %s -> %s\n",
						item.RemoteKeyPrefix, item.RemoteKeyTarget)
				}
				if len(item.ConfigPaths) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "           config rows to rename: %s\n",
						strings.Join(item.ConfigPaths, ", "))
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"\n%d renamed, %d conflict(s), %d without a publishable name\n",
				report.Renamed, report.Conflicts, report.Skipped)
			if !apply && report.Renamed > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "re-run with --apply to perform the renames")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the renames (default is a dry run)")
	cmd.Flags().BoolVar(&global, "global", false, "only the platform-wide layer")
	cmd.Flags().StringVar(&agentID, "agent", "", "only this agent's layer")
	cmd.Flags().StringVar(&userID, "user", "", "only this chatter's personal layer")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

func skillListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all discovered skills with source",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			var globalCfg config.SkillsCfg

			loader := agent.NewSkillsLoaderWithGlobal(homeDir, ".", "", config.SkillsConfig{}, globalCfg)
			loaded := loader.LoadSkills()

			if len(loaded) == 0 {
				fmt.Println("No skills discovered.")
				return nil
			}

			fmt.Printf("%-25s %-20s %s\n", "NAME", "SOURCE", "DESCRIPTION")
			fmt.Println(strings.Repeat("-", 75))
			for _, s := range loaded {
				desc := s.Description
				if len(desc) > 40 {
					desc = desc[:37] + "..."
				}
				fmt.Printf("%-25s %-20s %s\n", s.Name, s.Layer, desc)
			}
			return nil
		},
	}
}

func skillSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search the ClawHub skill registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := skills.NewClawHubClient()
			results, err := client.Search(args[0])
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			if len(results) == 0 {
				fmt.Println("No skills found.")
				return nil
			}

			fmt.Printf("%-25s %-10s %-10s %s\n", "SLUG", "VERSION", "DOWNLOADS", "DESCRIPTION")
			fmt.Println(strings.Repeat("-", 80))
			for _, s := range results {
				desc := s.Description
				if len(desc) > 35 {
					desc = desc[:32] + "..."
				}
				fmt.Printf("%-25s %-10s %-10d %s\n", s.Slug, s.Version, s.Downloads, desc)
			}
			return nil
		},
	}
}

func skillInstallCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "install <slug>",
		Short: "Install a skill from ClawHub",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			targetDir := filepath.Join(homeDir, "skills")
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return fmt.Errorf("create skills dir: %w", err)
			}

			client := skills.NewClawHubClient()
			fmt.Printf("Installing %s...\n", slug)
			if err := client.Install(slug, version, targetDir); err != nil {
				return fmt.Errorf("install failed: %w", err)
			}

			fmt.Printf("Skill %q installed to %s\n", slug, filepath.Join(targetDir, slug))
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "specific version to install")
	return cmd
}

func skillUpdateCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "update [slug]",
		Short: "Update installed skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			targetDir := filepath.Join(homeDir, "skills")
			client := skills.NewClawHubClient()

			if all {
				installed, err := skills.ListInstalled(targetDir)
				if err != nil {
					return err
				}
				if len(installed) == 0 {
					fmt.Println("No installed skills to update.")
					return nil
				}
				for _, s := range installed {
					fmt.Printf("Updating %s...\n", s.Name)
					if err := client.Update(s.Name, targetDir); err != nil {
						fmt.Printf("  Failed: %v\n", err)
					} else {
						fmt.Printf("  Updated %s\n", s.Name)
					}
				}
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a skill slug or use --all")
			}

			slug := args[0]
			fmt.Printf("Updating %s...\n", slug)
			if err := client.Update(slug, targetDir); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}
			fmt.Printf("Skill %q updated.\n", slug)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "update all installed skills")
	return cmd
}

func skillRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			skillDir := filepath.Join(homeDir, "skills", name)
			if _, err := os.Stat(skillDir); os.IsNotExist(err) {
				return fmt.Errorf("skill %q not found at %s", name, skillDir)
			}

			if err := os.RemoveAll(skillDir); err != nil {
				return fmt.Errorf("remove skill: %w", err)
			}

			fmt.Printf("Skill %q removed.\n", name)
			return nil
		},
	}
}

func skillInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <slug>",
		Short: "Show skill details from the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := skills.NewClawHubClient()
			info, err := client.Info(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Name:        %s\n", info.Name)
			fmt.Printf("Slug:        %s\n", info.Slug)
			fmt.Printf("Version:     %s\n", info.Version)
			fmt.Printf("Description: %s\n", info.Description)
			fmt.Printf("Downloads:   %d\n", info.Downloads)
			return nil
		},
	}
}
