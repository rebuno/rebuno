package main

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/spf13/cobra"
)

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage scoped client API keys"}
	var scopes []string
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a key and print its token once", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := kernel.CreateAPIKeyRequest{Name: args[0]}
			for _, s := range scopes {
				req.Scopes = append(req.Scopes, domain.Scope(s))
			}
			var key kernel.IssuedAPIKey
			if err := kernelClient().do(cmd.Context(), http.MethodPost, "/v0/api-keys", req, &key); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(key)
		},
	}
	create.Flags().StringSliceVar(&scopes, "scope", nil, "Permission scopes (repeatable or comma-separated)")
	list := &cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List key metadata", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var keys []domain.APIKey
			if err := kernelClient().do(cmd.Context(), http.MethodGet, "/v0/api-keys", nil, &keys); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(keys)
		}}
	rotate := &cobra.Command{Use: "rotate <id>", Short: "Replace a key's secret and print its token once", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var key kernel.IssuedAPIKey
			if err := kernelClient().do(cmd.Context(), http.MethodPost, "/v0/api-keys/"+url.PathEscape(args[0])+"/rotate", nil, &key); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(key)
		}}
	revoke := &cobra.Command{Use: "revoke <id>", Short: "Revoke a key", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return kernelClient().do(cmd.Context(), http.MethodDelete, "/v0/api-keys/"+url.PathEscape(args[0]), nil, nil)
		}}
	cmd.AddCommand(create, list, rotate, revoke)
	return cmd
}
