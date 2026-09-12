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

// configsReconcileMirrorCmd certifies the configs_kv mirror against the configs
// blob. It is the pre-flip acceptance pass: configs_kv may only take over as
// the authoritative read source once every row it would serve is certified
// complete.
func configsReconcileMirrorCmd() *cobra.Command {
	var strict, repair bool
	cmd := &cobra.Command{
		Use:   "reconcile-mirror",
		Short: "Certify the configs_kv mirror against the configs blob",
		Long: `Mirror every configs row into configs_kv and compare the two — what the
blob holds against what configs_kv actually holds. Rows that match are certified
(marked as a complete mirror, so a configs_kv-first reader may trust them); a
row that matches on names and values but predates value_kind is retagged and
certified; rows that genuinely diverge are left uncertified and reported.

By default nothing is repaired: a mismatch is data that needs a decision. With
--repair, a diverged row is instead rewritten to match the blob — the
namespace's configs_kv leaves are cleared and rewritten from it, then certified.
The blob is authoritative until the mirror takes over, so that is the only
correct direction; use it to catch up a mirror written by an older build (a
collapsed nested map, an ALL_CAPS data key folded to snake_case). Either way the
pass is safe to re-run.

--strict is the pre-flip acceptance gate and it checks two things, not one: no
row may diverge from the blob *and* every examined row must end up certified. A
gap alone is not enough to fail on — a row with no marker is a row the mirror
could not serve either.`,
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
			if strict && rep.Certified != rep.Examined {
				return fmt.Errorf("%d of %d row(s) are not certified: a row the mirror would have to serve has nothing vouching for it",
					rep.Examined-rep.Certified, rep.Examined)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false,
		"exit non-zero unless every examined row is certified (no gaps and nothing left unmarked)")
	cmd.Flags().BoolVar(&repair, "repair", false,
		"rewrite diverged rows from the blob instead of only reporting them")
	return cmd
}
