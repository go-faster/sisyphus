-- Tune autovacuum and fillfactor for the two tables that churn.
--
-- Neither setting can be expressed in the ent schema, so this is hand-written
-- (see internal/ent/migrate/CLAUDE.md) and carries no schema change of its own.
--
-- queue_jobs takes at least two UPDATEs per job — claim, then ack or nack — and
-- retention now deletes settled rows on top of that. Postgres's default
-- autovacuum_vacuum_scale_factor of 0.2 waits for 20% of the table to be dead
-- before it vacuums, which on a churn table means the dead tuples and the index
-- bloat they carry outlive the rows themselves. 0.02 vacuums ten times sooner,
-- for a table small enough that the scan is cheap.
--
-- fillfactor leaves free space on each page so a claim or an ack can write its
-- new row version on the same page as the old one (a HOT update), which updates
-- no index at all. At the default 100 there is no room, so every claim writes an
-- index entry it will then have to vacuum.
ALTER TABLE queue_jobs SET (
	fillfactor = 90,
	autovacuum_vacuum_scale_factor = 0.02,
	autovacuum_analyze_scale_factor = 0.02
);

-- notifications churns less — one row per delivery, updated once when it
-- settles rather than per claim — so it gets the vacuum threshold but keeps the
-- default fillfactor: its rows are mostly read after settling, and the wasted
-- page space would cost more than the HOT updates save.
ALTER TABLE notifications SET (
	autovacuum_vacuum_scale_factor = 0.05,
	autovacuum_analyze_scale_factor = 0.05
);
