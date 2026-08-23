// Command pdbr serves an automatically mapped REST API for a PostgreSQL
// database, and provides schema-cache, config, and one-off request tooling.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/plugin"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitError carries a process exit code without a message (the output was
// already written): pdbr request exits 2 on a 4xx/5xx response.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// configFlags defines flags that mirror config keys; their names are the
// koanf paths so precedence (flags > env > file) needs no mapping table.
func configFlags(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.String("database.url", "", "PostgreSQL connection URL")
	fs.String("server.addr", "", "HTTP listen address")
	fs.String("server.base_path", "", "prefix for every API route (\"\" mounts at the root)")
	fs.Bool("server.openapi", true, "serve the OpenAPI document at {base_path}/openapi.json")
	fs.StringSlice("server.cors_origins", nil, "allowed CORS origins ('*' for any)")
	fs.Bool("server.compression", false, "gzip responses when the client accepts it")
	fs.Bool("server.read_only", false, "answer every write with 405 (read-only mode)")
	fs.StringSlice("schema.schemas", nil, "PostgreSQL schemas to expose")
	fs.String("schema.cache_path", "", "boot from schema cache file")
	fs.Bool("rls.enabled", true, "enable RLS role switching")
	fs.Bool("watch.enabled", false, "watch for DDL changes (dev)")
	fs.Bool("filters.indexed_only", true, "only indexed columns filterable")
	fs.String("errors.detail", "", "error verbosity: dev|prod|strict")
	fs.String("log.level", "", "log level")
	registerEnumCompletion(cmd, "errors.detail", "dev", "prod", "strict")
	registerEnumCompletion(cmd, "log.level", "debug", "info", "warn", "error")
}

func registerEnumCompletion(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}

func loadConfig(cmd *cobra.Command) (config.Config, error) {
	cfg, err := loadConfigRaw(cmd)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// loadConfigRaw loads defaults < file < env < flags without validating, for
// commands that adjust the config before validation (pdbr request forces
// rls.auth.mode=none, so a missing jwt_secret must not stop it).
func loadConfigRaw(cmd *cobra.Command) (config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = confx.Discover("pdbr.yaml", "pdbr.yml")
	}
	cfg := config.DefaultConfig()
	if err := confx.Load(config.EnvPrefix, path, confx.ChangedFlags(cmd.Flags(), "config"), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func newApp(cfg config.Config) *pdbr.App {
	return pdbr.New(cfg, pdbr.WithVersion(version))
}

func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pdbr",
		Short:         "Zero-boilerplate REST API for PostgreSQL",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config file (default: ./pdbr.yaml if present)")
	root.AddCommand(serveCmd(), requestCmd(), schemaCmd(), configCmd(), pluginsCmd())
	return root
}

func serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the REST API server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			app := newApp(cfg)
			defer app.Close()
			return app.Serve(signalContext())
		},
	}
	configFlags(cmd)
	return cmd
}

