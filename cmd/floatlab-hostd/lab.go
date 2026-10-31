package main

import (
	"encoding/json"
	"github.com/floatlab/floatlab-core/internal/hostd"
	"github.com/floatlab/floatlab-core/internal/lab"
	"github.com/spf13/cobra"
)

func addLabCommands(root *cobra.Command) {
	var request lab.RecoveryRequest
	command := &cobra.Command{Use: "lab-recover", Short: "Explicitly recover lab consensus after fencing every excluded member", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := hostd.RecoverLabOnHost(cmd.Context(), request)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}
	command.Flags().StringVar(&request.OperationID, "operation-id", "", "Durable recovery operation ID; reuse on retry")
	command.Flags().StringVar(&request.SurvivorID, "survivor", "", "This appliance's permanent node ID")
	command.Flags().StringSliceVar(&request.ExcludedNodeIDs, "exclude", nil, "Every excluded old member ID (comma-separated)")
	command.Flags().BoolVar(&request.ConfirmFenced, "confirm-fenced", false, "Confirm excluded members are fenced from the recovered lab")
	root.AddCommand(command)
}
