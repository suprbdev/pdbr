package exec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
)

// BenchmarkUnwrapPage unwraps a keyset page of rows/op rows in the layout
// the compiler emits (rows last).
func BenchmarkUnwrapPage(b *testing.B) {
	bt := built(b)
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = fmt.Sprintf(`{"id" : %d, "title" : "Post number %d", "body" : "lorem ipsum dolor sit amet, consectetur adipiscing elit", "published" : true}`, i+1, i)
			}
			raw := []byte(fmt.Sprintf(`{"has_next" : true, "has_prev" : false, "first" : [1], "last" : [%d], "n" : %d, "rows" : [%s]}`, n, n, strings.Join(rows, ", ")))
			op := listOp(bt, "/posts")
			op.RawQuery = "limit=1000"
			stmt := &compile.Statement{Shape: compile.ShapePage, Cursors: true}
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Unwrap(op, stmt, raw, true, "/api"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
