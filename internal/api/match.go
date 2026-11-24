package api

import (
	"net/url"
	"strings"
)

// Match is the result of resolving a request path against the router table.
type Match struct {
	Route *Route
	// Key is the raw (still percent-encoded) primary-key segment of a row
	// route: "1" or "3,x%2Cy" for composite keys.
	Key string
	// Cols / Vals are the raw /by/{cols}/{vals} segments of a unique route;
	// the route's Unique constraint is already resolved from Cols.
	Cols string
	Vals string
	// Resource / RPC mirror Route for convenience.
	Resource *Resource
	RPC      *RPC
}

// Match resolves an escaped request path below base_path ("/users/1",
// "/audit/users", "/rpc/search_posts") to a route. Reserved first segments
// (rpc) and configured schema names win over bare aliases; only schemas[0]
// resources answer on the bare form. /{schema}/rpc/{fn} always addresses a
// function, whether or not one exists (a table named rpc has no row path).
// ok is false for an unknown path.
func (b *Built) Match(escapedPath string) (m *Match, ok bool) {
	p := strings.TrimPrefix(escapedPath, "/")
	if p == "" {
		return nil, false
	}
	raw := strings.Split(p, "/")
	segs := make([]string, len(raw))
	for i, s := range raw {
		u, err := url.PathUnescape(s)
		if err != nil {
			return nil, false
		}
		segs[i] = u
	}
	switch {
	case segs[0] == "rpc":
		if len(segs) != 2 {
			return nil, false
		}
		return b.rpcMatch(b.bareRPC[segs[1]])
	case b.schemas[segs[0]]:
		rest := segs[1:]
		if len(rest) == 0 {
			return nil, false
		}
		if rest[0] == "rpc" && len(rest) == 2 {
			return b.rpcMatch(b.qualRPC[segs[0]][rest[1]])
		}
		return b.resourceMatch(b.qualified[segs[0]][rest[0]], rest[1:], raw[2:])
	default:
		return b.resourceMatch(b.bare[segs[0]], segs[1:], raw[1:])
	}
}

func (b *Built) rpcMatch(rpc *RPC) (*Match, bool) {
	if rpc == nil {
		return nil, false
	}
	rt := b.byRPC[rpc]
	if rt == nil {
		return nil, false
	}
	return &Match{Route: rt, RPC: rpc}, true
}

func (b *Built) resourceMatch(r *Resource, rest, raw []string) (*Match, bool) {
	if r == nil {
		return nil, false
	}
	switch len(rest) {
	case 0:
		if rt := b.byResource[r][RouteList]; rt != nil {
			return &Match{Route: rt, Resource: r}, true
		}
	case 1:
		if rt := b.byResource[r][RouteRow]; rt != nil {
			return &Match{Route: rt, Resource: r, Key: raw[0]}, true
		}
	case 3:
		if rest[0] != "by" {
			return nil, false
		}
		cols := strings.Split(rest[1], ",")
		pgCols := make([]string, len(cols))
		for i, k := range cols {
			f := r.Field(k)
			if f == nil {
				return nil, false
			}
			pgCols[i] = f.Column.Name
		}
		u := r.UniqueFor(pgCols)
		if u == nil {
			return nil, false
		}
		if rt := b.byUnique[r][u.Name]; rt != nil {
			return &Match{Route: rt, Resource: r, Cols: raw[1], Vals: raw[2]}, true
		}
	}
	return nil, false
}
