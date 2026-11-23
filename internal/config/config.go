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

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/contrib"

	"github.com/suprbdev/pdbr/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbr/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbr/internal/plugins/postgis"
	"github.com/suprbdev/pdbr/internal/plugins/smartcomments"
)

// EnvPrefix is the environment-variable prefix (PDBR_DATABASE_URL, ...).
const EnvPrefix = "PDBR_"

// Config is the effective configuration.
type Config struct {
	Database confx.Database                `koanf:"database"`
	Server   Server                        `koanf:"server"`
	Schema   Schema                        `koanf:"schema"`
	Filters  confx.Filters                 `koanf:"filters"`
	Auth     confx.Auth                    `koanf:"auth"`
	Roles    confx.Roles                   `koanf:"roles"`
	Claims   confx.Claims                  `koanf:"claims"`
	TX       confx.TX                      `koanf:"transactions"`
	Watch    confx.Watch                   `koanf:"watch"`
	Errors   confx.Errors                  `koanf:"errors"`
	Plugins  confx.Plugins[PluginSettings] `koanf:"plugins"`
	Log      confx.Log                     `koanf:"log"`
	Metrics  confx.Metrics                 `koanf:"metrics"`

	Contracts []confx.Contract `koanf:"contracts" doc:"Schema contracts: named APIs for different audiences, each a subset of the full API selected by @contract / @behavior ±contract:<name> smart comments and served with its own OpenAPI document under its own base path. Entries are {name, default: include|exclude, path (default <server.base_path>/<name>)}, or bare names. When set, only the contracts are served (give one contract path: / to serve it at the root). Requires the smart_comments plugin."`
}

