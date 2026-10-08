package tools

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// callParams are the values a call of the statement gives: its single values, and the list it runs for.
func (st *sqlStatement) callParams() []string {
	if st.each == "" {
		return st.params
	}
	var params []string
	for _, p := range st.params {
		if !contains(st.columns, p) {
			params = append(params, p)
		}
	}
	return append(params, st.each)
}

// params are the values a call of the transaction gives.
func (t *sqlTransaction) params() []string {
	return append(append([]string{}, t.scalars...), t.lists...)
}

// callWrite runs a statement that changes rows, in a transaction of its own: a list that stops halfway
// leaves nothing behind.
func (s *SQL) callWrite(ctx context.Context, st *sqlStatement, args Args) (value.Value, error) {
	if err := s.checkArgs(st.name, st.callParams(), args); err != nil {
		return value.Nothing, err
	}
	n, err := s.inTransaction(ctx, st.name, func(tx *sql.Tx) (value.Value, error) {
		changed, err := s.exec(ctx, tx, st, args)
		return value.Number(float64(changed)), err
	})
	return n, err
}

// callTransaction runs the statements of a transaction in order, as one. It gives a record with how many
// rows each statement changed.
func (s *SQL) callTransaction(ctx context.Context, t *sqlTransaction, args Args) (value.Value, error) {
	if err := s.checkArgs(t.name, t.params(), args); err != nil {
		return value.Nothing, err
	}
	return s.inTransaction(ctx, t.name, func(tx *sql.Tx) (value.Value, error) {
		counts := map[string]value.Value{}
		for _, st := range t.steps {
			changed, err := s.exec(ctx, tx, st, args)
			if err != nil {
				return value.Nothing, err
			}
			before := 0.0
			if old, ok := counts[st.name]; ok {
				before = old.Number
			}
			counts[st.name] = value.Number(before + float64(changed))
		}
		return value.Record(counts), nil
	})
}

// inTransaction opens the database, begins a transaction, and commits it if work gives no problem.
func (s *SQL) inTransaction(ctx context.Context, name string, work func(*sql.Tx) (value.Value, error)) (value.Value, error) {
	db, err := s.database(ctx)
	if err != nil {
		return value.Nothing, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return value.Nothing, s.failure(ctx, &sqlStatement{name: name}, err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := work(tx)
	if err != nil {
		return value.Nothing, err
	}
	if err := tx.Commit(); err != nil {
		return value.Nothing, s.failure(ctx, &sqlStatement{name: name}, err)
	}
	return result, nil
}

// exec runs one statement that changes rows, once, or once for each item of its list, and gives how many
// rows it changed.
func (s *SQL) exec(ctx context.Context, tx *sql.Tx, st *sqlStatement, args Args) (int64, error) {
	if st.each == "" {
		bound, err := s.place(st, func(name string) value.Value { return args[name] })
		if err != nil {
			return 0, err
		}
		return s.affected(ctx, st, func() (sql.Result, error) { return tx.ExecContext(ctx, st.query, bound...) })
	}
	items, err := s.items(st, args[st.each])
	if err != nil {
		return 0, err
	}
	prepared, err := tx.PrepareContext(ctx, st.query)
	if err != nil {
		return 0, s.failure(ctx, st, err)
	}
	defer prepared.Close()
	var total int64
	for i, item := range items {
		bound, err := s.place(st, func(name string) value.Value {
			if at := indexOf(st.columns, name); at >= 0 {
				return item(at)
			}
			return args[name]
		})
		if err != nil {
			return 0, s.atItem(err, i)
		}
		n, err := s.affected(ctx, st, func() (sql.Result, error) { return prepared.ExecContext(ctx, bound...) })
		if err != nil {
			return 0, s.atItem(err, i)
		}
		total += n
	}
	return total, nil
}

// atItem says which item of a list a problem is in. The item is told by its place, never by its content.
func (s *SQL) atItem(err error, i int) error {
	if d, ok := err.(*diag.Diagnostic); ok {
		return d.AddRelated(fmt.Sprintf("it happened at item %d of the list (the first is 1)", i+1))
	}
	return err
}

func (s *SQL) affected(ctx context.Context, st *sqlStatement, run func() (sql.Result, error)) (int64, error) {
	result, err := run()
	if err != nil {
		return 0, s.failure(ctx, st, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, nil // a driver that does not count gives no count; the statement did run
	}
	return n, nil
}

// items checks the list of a call and gives, for each item, a function that returns the value of the
// column at a place of st.columns. An item is a record, or a list in the order of the columns.
func (s *SQL) items(st *sqlStatement, list value.Value) ([]func(int) value.Value, error) {
	if list.Kind != value.KindList {
		return nil, diag.Newf("`%s` in `%s.%s` has to be a list, and it is %s", st.each, s.decl.Name, st.name, list.Describe()).
			Fixf("give a list with one item for each row, with the values %s", strings.Join(st.columns, ", "))
	}
	if len(list.List) > s.limits.MaxSQLWriteRows {
		return nil, diag.Newf("`%s` in `%s.%s` has %d items, and %d is the most one call may have", st.each, s.decl.Name, st.name, len(list.List), s.limits.MaxSQLWriteRows).
			Fix("send the list in parts, or raise max_sql_write_rows in the [limits] section of metagente.toml")
	}
	items := make([]func(int) value.Value, len(list.List))
	for i, item := range list.List {
		switch item.Kind {
		case value.KindRecord:
			for _, column := range st.columns {
				if _, ok := item.Record[column]; !ok {
					return nil, diag.Newf("item %d of `%s` in `%s.%s` has no value called `%s`", i+1, st.each, s.decl.Name, st.name, column).
						Fixf("each item needs: %s", strings.Join(st.columns, ", "))
				}
			}
			items[i] = func(at int) value.Value { return item.Record[st.columns[at]] }
		case value.KindList:
			if len(item.List) != len(st.columns) {
				return nil, diag.Newf("item %d of `%s` in `%s.%s` has %d values, and %d are needed (%s)", i+1, st.each, s.decl.Name, st.name,
					len(item.List), len(st.columns), strings.Join(st.columns, ", ")).
					Fix("give the values in that order")
			}
			items[i] = func(at int) value.Value { return item.List[at] }
		default:
			return nil, diag.Newf("item %d of `%s` in `%s.%s` has to be a record or a list, and it is %s", i+1, st.each, s.decl.Name, st.name, item.Describe()).
				Fixf("each item needs: %s", strings.Join(st.columns, ", "))
		}
	}
	return items, nil
}

func indexOf(list []string, item string) int {
	for i, s := range list {
		if s == item {
			return i
		}
	}
	return -1
}

var quoted = regexp.MustCompile(`'[^']*'|"[^"]*"|` + "`[^`]*`")

// scrubValues takes out what is between quotes in the message of a driver. Some drivers put the value
// that was refused in the message (a key that already exists, a text that is not a number), and the
// rows may hold personal data: a message may be seen and kept, a row may not.
func scrubValues(message string) string {
	return quoted.ReplaceAllString(message, "…")
}
