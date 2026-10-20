package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/hostupgrade"
	"github.com/floatlab/floatlab-core/internal/managementupdate"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/hostd"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

func main() {
	root := &cobra.Command{
		Use:   "floatlab-hostd",
		Short: "FloatLab host daemon — manages filesystem and containers on behalf of the control plane",
		RunE:  run,
	}
	network := &cobra.Command{Use: "network", Short: "Initialize or restore host networking"}
	network.AddCommand(&cobra.Command{Use: "init", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return hostnetwork.New(hostnetwork.ConfigPath, hostnetwork.Linux{}).Initialize(cmd.Context())
	}})
	expired := false
	rollback := &cobra.Command{Use: "rollback CHANGE_ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := hostnetwork.New(hostnetwork.ConfigPath, hostnetwork.Linux{}).Rollback(args[0], expired)
		return err
	}}
	rollback.Flags().BoolVar(&expired, "expired", false, "Only restore expired unconfirmed changes")
	network.AddCommand(rollback)
	root.AddCommand(network)
	root.AddCommand(&cobra.Command{Use: "update-reconcile", Short: "Recover updates before management startup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		management := managementupdate.NewDefault()
		host := hostd.NewHostUpdateCoordinator(management)
		if err := host.Recover(cmd.Context()); err != nil {
			return err
		}
		return management.Recover(cmd.Context())
	}})
	root.AddCommand(&cobra.Command{Use: "boot-recovery", Short: "Independent OS trial deadline and heartbeat guard", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return hostupgrade.Guard(cmd.Context()) }})
	updates := &cobra.Command{Use: "update", Short: "Root recovery interface through local hostd IPC"}
	for _, kind := range []string{"host", "management"} {
		component := &cobra.Command{Use: kind}
		actions := []string{"status", "releases", "prepare", "activate", "commit", "rollback", "cleanup"}
		if kind == "management" {
			actions = []string{"status", "releases", "start", "rollback", "cleanup"}
		}
		for _, action := range actions {
			command := &cobra.Command{Use: action + " [OPERATION_ID] [RELEASE]", Args: cobra.MaximumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
				if action == "prepare" || action == "start" {
					if len(args) != 2 {
						return fmt.Errorf("provide OPERATION_ID and RELEASE")
					}
				} else if action != "status" && action != "releases" {
					if len(args) != 1 {
						return fmt.Errorf("provide OPERATION_ID")
					}
				} else if action == "releases" && len(args) != 0 {
					return fmt.Errorf("releases accepts no arguments")
				}
				if os.Geteuid() != 0 {
					return fmt.Errorf("run as root")
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Minute)
				defer cancel()
				client := ipc.NewClient("/run/floatlab/hostd.sock")
				if err := client.Connect(ctx); err != nil {
					return err
				}
				defer client.Close()
				request := map[string]string{"actor": "root", "idempotency_key": fmt.Sprintf("root-%d", time.Now().UnixNano())}
				if len(args) > 0 {
					request["operation_id"] = args[0]
				}
				if len(args) > 1 {
					request["release"] = args[1]
				}
				raw, err := client.Execute(ctx, kind+".update."+action, request)
				if err != nil {
					return err
				}
				var value any
				if err = json.Unmarshal(raw, &value); err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
			}}
			component.AddCommand(command)
		}
		updates.AddCommand(component)
	}
	root.AddCommand(updates)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := hostd.NewServer(log)
	if err := srv.Run(ctx); err != nil {
		log.Error("hostd exited with error", zap.Error(err))
		return err
	}
	log.Info("hostd stopped cleanly")
	return nil
}
