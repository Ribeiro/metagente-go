//go:build !nosqlite

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// courierSource packs a page of rows the way the Extractor of the asynchronous ELT does, and unpacks it the
// way the Worker does: the rows travel as a compressed text with the hash of the text before compression.
const courierSource = `agent Courier
  goal "Pack a batch and unpack it again"
  tool orders from sql "orders-db"
  tool codec
  accepts go start
  on go
    page = orders.page after: 0 size: 4
    rows = codec.table rows: page columns: ["id", "customer"]
    body = codec.json value: rows
    sum = codec.sha256 text: body
    payload = codec.gzip text: body
    event = codec.record v: 1 seq: 7 columns: ["id", "customer"] payload: payload sha256: sum
    wire = codec.json value: event
    received = codec.parse text: wire
    unpacked = codec.gunzip text: received.payload
    again = codec.sha256 text: unpacked
    if again is not received.sha256
      fail "the hash does not match"
    back = codec.parse text: unpacked
    records = codec.records rows: back columns: received.columns
    n = 0
    for r in records
      n = r.id
    reply "seq {received.seq}; last id {n}; hash ok"
`

func TestRowsTravelPackedAndComeOutTheSame(t *testing.T) {
	_, dir, _ := sqlProject(t, ordersToml)
	file := filepath.Join(dir, "courier.ag")
	toml := `
[sql.orders-db]
driver = "sqlite"
path = "orders.db"

[sql.orders-db.statements]
page = "SELECT id, customer FROM orders WHERE id > :after ORDER BY id LIMIT :size"
`
	rt := sqlRuntime(t, dir, toml, filepath.Join(t.TempDir(), "approvals"))
	if err := os.WriteFile(file, []byte(courierSource), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: approveAll})
	if err != nil || got.Text != "seq 7; last id 4; hash ok" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
}
