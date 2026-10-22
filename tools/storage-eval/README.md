# SQLite driver evaluation

This nested Go module keeps evaluation-only SQLite drivers out of the Server and
Agent dependency graph. It compares the CGO-based `mattn/go-sqlite3` driver with
the CGo-free `modernc.org/sqlite` driver using local synthetic metric batches.
Behavior tests check transaction rollback, atomic commit, and batch-ID replay
handling against a small fixture schema; the fixture is not a production schema.

Run from this directory:

```sh
go test ./...
go test -run '^$' -bench 'BenchmarkSQLite' -benchmem -benchtime=2s -count=3
```

The benchmark creates temporary databases under this module directory and
removes them after each run. Results are host-specific and are not production
capacity estimates. Target architecture builds, filesystem behavior, actual
batch shape, retention, storage quotas, and operational recovery still require
separate validation before choosing a runtime driver.
