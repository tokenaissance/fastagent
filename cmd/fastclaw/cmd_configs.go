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
	var strict, repair bool
	cmd := &cobra.Command{
		Use:   "reconcile-mirror",
		Short: "Certify the configs_kv mirror against the configs blob",
		Long: `Re-project every configs row and compare it against what is actually in
configs_kv. Rows that match are certified (marked as a complete projection, so a
mirror-first reader may trust them); a row that matches on text but predates
value_kind is retagged and certified; rows that genuinely diverge are left
uncertified and reported.

By default nothing is repaired: a mismatch is data that needs a decision. With
--repair, a diverged row is instead re-projected from the blob — the namespace's
configs_kv leaves are cleared and rewritten from the projection, then certified.
The blob is authoritative until the mirror takes over, so that is the only
correct direction; use it to catch up a mirror written by an older build (a
collapsed nested map, an ALL_CAPS data key folded to snake_case). Either way the
pass is safe to re-run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()

			rep, err := st.ReconcileConfigMirrors(context.Background(), repair)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "examined %d row(s): %d certified (%d value_kind backfill: %d leaves; %d repaired: %d leaves), %d gap(s)\n",
				rep.Examined, rep.Certified, rep.Untyped, rep.Retagged, rep.Repaired, rep.Rewritten, len(rep.Gaps))
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
	cmd.Flags().BoolVar(&repair, "repair", false,
		"re-project diverged rows from the blob instead of only reporting them")
	return cmd
}