func requestCmd() *cobra.Command {
	var body, accept string
	var headers []string
	var include, printSQL bool
	cmd := &cobra.Command{
		Use:   "request METHOD PATH",
		Short: "Run a one-off request through the real handler (body to stdout, status to stderr)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigRaw(cmd)
			if err != nil {
				return err
			}
			// One-off CLI requests run without HTTP auth: no role switch.
			cfg.RLS.Auth.Mode = "none"
			if err := cfg.Validate(); err != nil {
				return err
			}
			if accept != "" {
				headers = append(headers, "Accept: "+accept)
			}
			return runRequest(signalContext(), cfg, requestSpec{
				method: strings.ToUpper(args[0]), path: args[1], body: body, headers: headers,
				include: include, printSQL: printSQL,
			}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "request body: inline JSON, @file, or - for stdin")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "request header 'K: V' (repeatable)")
	cmd.Flags().StringVar(&accept, "accept", "", "Accept header (shorthand for --header 'Accept: ...')")
	cmd.Flags().BoolVarP(&include, "include", "i", false, "print response headers before the body")
	cmd.Flags().BoolVar(&printSQL, "print-sql", false, "print the compiled SQL and parameters instead of executing")
	configFlags(cmd)
	return cmd
}

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schema", Short: "Schema cache and OpenAPI tools"}

	var out string
	dump := &cobra.Command{
		Use:   "dump",
		Short: "Introspect the database and write a schema cache file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			cfg.Schema.CachePath = "" // always introspect live
			app := newApp(cfg)
			defer app.Close()
			cat, err := app.LoadCatalog(signalContext())
			if err != nil {
				return err
			}
			if err := cache.Save(out, cat); err != nil {
				return err
			}
			hash, _ := cat.Hash()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d tables, hash %s)\n", out, len(cat.Tables), hash[:12])
			return nil
		},
	}
	dump.Flags().StringVarP(&out, "output", "o", "schema.cache", "output path")
	configFlags(dump)

	var cachePath string
	check := &cobra.Command{
		Use:   "check",
		Short: "Compare the live database schema against a cache file (exit 1 on drift)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			// Load the cache before touching the database so a corrupt or
			// stale-format file fails fast, then compare against a live
			// introspection regardless of schema.cache_path.
			cached, err := cache.Load(cachePath)
			if err != nil {
				return err
			}
			cfg.Schema.CachePath = ""
			app := newApp(cfg)
			defer app.Close()
			live, err := app.LoadCatalog(signalContext())
			if err != nil {
				return err
			}
			report, err := schemaDrift(cached, live)
			if err != nil {
				return err
			}
			if report != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), report)
				return &exitError{code: 1}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "schema cache is up to date")
			return nil
		},
	}
	check.Flags().StringVar(&cachePath, "cache", "schema.cache", "cache file to compare against")
	configFlags(check)

	var asYAML bool
	print := &cobra.Command{
		Use:   "print",
		Short: "Print the OpenAPI 3.1 document (JSON, or YAML with --yaml)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			app := newApp(cfg)
			defer app.Close()
			ctx := signalContext()
			cat, err := app.LoadCatalog(ctx)
			if err != nil {
				return err
			}
			built, err := app.BuildAPI(ctx, cat)
			if err != nil {
				return err
			}
			render := built.OpenAPI.JSON
			if asYAML {
				render = built.OpenAPI.YAML
			}
			doc, err := render()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(doc)
			return err
		},
	}
	print.Flags().BoolVar(&asYAML, "yaml", false, "output YAML instead of JSON")
	configFlags(print)

	cmd.AddCommand(dump, check, print)
	return cmd
}

// schemaDrift renders the `schema check` report: the hash summary followed
// by one indented line per changed object ("" when the catalogs match).
func schemaDrift(cached, live *introspect.Catalog) (string, error) {
	ch, err := cached.Hash()
	if err != nil {
		return "", err
	}
	lh, err := live.Hash()
	if err != nil {
		return "", err
	}
	if ch == lh {
		return "", nil
	}
	lines := []string{fmt.Sprintf("schema drift detected: cache %s != live %s", ch[:12], lh[:12])}
	for _, d := range introspect.Diff(cached, live) {
		lines = append(lines, "  "+d)
	}
	return strings.Join(lines, "\n"), nil
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Configuration tools"}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter pdbr.yaml in the current directory",
		RunE: func(_ *cobra.Command, _ []string) error {
			const path = "pdbr.yaml"
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			starter := `# pdbr configuration. Run "pdbr config example" for the full annotated reference.
database:
  # Fill in real credentials, or delete the url and set PDBR_DATABASE_URL.
  url: "postgres://USER:PASSWORD@HOST:5432/DB"
server:
  addr: ":8080"
  base_path: "/api"
schema:
  schemas: ["public"]
rls:
  enabled: false # enable + configure auth before production use
`
			if err := os.WriteFile(path, []byte(starter), 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", path)
			return nil
		},
	}

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the effective configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := loadConfig(cmd); err != nil {
				return err
			}
			fmt.Println("configuration is valid")
			return nil
		},
	}
	configFlags(validate)

	example := &cobra.Command{
		Use:   "example",
		Short: "Print the fully-commented reference configuration (generated from the config structs)",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(config.ExampleYAML())
		},
	}

	cmd.AddCommand(initCmd, validate, example)
	return cmd
}

func pluginsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plugins", Short: "Plugin tools"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered plugins and the hooks they implement",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				// plugins list should work without a valid DB config
				cfg = config.DefaultConfig()
				cfg.RLS.Enabled = false
			}
			app := newApp(cfg)
			disabled := cfg.DisabledPlugins()
			for _, p := range app.Plugins() {
				status := "enabled"
				if disabled[p.Name()] {
					status = "disabled"
				}
				fmt.Printf("%-20s priority=%-4d %-8s hooks: %s\n",
					p.Name(), p.Priority(), status, strings.Join(plugin.Hooks(p), ", "))
			}
			return nil
		},
	}
	configFlags(list)
	cmd.AddCommand(list)
	return cmd
}
