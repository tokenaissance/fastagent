package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// configsCmd groups config-storage maintenance for the configs /
// configs_kv domain. These are operator commands that write to the DB
// directly, not through the HTTP API.
func configsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configs",
		Short: "Config storage maintenance (configs / configs_kv)",
	}
	cmd.AddCommand(configsReconcileMirrorCmd())
	return cmd
}

// configsReconcileMirrorCmd certifies the configs_kv mirror against the
// configs blob. It is the pre-flip acceptance pass: the mirror may only take
// over as the authoritative read source once every row it would serve is
// certified complete.
func configsReconcileMirrorCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "reconcile-mirror",
		Short: "Certify the configs_kv mirror against the configs blob",
		Long: `Re-project every configs row and compare it against what is actually in
configs_kv. Rows that match are certified (marked as a complete projection, so a
mirror-first reader may trust them); rows that do not are left uncertified and
reported. It repairs nothing — a mismatch is data that needs a decision — and it
is safe to re-run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()

			rep, err := st.ReconcileConfigMirrors(context.Background())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "examined %d row(s): %d certified (%d needed a value_kind backfill, %d leaves retagged), %d gap(s)\n",
				rep.Examined, rep.Certified, rep.Untyped, rep.Retagged, len(rep.Gaps))
			for _, g := range rep.Gaps {
				fmt.Fprintf(out, "  gap %s/%s/%s/%s: blob=%d mirror=%d",
					g.Kind, g.Scope, g.ScopeID, g.Name, g.WantKeys, g.GotKeys)
				if len(g.Missing) > 0 {
					fmt.Fprintf(out, " missing=%v", g.Missing)
				}
				if len(g.Extra) > 0 {
					fmt.Fprintf(out, " extra=%v", g.Extra)
				}
				if len(g.Changed) > 0 {
					fmt.Fprintf(out, " changed=%v", g.Changed)
				}
				fmt.Fprintln(out)
			}
			if strict && len(rep.Gaps) > 0 {
				return fmt.Errorf("%d row(s) are not a complete mirror", len(rep.Gaps))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false,
		"exit non-zero if any row is not a complete mirror")
	return cmd
}
