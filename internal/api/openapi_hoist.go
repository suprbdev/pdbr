package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// hoistMinSize is the smallest rendered parameter worth replacing by a $ref.
const hoistMinSize = 100

// hoistParameters returns a copy of the document in which every non-path
// parameter that occurs at least twice with identical content is replaced by
// a $ref to components/parameters. The receiver is not modified.
func (d *Document) hoistParameters() (*Document, error) {
	type entry struct {
		key   string
		param *Parameter
		count int
	}
	entries := map[string]*entry{}
	keyOf := map[*Parameter]string{}
	visit := func(ps []*Parameter) error {
		for _, p := range ps {
			if p == nil || p.Ref != "" || p.In == "path" {
				continue
			}
			if _, ok := keyOf[p]; ok {
				entries[keyOf[p]].count++
				continue
			}
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if len(raw) < hoistMinSize {
				continue
			}
			k := string(raw)
			e := entries[k]
			if e == nil {
				e = &entry{key: k, param: p}
				entries[k] = e
			}
			e.count++
			keyOf[p] = k
		}
		return nil
	}
	ops := func(it *PathItem) []*Operation {
		return []*Operation{it.Get, it.Head, it.Post, it.Put, it.Patch, it.Delete}
	}
	for _, pattern := range slices.Sorted(maps.Keys(d.Paths)) {
		it := d.Paths[pattern]
		if err := visit(it.Parameters); err != nil {
			return nil, err
		}
		for _, op := range ops(it) {
			if op != nil {
				if err := visit(op.Parameters); err != nil {
					return nil, err
				}
			}
		}
	}

	variants := map[[2]string]int{}
	shared := slices.Sorted(maps.Keys(entries))
	shared = slices.DeleteFunc(shared, func(k string) bool { return entries[k].count < 2 })
	for _, k := range shared {
		p := entries[k].param
		variants[[2]string{p.In, p.Name}]++
	}
	names := map[string]string{}
	used := map[string]bool{}
	comps := map[string]*Parameter{}
	for _, k := range shared {
		p := entries[k].param
		sum := sha256.Sum256([]byte(k))
		hexSum := hex.EncodeToString(sum[:])
		base := sanitizeComponentName(p.In + "." + p.Name)
		name := base
		if variants[[2]string{p.In, p.Name}] > 1 || name != p.In+"."+p.Name {
			name = base + "." + hexSum[:8]
		}
		for n := 12; used[name] && n <= len(hexSum); n += 4 {
			name = base + "." + hexSum[:n]
		}
		used[name] = true
		names[k] = name
		comps[name] = p
	}

	out := *d
	if len(comps) == 0 {
		return &out, nil
	}
	cc := Components{}
	if d.Components != nil {
		cc = *d.Components
	}
	cc.Parameters = maps.Clone(cc.Parameters)
	if cc.Parameters == nil {
		cc.Parameters = map[string]*Parameter{}
	}
	maps.Copy(cc.Parameters, comps)
	out.Components = &cc

	swap := func(ps []*Parameter) []*Parameter {
		if len(ps) == 0 {
			return ps
		}
		res := make([]*Parameter, len(ps))
		for i, p := range ps {
			res[i] = p
			if k, ok := keyOf[p]; ok {
				if name, ok := names[k]; ok {
					res[i] = &Parameter{Ref: "#/components/parameters/" + name}
				}
			}
		}
		return res
	}
	out.Paths = make(map[string]*PathItem, len(d.Paths))
	for pattern, it := range d.Paths {
		ni := *it
		ni.Parameters = swap(it.Parameters)
		for _, slot := range []**Operation{&ni.Get, &ni.Head, &ni.Post, &ni.Put, &ni.Patch, &ni.Delete} {
			if *slot == nil {
				continue
			}
			no := **slot
			no.Parameters = swap(no.Parameters)
			*slot = &no
		}
		out.Paths[pattern] = &ni
	}
	return &out, nil
}

func sanitizeComponentName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
