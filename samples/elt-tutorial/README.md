# The tutorial of the asynchronous ELT

The files of [`docs/tutorial-elt.md`](../../docs/tutorial-elt.md): an Extractor and a Worker that are one line each,
because everything they do comes from the description `[elt.orders]` in `extractor.toml` and `worker.toml`.

| File | What it is |
|---|---|
| `extractor.ag`, `worker.ag` | the two agents: `tool orders from elt "orders" extract` and `... load` |
| `extractor.toml`, `worker.toml` | the places (databases, broker) and the description of the copy of `orders` |
| `migrations/source.sql`, `migrations/outbox.sql` | the demo source (2,500 orders) and the outbox of the Extractor |
| `migrations/control.sql`, `migrations/orders.sql` | the destination in SQLite: the control tables, then staging and the final table |
| `migrations/control.postgres.sql`, `migrations/orders.postgres.sql` | the same for PostgreSQL |

A test (`internal/consume/elt_tutorial_test.go`) runs these files at every change, with a broker in memory, and
`integration/elt_test.go` runs them against a real JetStream server and a real PostgreSQL. For the step with a language
model, the sweeper, and SQL Server and Oracle as the destination, see [`samples/async-elt`](../async-elt/README.md),
whose agents are written step by step.
