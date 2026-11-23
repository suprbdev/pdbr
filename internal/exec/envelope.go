package exec

import "bytes"

// splitEnvelope splits a wrapper object whose last member is key into meta,
// the object without that member, and the member's value. Only the members
// before key are scanned (for structure, not validity: meta is decoded by
// the caller); value is a subslice of raw, handed on without being read, so
// key must be the last member (the compiler puts the client payload last in
// every wrapper, after at least one metadata member, see compile.ShapePage).
// ok is false when raw is not an object, lacks key or starts with it, and
// the caller decodes the whole wrapper instead.
func splitEnvelope(raw []byte, key string) (meta, value []byte, ok bool) {
	i := skipSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil, nil, false
	}
	depth, comma, expectKey := 0, -1, false
	for ; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			end := stringEnd(raw, i)
			if end < 0 {
				return nil, nil, false
			}
			if depth == 1 && expectKey {
				if string(raw[i+1:end]) == key {
					if comma < 0 {
						return nil, nil, false
					}
					return splitAt(raw, comma, end+1)
				}
				expectKey = false
			}
			i = end
		case '{', '[':
			depth++
			expectKey = depth == 1
		case '}', ']':
			depth--
		case ',':
			if depth == 1 {
				comma, expectKey = i, true
			}
		}
	}
	return nil, nil, false
}

// splitAt cuts raw at the member whose key ends just before colon: meta is
// raw up to comma (the separator before that member) closed with '}', and
// value runs from after the colon to the object's closing brace.
func splitAt(raw []byte, comma, colon int) (meta, value []byte, ok bool) {
	colon = skipSpace(raw, colon)
	if colon >= len(raw) || raw[colon] != ':' {
		return nil, nil, false
	}
	rest := bytes.TrimRight(raw[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[len(rest)-1] != '}' {
		return nil, nil, false
	}
	value = bytes.TrimSpace(rest[:len(rest)-1])
	if len(value) == 0 {
		return nil, nil, false
	}
	meta = make([]byte, 0, comma+1)
	meta = append(append(meta, raw[:comma]...), '}')
	return meta, value, true
}

// stringEnd returns the index of the quote closing the JSON string that
// starts at raw[start], or -1.
func stringEnd(raw []byte, start int) int {
	for j := start + 1; j < len(raw); j++ {
		switch raw[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}

func skipSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}
