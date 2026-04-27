package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// userCmd manages API keys.
func userCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apikey",
		Aliases: []string{"user"},
		Short:   "Manage API keys for accessing FastClaw agents",
		Long: `Manage API keys for accessing the FastClaw HTTP API (chat, agents, config).

Keys are persisted through the configured Store (DB-backed in cloud, file-backed
locally). The plaintext token is shown once at creation/rotation — capture it
right away, only the SHA256 hash is retained.`,
	}
	cmd.AddCommand(apikeyAddCmd())
	cmd.AddCommand(apikeyListCmd())
	cmd.AddCommand(apikeyRemoveCmd())
	cmd.AddCommand(apikeyRotateCmd())
	return cmd
}

// openRegistry loads the same Store configuration the gateway uses, so the
// CLI writes to wherever the running fastclaw will read from. Reads env.toml
// the same way the gateway does — there's no second config path.
func openRegistry() (*users.Registry, store.Store, error) {
	cfg := config.LoadEnv()
	homeDir, err := config.HomeDir()
	if err != nil {
		return nil, nil, err
	}
	st, err := store.New(&store.StorageConfig{
		Type:        store.StorageType(cfg.Storage.Type),
		DSN:         cfg.Storage.DSN,
		AutoMigrate: cfg.Storage.AutoMigrate,
	}, homeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	reg, err := users.Load(st)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return reg, st, nil
}

func apikeyAddCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "add [id]",
		Short: "Create a new API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, st, err := openRegistry()
			if err != nil {
				return err
			}
			defer st.Close()
			ak, key, err := reg.Add(args[0], name)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "API key created: %s\n", ak.ID)
			fmt.Println(key)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name for this key")
	return cmd
}

func apikeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, st, err := openRegistry()
			if err != nil {
				return err
			}
			defer st.Close()
			list := reg.List()
			if len(list) == 0 {
				fmt.Println("No API keys configured.")
				return nil
			}
			for _, ak := range list {
				name := ak.Name
				if name == "" {
					name = ak.ID
				}
				fmt.Printf("%-20s %-20s %s\n", ak.ID, name, ak.Key)
			}
			return nil
		},
	}
}

func apikeyRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [id]",
		Short: "Remove an API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, st, err := openRegistry()
			if err != nil {
				return err
			}
			defer st.Close()
			return reg.Remove(args[0])
		},
	}
}

func apikeyRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rotate [id]",
		Aliases: []string{"token"},
		Short:   "Rotate (regenerate) an API key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, st, err := openRegistry()
			if err != nil {
				return err
			}
			defer st.Close()
			key, err := reg.IssueToken(args[0])
			if err != nil {
				return err
			}
			fmt.Println(key)
			return nil
		},
	}
}
