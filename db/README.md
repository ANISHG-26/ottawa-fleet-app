# PostgreSQL schema and initialization

Fleet owns `db/fleet`; Ride API and the assignment worker share
`db/ride/001_ride_jobs.sql` because rides, idempotency records and jobs commit
as one unit. The migration runner applies ordered SQL files from both folders
and tracks completed files in `app_schema_migrations`. Each migration runs in a
transaction, and `cmd/db-init` is the only command that applies migrations.
Concurrent initializers serialize on a PostgreSQL advisory lock held by one
dedicated connection. The lock covers migration-ledger creation and every
migration; cleanup uses an independent bounded context so cancellation cannot
return a still-locked session to the connection pool.

`cmd/db-init` initializes a durable random dataset epoch, then inserts the six
fixed synthetic vehicles using `FLEET_SEED_TIME` or its current UTC clock. It
does not overwrite existing inventory or reset reservations. A full local reset
removes the named PostgreSQL volume, so the next initialization creates new
schema state, epoch, and seed data.

Fleet inventory stores source `observed_at`, maintenance mode, and battery
percentage. The API derives effective freshness/availability; reads do not
write inventory. The opt-in demo observation feed is a separate source that
updates the five current seeded observations and keeps vehicle 006 stale.

Run initialization from `services/` after PostgreSQL is ready:

```powershell
$env:DATABASE_URL = "postgres://postgres:postgres@localhost:5432/fleet?sslmode=disable"
go run ./cmd/db-init
```