// Server is the pdbr-owned HTTP section.
type Server struct {
	Addr                     string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	BasePath                 string        `koanf:"base_path" doc:"Prefix for every API route (\"\" mounts at the root). /healthz and /readyz are never prefixed."`
	OpenAPI                  bool          `koanf:"openapi" doc:"Serve the OpenAPI 3.1 document at GET {base_path}/openapi.json (on by default; it reveals every table, column, relation and function to unauthenticated callers)."`
	OpenAPIDomains           bool          `koanf:"openapi_domains" doc:"Document each domain over a built-in type as a named component schema (its base type's schema, the domain's comment and constraints, pattern/minimum/maximum when a CHECK is a simple comparison) that columns, arguments and results reference. Off inlines the base type's schema."`
	RequestTimeout           time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout; expiry answers 504."`
	ReadyzTimeout            time.Duration `koanf:"readyz_timeout" doc:"Timeout of the database ping behind GET /readyz; an unreachable or slow database answers 503 unavailable. /healthz stays a liveness check."`
	MaxBodyBytes             int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes; over answers 413."`
	MaxEmbedDepth            int           `koanf:"max_embed_depth" doc:"Maximum select= embed nesting depth; deeper answers 400 embed_depth_exceeded."`
	MaxCost                  int           `koanf:"max_cost" doc:"Maximum estimated cost (1 + list multipliers) per request; over answers 400 cost_exceeded. A list counts the rows the SQL can fetch: its limit, else max_page_size (to-many embeds included). @costMultiplier smart comments tune the estimate."`
	MaxPageSize              int           `koanf:"max_page_size" doc:"Largest accepted limit (larger answers 400 page_size_exceeded); also the page size when limit is absent."`
	MaxHeadlineDocument      int           `koanf:"max_headline_document" doc:"Characters of a document a full-text headline (<col>_headline) reads: a text document is cut to its first max_headline_document characters, a json or jsonb document longer than that gives a null headline. ts_headline re-parses its document on every call, so each headline costs 10 per started 8192 characters of this bound in max_cost."`
	CORSOrigins              []string      `koanf:"cors_origins" doc:"Allowed CORS origins (exact match, or '*' for any). Empty disables CORS headers and preflight entirely."`
	Compression              bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	ReadOnly                 bool          `koanf:"read_only" doc:"Answer every write with 405 (code read_only) for read replicas, maintenance or demos. Also toggleable at runtime when embedding pdbr as a library (App.SetReadOnly)."`
	Etags                    bool          `koanf:"etags" doc:"Weak ETag on single-row GETs, If-None-Match answering 304, If-Match on PATCH/DELETE answering 412. The hash covers every column the resource exposes (not @omit / @omit read ones); disable when column-level grants deny SELECT on an exposed column."`
	CSV                      bool          `koanf:"csv" doc:"Honour Accept: text/csv on list GETs and set-returning RPCs."`
	CSVEscapeFormulas        bool          `koanf:"csv_escape_formulas" doc:"Prefix CSV header and string cells that start with =, +, -, @, a tab or a carriage return with a single quote, so spreadsheets do not evaluate them as formulas (CSV injection). Numbers are never escaped. Disable only when every consumer reads the CSV as data."`
	AllowUnfilteredMutations bool          `koanf:"allow_unfiltered_mutations" doc:"Allow PATCH and DELETE on a collection route without a filter (every visible row). Off by default: such requests answer 400 unfiltered_mutation."`
	Aggregates               bool          `koanf:"aggregates" doc:"Allow aggregate functions in select= (count(), col.sum(), col.avg(), col.max(), col.min(); col.extent(), col.union(), col.collect(), col.centroid() on spatial columns), grouped by the other selected items. Off by default (PostgREST db-aggregates-enabled): an aggregate reads every row its filters match, which limit does not bound; requests answer 400 aggregates_disabled."`
	PartialBulkWrites        bool          `koanf:"partial_bulk_writes" doc:"Let a bulk POST or PUT opt in (Prefer: partial=items) to running each element in its own savepoint, so a failing element is reported without rolling back the others. The response is then 207 with a per-element report whenever one failed. Off by default: the token is ignored, or 400 invalid_preference under handling=strict."`
	PartialBulkMaxItems      int           `koanf:"partial_bulk_max_items" doc:"Most elements a bulk POST or PUT may carry when it opts in with Prefer: partial=items; each element is its own statement and savepoint, so this bounds the round trips one request makes. Larger bodies answer 413 body_too_large."`
	TxEnd                    string        `koanf:"tx_end" doc:"How a write's transaction ends (PostgREST db-tx-end): commit, commit-allow-override, rollback or rollback-allow-override. rollback runs every write (POST, PUT, PATCH, DELETE, volatile RPCs) and discards its effects while still answering with the result; the -allow-override forms honour Prefer: tx=commit|rollback, echoed in Preference-Applied. Without an override form Prefer: tx= is ignored, or 400 invalid_preference under handling=strict."`
}

// PluginSettings is the plugins.settings section: one typed section per
// configurable built-in plugin, keyed by the plugin's name.
type PluginSettings struct {
	AdvancedFilters advancedfilters.Settings `koanf:"advanced_filters" doc:"advanced_filters plugin: relation filters, related-column ordering and computed-column filter/order."`
	PostGIS         postgis.Settings         `koanf:"postgis" doc:"postgis plugin: the spatial query surface of geometry, geography and raster columns."`
	NestedMutations nestedmutations.Settings `koanf:"nested_mutations" doc:"nested_mutations plugin: related rows written in the same statement as a POST or PATCH."`
}

// DefaultPluginSettings returns every built-in plugin's default settings.
func DefaultPluginSettings() PluginSettings {
	return PluginSettings{
		AdvancedFilters: advancedfilters.DefaultSettings(),
		PostGIS:         postgis.DefaultSettings(),
		NestedMutations: nestedmutations.DefaultSettings(),
	}
}

// Validate reports out-of-range plugin settings, keyed by config path.
func (p PluginSettings) Validate() []string {
	return p.NestedMutations.Validate()
}

// BuiltinPlugins are the names of the built-in plugins, in priority order:
// the names plugins.disabled accepts.
var BuiltinPlugins = []string{smartcomments.Name, advancedfilters.Name, postgis.Name, nestedmutations.Name}

