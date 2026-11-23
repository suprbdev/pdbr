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
	"github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/contrib"
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
	fs.StringSlice("server.cors_origins", nil, "allowed CORS origins ('*' for any)")
	fs.Bool("server.compression", false, "gzip responses when the client accepts it")
	fs.Bool("server.read_only", false, "answer every write with 405 (read-only mode)")
	fs.Bool("openapi.enabled", true, "serve the OpenAPI document at {base_path}/openapi.json")
	fs.String("openapi.title", "", "OpenAPI info.title (default pdbr)")
	fs.String("openapi.server_url", "", "absolute origin put before the base path in the OpenAPI servers[0].url")
	fs.Int("limits.max_headline_document", contrib.MaxHeadlineDocument, "characters of a document a full-text headline reads")
	fs.StringSlice("schema.schemas", nil, "PostgreSQL schemas to expose")
	fs.String("schema.cache_path", "", "boot from schema cache file")
	fs.Bool("schema.expose_partitions", false, "expose table partitions as resources of their own")
	fs.Bool("schema.domains", true, "document domains as named OpenAPI component schemas")
	fs.Bool("mutations.allow_unfiltered", false, "allow PATCH/DELETE on a collection without a filter")
	fs.Bool("aggregates.enabled", false, "allow aggregate functions in select= (count(), col.sum(), ...)")
	fs.String("transactions.end", "", "how a write's transaction ends: commit|commit-allow-override|rollback|rollback-allow-override")
	fs.Bool("transactions.partial.enabled", false, "let a bulk POST/PUT opt in (Prefer: partial=items) to per-element savepoints")
	fs.Int("transactions.partial.max_items", 1000, "most elements a Prefer: partial=items bulk body may carry")
	fs.String("auth.mode", "", "credential source: jwt|headers|none")
	fs.Bool("roles.switch", true, "run each request as its resolved role (SET LOCAL ROLE)")
	fs.String("roles.anonymous", "", "role of requests without credentials (empty answers them 401)")
	fs.Bool("watch.enabled", false, "watch for DDL changes (dev)")
	fs.Bool("filters.indexed_only", true, "only indexed columns filterable")
	fs.String("errors.detail", "", "error verbosity: dev|prod|strict")
	fs.String("log.level", "", "log level")
	fs.Bool("metrics.enabled", false, "serve Prometheus metrics")
	fs.String("metrics.path", "", "URL path of the metrics endpoint (default /metrics)")
	fs.String("metrics.addr", "", "separate listen address for the metrics endpoint, e.g. :9090")
	registerEnumCompletion(cmd, "transactions.end", "commit", "commit-allow-override", "rollback", "rollback-allow-override")
	registerEnumCompletion(cmd, "auth.mode", "jwt", "headers", "none")
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
// commands that adjust the config before validation.
func loadConfigRaw(cmd *cobra.Command) (config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = confx.Discover("pdbr.yaml", "pdbr.yml")
	}
	return config.LoadUnvalidated(path, changedConfigFlags(cmd))
}

// changedConfigFlags returns the config flags set on cmd's command line:
// the flags named after a config path (configFlags), leaving out --config
// and command options such as --cache or --print-sql, which strict loading
// would reject as unknown keys.
func changedConfigFlags(cmd *cobra.Command) *pflag.FlagSet {
	var local []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !strings.Contains(f.Name, ".") {
			local = append(local, f.Name)
		}
	})
	return confx.ChangedFlags(cmd.Flags(), local...)
}

// loadConnectionConfig loads the config of a command that serves no HTTP
// clients (request, schema dump/check, plugins list): it runs as the
// connection role without credentials (config.UseConnectionIdentity) and is
// then validated, so it works on a default config without a JWT secret.
func loadConnectionConfig(cmd *cobra.Command) (config.Config, error) {
	cfg, err := loadConfigRaw(cmd)
	if err != nil {
		return cfg, err
	}
	cfg.UseConnectionIdentity()
	return cfg, cfg.Validate()
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
			cfg, err := loadConnectionConfig(cmd)
			if err != nil {
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
			cfg, err := loadConnectionConfig(cmd)
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
			if err := cache.Save(out, cat, cache.Writer{Product: "pdbr", Version: version}); err != nil {
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
			cfg, err := loadConnectionConfig(cmd)
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
	var contractName string
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
			built, err := app.BuildContractAPI(ctx, cat, contractName)
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
	print.Flags().StringVar(&contractName, "contract", "", "print the OpenAPI document of this configured contract instead of the full API")
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
			if err := os.WriteFile(path, []byte(config.Starter), 0o644); err != nil {
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
			cfg, err := loadConnectionConfig(cmd)
			if err != nil {
				return err
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
