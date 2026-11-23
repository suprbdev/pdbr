package compile

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderGolden renders a compiled statement in the golden / --print-sql
// format (docs/cli.md):
//
//	-- request: METHOD PATH
//	<SQL>
//	-- args: [json]
func RenderGolden(method, path string, stmt *Statement) string {
	args, err := json.Marshal(stmt.Args)
	if err != nil {
		args = []byte("null")
	}
	if stmt.Args == nil {
		args = []byte("[]")
	}
	return fmt.Sprintf("-- request: %s %s\n%s\n-- args: %s\n", method, path, strings.ReplaceAll(stmt.SQL, guardToken, guardMask), args)
}