// HeaderPrefix is the default auth.headers.prefix.
const HeaderPrefix = "X-Pdbr-Claim-"

// MovedKeys maps pdbr keys renamed before 1.0 to their replacement (on top
// of confx.MovedKeys), so loading an old config names the new key.
var MovedKeys = map[string]string{
	"plugins.settings.advanced-filters": "plugins.settings.advanced_filters",
	"plugins.settings.nested-mutations": "plugins.settings.nested_mutations",
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
			StatementTimeout: 25 * time.Second,
		},
		Server: Server{
			Addr:                ":8080",
			BasePath:            "/api",
			OpenAPI:             true,
			OpenAPIDomains:      true,
			RequestTimeout:      30 * time.Second,
			ReadyzTimeout:       2 * time.Second,
			MaxBodyBytes:        1 << 20,
			MaxEmbedDepth:       5,
			MaxCost:             100000,
			MaxPageSize:         100,
			MaxHeadlineDocument: contrib.MaxHeadlineDocument,
			Etags:               true,
			CSV:                 true,
			CSVEscapeFormulas:   true,
			TxEnd:               "commit",
			PartialBulkMaxItems: 1000,
		},
		Schema: Schema{Schema: confx.Schema{
			Schemas:   []string{"public"},
			Functions: true,
		}},
		Filters: confx.Filters{IndexedOnly: true},
		Auth:    confx.DefaultAuth(HeaderPrefix),
		Roles:   confx.DefaultRoles(),
		Claims:  confx.DefaultClaims(),
		TX:      confx.TX{Mutations: true, Isolation: "read_committed"},
		Watch:   confx.Watch{PollInterval: 5 * time.Second, Channel: "pdbr_ddl"},
		Errors:  confx.Errors{Detail: "prod"},
		Plugins: confx.Plugins[PluginSettings]{Settings: DefaultPluginSettings()},
		Log:     confx.Log{Level: "info", Format: "text"},
		Metrics: confx.DefaultMetrics(),
	}
}

