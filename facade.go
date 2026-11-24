// Public embedding surface: aliases that let external modules construct a
// Config, implement plugin hooks, and mount pdbr inside their own HTTP mux.
// The implementation stays under internal/; aliases are the contract.
package pdbr

import (
	"github.com/suprbdev/pdbcore/confx"
	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/plugin"
	"github.com/suprbdev/pdbr/internal/server"
)

// Configuration. Shared section types come from pdbcore/confx; Server is
// pdbr's own and Schema embeds the shared section (confx.Schema) with
// pdbr's own keys. Section types carry a Config suffix to avoid clashing with
// unrelated root-package names.
type (
	Config         = config.Config
	DatabaseConfig = confx.Database
	ServerConfig   = config.Server
	SchemaConfig   = config.Schema
	FiltersConfig  = confx.Filters
	AuthConfig     = confx.Auth
	JWTConfig      = confx.JWT
	HeadersConfig  = confx.Headers
	RolesConfig    = confx.Roles
	ClaimsConfig   = confx.Claims
	TXConfig       = confx.TX
	WatchConfig    = confx.Watch
	ErrorsConfig   = confx.Errors
	PluginsConfig  = confx.Plugins[config.PluginSettings]
	PluginSettings = config.PluginSettings
	LogConfig      = confx.Log
	ContractConfig = confx.Contract
)

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config { return config.DefaultConfig() }

// LoadConfig builds the effective config: defaults < YAML < PDBR_* env <
// flags, given as koanf path to value (nil is fine; cliflags.Changed in
// github.com/suprbdev/pdbcore/confx/cliflags reads them from a pflag set),
// and validates it.
func LoadConfig(path string, flags map[string]any) (Config, error) {
	return config.Load(path, flags)
}

// ExampleConfigYAML renders the annotated reference configuration.
func ExampleConfigYAML() string { return config.ExampleYAML() }

// Plugin hook surface.
type (
	Plugin         = plugin.Plugin
	CatalogHook    = plugin.CatalogHook
	InflectionHook = plugin.InflectionHook
	APIHook        = plugin.APIHook
	CompileHook    = plugin.CompileHook
	RequestHook    = plugin.RequestHook
	PluginRegistry = plugin.Registry
)

// Inflection kinds pdbr asks the naming chain for.
const (
	KindResourcePath     = inflect.KindResourcePath
	KindFieldName        = inflect.KindFieldName
	KindRelationForward  = inflect.KindRelationForward
	KindRelationBackward = inflect.KindRelationBackward
	KindRPCName          = inflect.KindRPCName
	KindComputedField    = inflect.KindComputedField
	KindEnumValue        = inflect.KindEnumValue
)

// Types referenced by hook signatures, the plan and the executor.
type (
	Catalog        = introspect.Catalog
	InflectKind    = coreinflect.Kind
	InflectInput   = coreinflect.Input
	InflectNext    = coreinflect.Next
	APIBuilder     = api.Builder
	Built          = api.Built
	Resource       = api.Resource
	Relation       = api.Relation
	Computed       = api.Computed
	RPC            = api.RPC
	Route          = api.Route
	Plan           = plan.Request
	CompileFunc    = compile.Func
	CompileRequest = compile.Request
	Statement      = compile.Statement
	UserError      = compile.UserError
	Operation      = exec.Operation
	Result         = exec.Result
	Runtime        = server.Runtime
	Problem        = server.Problem
)
