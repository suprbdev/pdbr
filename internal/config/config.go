// Package config defines pdbr's configuration model and its loading rules:
// YAML file < environment (PDBR_*) < command-line flags.
//
// The shared sections come from pdbcore/confx verbatim; pdbr owns the Server
// section and the root Config/Validate. Every field carries a `koanf` tag
// (its YAML/env path) and a `doc` tag; the annotated reference YAML
// (`pdbr config example`) is generated from these tags.
package config

import (
	"fmt"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/confx"
)

// EnvPrefix is the environment-variable prefix (PDBR_DATABASE_URL, ...).
const EnvPrefix = "PDBR_"

// Config is the effective configuration.
type Config struct {
	Database confx.Database `koanf:"database"`
	Server   Server         `koanf:"server"`
	Schema   Schema         `koanf:"schema"`
	Filters  confx.Filters  `koanf:"filters"`
	RLS      confx.RLS      `koanf:"rls"`
	TX       confx.TX       `koanf:"transactions"`
	Watch    confx.Watch    `koanf:"watch"`
	Errors   confx.Errors   `koanf:"errors"`
	Plugins  confx.Plugins  `koanf:"plugins"`
	Log      confx.Log      `koanf:"log"`
}

// Server is the pdbr-owned HTTP section.
type Server struct {
	Addr                     string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	BasePath                 string        `koanf:"base_path" doc:"Prefix for every API route (\"\" mounts at the root). /healthz and /readyz are never prefixed."`
	OpenAPI                  bool          `koanf:"openapi" doc:"Serve the OpenAPI 3.1 document at GET {base_path}/openapi.json (on by default; it reveals every table, column, relation and function to unauthenticated callers)."`
	RequestTimeout           time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout; expiry answers 504."`
	MaxBodyBytes             int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes; over answers 413."`
	MaxEmbedDepth            int           `koanf:"max_embed_depth" doc:"Maximum select= embed nesting depth; deeper answers 400 embed_depth_exceeded."`
	MaxCost                  int           `koanf:"max_cost" doc:"Maximum estimated cost (1 + list multipliers) per request; over answers 400 cost_exceeded. @costMultiplier smart comments tune the estimate."`
	MaxPageSize              int           `koanf:"max_page_size" doc:"Largest accepted limit (larger answers 400 page_size_exceeded); also the page size when limit is absent."`
	CORSOrigins              []string      `koanf:"cors_origins" doc:"Allowed CORS origins (exact match, or '*' for any). Empty disables CORS headers and preflight entirely."`
	Compression              bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	ReadOnly                 bool          `koanf:"read_only" doc:"Answer every write with 405 (code read_only) for read replicas, maintenance or demos. Also toggleable at runtime when embedding pdbr as a library (App.SetReadOnly)."`
	Etags                    bool          `koanf:"etags" doc:"Weak ETag on single-row GETs, If-None-Match answering 304, If-Match on PATCH/DELETE answering 412. Disable when column-level grants deny SELECT on some columns (the ETag hashes the whole row)."`
	CSV                      bool          `koanf:"csv" doc:"Honour Accept: text/csv on list GETs and set-returning RPCs."`
	AllowUnfilteredMutations bool          `koanf:"allow_unfiltered_mutations" doc:"Allow PATCH and DELETE on a collection route without a filter (every visible row). Off by default: such requests answer 400 unfiltered_mutation."`
	Aggregates               bool          `koanf:"aggregates" doc:"Allow aggregate functions in select= (count(), col.sum(), col.avg(), col.max(), col.min(); col.extent(), col.union(), col.collect(), col.centroid() on spatial columns), grouped by the other selected items. Off by default (PostgREST db-aggregates-enabled): an aggregate reads every row its filters match, which limit does not bound; requests answer 400 aggregates_disabled."`
}

// Schema is the shared schema section plus pdbr's own keys.
type Schema struct {
	confx.Schema     `koanf:",squash"`
	ExposePartitions bool `koanf:"expose_partitions" doc:"Expose each partition of a partitioned table as a resource of its own. Off by default: a partition's rows are already reachable (and writable) through its partitioned parent, which stays a resource either way."`
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Database: confx.Database{
			MaxConns:         10,
			ConnectTimeout:   10 * time.Second,
			StatementTimeout: 30 * time.Second,
		},
		Server: Server{
			Addr:           ":8080",
			BasePath:       "/api",
			OpenAPI:        true,
			RequestTimeout: 30 * time.Second,
			MaxBodyBytes:   1 << 20,
			MaxEmbedDepth:  5,
			MaxCost:        10000,
			MaxPageSize:    100,
			Etags:          true,
			CSV:            true,
		},
		Schema: Schema{Schema: confx.Schema{
			Schemas:   []string{"public"},
			Functions: true,
		}},
		Filters: confx.Filters{IndexedOnly: true},
		RLS: confx.RLS{
			Enabled:       true,
			AnonymousRole: "anonymous",
			RoleClaim:     "role",
			ClaimsPrefix:  "pdbq.claims",
			Auth:          confx.Auth{Mode: "jwt", HeaderPrefix: "X-Pdbr-Claim-", JWKSCacheTTL: time.Hour},
		},
		TX:      confx.TX{Mutations: true, Isolation: "read_committed"},
		Watch:   confx.Watch{PollInterval: 5 * time.Second, Channel: "pdbr_ddl"},
		Errors:  confx.Errors{Detail: "prod"},
		Plugins: confx.Plugins{Settings: map[string]map[string]any{}},
		Log:     confx.Log{Level: "info", Format: "text"},
	}
}

// Load builds the effective config: defaults, then the optional YAML file,
// then PDBR_* env vars, then flags (highest precedence), and validates it.
// Pass only the flags the user actually set (confx.ChangedFlags).
func Load(path string, flags *flag.FlagSet) (Config, error) {
	cfg := DefaultConfig()
	if err := confx.Load(EnvPrefix, path, flags, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks every rule in docs/configuration.md and reports all
// violations together.
func (c *Config) Validate() error {
	var errs []string
	errs = append(errs, confx.ValidateErrors(c.Errors)...)
	errs = append(errs, confx.ValidateRLS(c.RLS)...)
	errs = append(errs, confx.ValidateTX(c.TX)...)
	errs = append(errs, confx.ValidateWatch(c.Watch, c.Schema.CachePath)...)
	errs = append(errs, confx.ValidateSchema(c.Schema.Schema)...)
	errs = append(errs, validateServer(c.Server)...)
	errs = append(errs, confx.ValidateLog(c.Log)...)
	return confx.InvalidConfig(errs)
}

func validateServer(s Server) []string {
	var errs []string
	if s.MaxPageSize < 1 {
		errs = append(errs, "server.max_page_size must be >= 1")
	}
	if bp := s.BasePath; bp != "" && (!strings.HasPrefix(bp, "/") || strings.HasSuffix(bp, "/")) {
		errs = append(errs, fmt.Sprintf("server.base_path: %q must be empty or start with '/' and not end with '/'", bp))
	}
	if s.MaxEmbedDepth < 1 {
		errs = append(errs, "server.max_embed_depth must be >= 1")
	}
	return errs
}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool { return c.Plugins.DisabledSet() }