// Load builds the effective config: defaults, then the optional YAML file,
// then PDBR_* env vars, then flags (highest precedence), and validates it.
// Pass only the flags the user actually set (confx.ChangedFlags). Loading
// is strict: unknown keys in the file, and unknown PDBR_* variables inside
// a config section, are errors naming the key they moved to or the
// closest valid one.
func Load(path string, flags *flag.FlagSet) (Config, error) {
	cfg, err := LoadUnvalidated(path, flags)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// LoadUnvalidated is Load without the final Validate, for callers that
// adjust the config first (UseConnectionIdentity) and then validate it.
func LoadUnvalidated(path string, flags *flag.FlagSet) (Config, error) {
	cfg := DefaultConfig()
	err := confx.Load(EnvPrefix, path, flags, &cfg, confx.WithMoved(MovedKeys))
	return cfg, err
}

// UseConnectionIdentity makes every request run as the connection role
// without credentials (auth.mode none, roles.switch false): the identity
// of the CLI commands that serve no HTTP clients (pdbr request, schema
// dump and check, plugins list).
func (c *Config) UseConnectionIdentity() {
	c.Auth.Mode = auth.ModeNone
	c.Roles.Switch = false
}

// Validate checks every rule in docs/configuration.md and reports all
// violations together.
func (c *Config) Validate() error {
	var errs []string
	errs = append(errs, confx.ValidateErrors(c.Errors)...)
	errs = append(errs, confx.ValidateIdentity(c.Auth, c.Roles, c.Claims)...)
	errs = append(errs, confx.ValidateTX(c.TX)...)
	errs = append(errs, confx.ValidateWatch(c.Watch, c.Schema.CachePath)...)
	errs = append(errs, confx.ValidateSchema(c.Schema.Schema)...)
	errs = append(errs, validateServer(c.Server)...)
	errs = append(errs, confx.ValidateLog(c.Log)...)
	errs = append(errs, confx.ValidateMetrics(c.Metrics, c.Server.Addr)...)
	errs = append(errs, confx.ValidatePluginNames(c.Plugins.Disabled, BuiltinPlugins)...)
	errs = append(errs, c.Plugins.Settings.Validate()...)
	if len(c.Contracts) == 0 {
		errs = append(errs, validateMetricsPath(c.Metrics, c.Server)...)
	} else {
		errs = append(errs, confx.ValidateContracts(c.Contracts, c.Server.BasePath, false)...)
		if c.DisabledPlugins()[smartcomments.Name] {
			errs = append(errs, "contracts require the smart_comments plugin: it removes the columns a contract hides (remove smart_comments from plugins.disabled)")
		}
		for _, ct := range c.Contracts {
			s := c.Server
			s.BasePath = c.ContractBase(ct)
			errs = append(errs, validateMetricsPath(c.Metrics, s)...)
		}
	}
	return confx.InvalidConfig(errs)
}

// Contract returns the configured contract named name; "" is the full API
// and is always found.
func (c *Config) Contract(name string) (confx.Contract, error) {
	if name == "" {
		return confx.Contract{}, nil
	}
	ct, ok := confx.FindContract(c.Contracts, name)
	if !ok {
		return confx.Contract{}, fmt.Errorf("config: unknown contract %q", name)
	}
	return ct, nil
}

// ContractBase returns the base path contract ct is served under: its
// path, or server.base_path joined with its name, with "/" as "" (the
// root, like an empty server.base_path). The full API (Name "") is served
// under server.base_path.
func (c *Config) ContractBase(ct confx.Contract) string {
	if ct.Name == "" {
		return c.Server.BasePath
	}
	if p := ct.URLPath(c.Server.BasePath); p != "/" {
		return p
	}
	return ""
}

// validateMetricsPath rejects a metrics.path that would shadow one of pdbr's
// own routes on the main listener: /healthz, /readyz, or server.base_path and
// any path under it (the API and openapi.json). With an empty base_path only
// /openapi.json is added: the API owns every path there and metrics.path
// takes precedence over a resource of the same name. A separate metrics.addr
// never collides.
func validateMetricsPath(m confx.Metrics, s Server) []string {
	if !m.Enabled || m.Addr != "" {
		return nil
	}
	p := m.Path
	switch {
	case p == "/healthz" || p == "/readyz":
	case s.BasePath != "" && (p == s.BasePath || strings.HasPrefix(p, s.BasePath+"/")):
	case s.BasePath == "" && p == "/openapi.json":
	default:
		return nil
	}
	return []string{fmt.Sprintf("metrics.path: %q collides with a pdbr route on server.addr; choose another path or set metrics.addr", p)}
}

func validateServer(s Server) []string {
	var errs []string
	if s.MaxPageSize < 1 {
		errs = append(errs, "server.max_page_size must be >= 1")
	}
	if bp := s.BasePath; bp != "" && (!strings.HasPrefix(bp, "/") || strings.HasSuffix(bp, "/")) {
		errs = append(errs, fmt.Sprintf("server.base_path: %q must be empty or start with '/' and not end with '/'", bp))
	}
	if s.ReadyzTimeout <= 0 {
		errs = append(errs, "server.readyz_timeout must be > 0")
	}
	if s.MaxEmbedDepth < 1 {
		errs = append(errs, "server.max_embed_depth must be >= 1")
	}
	if s.MaxHeadlineDocument < 1 {
		errs = append(errs, "server.max_headline_document must be >= 1")
	}
	if s.PartialBulkMaxItems < 1 {
		errs = append(errs, "server.partial_bulk_max_items must be >= 1")
	}
	switch s.TxEnd {
	case "commit", "commit-allow-override", "rollback", "rollback-allow-override":
	default:
		errs = append(errs, fmt.Sprintf("server.tx_end: %q must be commit, commit-allow-override, rollback or rollback-allow-override", s.TxEnd))
	}
	return errs
}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool { return c.Plugins.DisabledSet() }
