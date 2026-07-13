package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/telemetry"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status [path]",
		Short: "Show index staleness, watch daemon state, and stats",
		Long:  statusLongHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			m, mErr := meta.Read(root)
			fresh := freshness.Inspect(root)
			daemonState, _ := daemon.ReadState(root)
			if asJSON {
				out := map[string]interface{}{
					"meta_path": paths.MetaPath(root),
					"meta":      m,
					"freshness": fresh,
					"watch":     daemonState,
					"telemetry": telemetry.Snapshot(),
				}
				if mErr != nil {
					out["meta_error"] = mErr.Error()
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if mErr != nil {
				fmt.Println("not indexed:", mErr)
				return nil
			}
			fmt.Println("meta:", paths.MetaPath(root))
			b, _ := json.MarshalIndent(m, "", "  ")
			fmt.Println(string(b))
			fmt.Println()
			fmt.Println("freshness:")
			fb, _ := json.MarshalIndent(fresh, "", "  ")
			fmt.Println(string(fb))
			if daemonState != nil {
				fmt.Println()
				fmt.Println("watch daemon:")
				db, _ := json.MarshalIndent(daemonState, "", "  ")
				fmt.Println(string(db))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable status with telemetry/freshness/watch snapshot")
	return c
}
