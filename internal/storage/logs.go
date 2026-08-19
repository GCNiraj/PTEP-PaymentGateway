package storage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	logQueueBufferSize           = 500
	logRetryInterval             = 10 * time.Second
	logFallbackReplayInterval    = 30 * time.Second
	logMaintenanceInterval       = 6 * time.Hour
	logInternalRetentionInterval = 365 * 24 * time.Hour
	logBusinessRetentionInterval = 7 * 365 * 24 * time.Hour
	logSchemaEnsureTimeout       = 30 * time.Minute
)

var errLogRepositoryClosed = errors.New("log repository closed")

// EnsureLogsSchema creates the api_logs table if it does not already exist.
// Why needed: guarantees the table exists via plain SQL independent of GORM AutoMigrate.
// Called from: main() after storage.Connect.
func EnsureLogsSchema(db *DB) error {
	if db == nil || db.Conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), logSchemaEnsureTimeout)
	defer cancel()
	if err := ensureAPILogPartitionRegistry(ctx, db.Conn); err != nil {
		return fmt.Errorf("ensure logs partition registry: %w", err)
	}
	exists, err := tableExists(ctx, db.Conn, "api_logs")
	if err != nil {
		return fmt.Errorf("ensure logs schema exists check: %w", err)
	}
	if !exists {
		if err := createPartitionedAPILogTable(ctx, db.Conn); err != nil {
			return fmt.Errorf("ensure logs schema create partitioned table: %w", err)
		}
	} else {
		partitioned, err := isPartitionedTable(ctx, db.Conn, "api_logs")
		if err != nil {
			return fmt.Errorf("ensure logs schema partitioned check: %w", err)
		}
		if !partitioned {
			if err := migrateUnpartitionedAPILogs(ctx, db.Conn); err != nil {
				return fmt.Errorf("ensure logs schema migrate partitioned: %w", err)
			}
		}
	}
	if err := ensureAPILogParentShape(ctx, db.Conn); err != nil {
		return fmt.Errorf("ensure logs schema parent shape: %w", err)
	}
	if err := ensureAPILogSubpartitionRoots(ctx, db.Conn); err != nil {
		return fmt.Errorf("ensure logs schema subpartition roots: %w", err)
	}
	now := time.Now().UTC()
	if err := ensureAPILogMonthlyPartitions(ctx, db.Conn, monthStartsBetween(monthStartUTC(now), monthStartUTC(now.AddDate(0, 1, 0)))); err != nil {
		return fmt.Errorf("ensure logs schema monthly partitions: %w", err)
	}
	if err := syncAPILogSequence(ctx, db.Conn); err != nil {
		return fmt.Errorf("ensure logs schema sequence sync: %w", err)
	}
	return nil
}

type apiLogPartitionSpec struct {
	Surface         string
	RootTable       string
	LeafTable       string
	MonthStart      time.Time
	MonthEnd        time.Time
	AutoDrop        bool
	ArchiveRequired bool
}

func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name = $1
		)
	`, table).Scan(&exists)
	return exists, err
}

func isPartitionedTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var partitioned bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_partitioned_table p
			JOIN pg_class c ON c.oid = p.partrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			  AND c.relname = $1
		)
	`, table).Scan(&partitioned)
	return partitioned, err
}

func createPartitionedAPILogTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS api_logs_id_seq`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS api_logs (
			id            BIGINT NOT NULL DEFAULT nextval('api_logs_id_seq'),
			method        TEXT NOT NULL DEFAULT '',
			path          TEXT NOT NULL DEFAULT '',
			status        INTEGER NOT NULL DEFAULT 0,
			duration_ms   BIGINT NOT NULL DEFAULT 0,
			ip            TEXT NOT NULL DEFAULT '',
			request_id    TEXT NOT NULL DEFAULT '',
			actor_id      TEXT NOT NULL DEFAULT '',
			api_surface   TEXT NOT NULL DEFAULT '',
			route         TEXT NOT NULL DEFAULT '',
			auth_result   TEXT NOT NULL DEFAULT '',
			request_body  TEXT NOT NULL DEFAULT '',
			response_body TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		) PARTITION BY LIST (api_surface)
	`); err != nil {
		return err
	}
	return nil
}

func ensureAPILogParentShape(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE SEQUENCE IF NOT EXISTS api_logs_id_seq`,
		`ALTER TABLE api_logs ADD COLUMN IF NOT EXISTS actor_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_logs ADD COLUMN IF NOT EXISTS api_surface TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_logs ADD COLUMN IF NOT EXISTS route TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_logs ADD COLUMN IF NOT EXISTS auth_result TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN id SET DEFAULT nextval('api_logs_id_seq')`,
		`ALTER TABLE api_logs ALTER COLUMN method SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN path SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN status SET DEFAULT 0`,
		`ALTER TABLE api_logs ALTER COLUMN duration_ms SET DEFAULT 0`,
		`ALTER TABLE api_logs ALTER COLUMN ip SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN request_id SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN actor_id SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN api_surface SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN route SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN auth_result SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN request_body SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN response_body SET DEFAULT ''`,
		`ALTER TABLE api_logs ALTER COLUMN created_at SET DEFAULT now()`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := ensureAPILogCreatedAtIndex(ctx, db); err != nil {
		return err
	}
	return nil
}

func ensureAPILogCreatedAtIndex(ctx context.Context, db *sql.DB) error {
	// During migration from a legacy unpartitioned table, the old table may retain an
	// index named "api_logs_created_at_idx" after it is renamed. Using CREATE INDEX IF NOT
	// EXISTS with the same name would no-op and leave the new api_logs parent without the
	// intended index. Check relation-specific indexes instead of relying on name-only IF NOT EXISTS.
	var hasIndex bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_index i
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			WHERE n.nspname = current_schema()
			  AND t.relname = 'api_logs'
			  AND pg_get_indexdef(i.indexrelid) ILIKE '%created_at%'
		)
	`).Scan(&hasIndex)
	if err != nil {
		return err
	}
	if hasIndex {
		return nil
	}
	_, err = db.ExecContext(ctx, `CREATE INDEX api_logs_created_at_parent_idx ON api_logs (created_at DESC)`)
	return err
}

func ensureAPILogSubpartitionRoots(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS api_logs_business PARTITION OF api_logs FOR VALUES IN ('business') PARTITION BY RANGE (created_at)`,
		`CREATE TABLE IF NOT EXISTS api_logs_internal PARTITION OF api_logs FOR VALUES IN ('internal') PARTITION BY RANGE (created_at)`,
		`CREATE TABLE IF NOT EXISTS api_logs_unknown PARTITION OF api_logs DEFAULT PARTITION BY RANGE (created_at)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func ensureAPILogPartitionRegistry(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS api_log_partition_registry (
			partition_name   TEXT PRIMARY KEY,
			surface          TEXT NOT NULL,
			root_table       TEXT NOT NULL,
			month_start      DATE NOT NULL,
			month_end        DATE NOT NULL,
			auto_drop        BOOLEAN NOT NULL DEFAULT false,
			archive_required BOOLEAN NOT NULL DEFAULT false,
			archived_at      TIMESTAMPTZ NULL,
			dropped_at       TIMESTAMPTZ NULL,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS api_log_partition_registry_surface_month_idx ON api_log_partition_registry (surface, month_start DESC)`,
		`CREATE INDEX IF NOT EXISTS api_log_partition_registry_drop_idx ON api_log_partition_registry (auto_drop, dropped_at, month_end)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func migrateUnpartitionedAPILogs(ctx context.Context, db *sql.DB) error {
	legacyName := uniqueLegacyAPILogTableName()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, quoteIdent("api_logs"), quoteIdent(legacyName))); err != nil {
		return err
	}
	log.Printf("api_logs partition migration: renamed legacy table to %s", legacyName)

	for _, stmt := range []string{
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS actor_id TEXT NOT NULL DEFAULT ''`, quoteIdent(legacyName)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS api_surface TEXT NOT NULL DEFAULT ''`, quoteIdent(legacyName)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS route TEXT NOT NULL DEFAULT ''`, quoteIdent(legacyName)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS auth_result TEXT NOT NULL DEFAULT ''`, quoteIdent(legacyName)),
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if err := createPartitionedAPILogTable(ctx, db); err != nil {
		return err
	}
	if err := ensureAPILogParentShape(ctx, db); err != nil {
		return err
	}
	if err := ensureAPILogSubpartitionRoots(ctx, db); err != nil {
		return err
	}
	if err := ensureAPILogPartitionRegistry(ctx, db); err != nil {
		return err
	}

	var minCreated, maxCreated sql.NullTime
	// #nosec G201 -- legacyName is an internally generated table identifier escaped via quoteIdent.
	rangeQuery := fmt.Sprintf(`SELECT min(created_at), max(created_at) FROM %s`, quoteIdent(legacyName))
	if err := db.QueryRowContext(ctx, rangeQuery).Scan(&minCreated, &maxCreated); err != nil {
		return err
	}
	now := time.Now().UTC()
	months := monthStartsBetween(monthStartUTC(now), monthStartUTC(now.AddDate(0, 1, 0)))
	if minCreated.Valid && maxCreated.Valid {
		months = append(months, monthStartsBetween(monthStartUTC(minCreated.Time), monthStartUTC(maxCreated.Time))...)
		months = uniqueMonthStarts(months)
	}
	if err := ensureAPILogMonthlyPartitions(ctx, db, months); err != nil {
		return err
	}

	// #nosec G201 -- legacyName is an internally generated table identifier escaped via quoteIdent.
	copyQuery := fmt.Sprintf(`
		INSERT INTO api_logs (
			id, method, path, status, duration_ms, ip, request_id,
			actor_id, api_surface, route, auth_result,
			request_body, response_body, created_at
		)
		SELECT
			id,
			COALESCE(method, ''),
			COALESCE(path, ''),
			COALESCE(status, 0),
			COALESCE(duration_ms, 0),
			COALESCE(ip, ''),
			COALESCE(request_id, ''),
			COALESCE(actor_id, ''),
			COALESCE(NULLIF(api_surface, ''), CASE
				WHEN path LIKE '/api/business/%%' OR path LIKE '/api/v1/%%' THEN 'business'
				WHEN path LIKE '/api/admin/%%' OR path LIKE '/api/dkpg/%%' OR path LIKE '/api/_internal/%%' THEN 'internal'
				ELSE 'unknown'
			END),
			COALESCE(NULLIF(route, ''), COALESCE(path, '')),
			COALESCE(NULLIF(auth_result, ''), CASE
				WHEN status = 401 THEN 'unauthenticated'
				WHEN status = 403 THEN 'forbidden'
				WHEN COALESCE(NULLIF(actor_id, ''), '') <> '' THEN 'authenticated'
				ELSE ''
			END),
			COALESCE(request_body, ''),
			COALESCE(response_body, ''),
			COALESCE(created_at, now())
		FROM %s
	`, quoteIdent(legacyName))
	if _, err := db.ExecContext(ctx, copyQuery); err != nil {
		return err
	}
	if err := syncAPILogSequence(ctx, db); err != nil {
		return err
	}

	log.Printf("api_logs partition migration completed; legacy backup retained as %s", legacyName)
	return nil
}

func syncAPILogSequence(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS api_logs_id_seq`); err != nil {
		return err
	}
	var maxID sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(id) FROM api_logs`).Scan(&maxID); err != nil {
		return err
	}
	nextVal := int64(1)
	isCalled := false
	if maxID.Valid && maxID.Int64 > 0 {
		nextVal = maxID.Int64
		isCalled = true
	}
	if _, err := db.ExecContext(ctx, `SELECT setval('api_logs_id_seq', $1, $2)`, nextVal, isCalled); err != nil {
		return err
	}
	return nil
}

func ensureAPILogMonthlyPartitions(ctx context.Context, db *sql.DB, months []time.Time) error {
	months = uniqueMonthStarts(months)
	for _, month := range months {
		for _, surface := range []string{"business", "internal", "unknown"} {
			spec := makeAPILogPartitionSpec(surface, month)
			// #nosec G201 -- partition names are internal and escaped via quoteIdent; timestamp bounds are generated server-side.
			ddl := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				quoteIdent(spec.LeafTable),
				quoteIdent(spec.RootTable),
				spec.MonthStart.Format(time.RFC3339),
				spec.MonthEnd.Format(time.RFC3339),
			)
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				return err
			}
			if err := upsertAPILogPartitionRegistry(ctx, db, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

func upsertAPILogPartitionRegistry(ctx context.Context, db *sql.DB, spec apiLogPartitionSpec) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO api_log_partition_registry (
			partition_name, surface, root_table, month_start, month_end,
			auto_drop, archive_required, dropped_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,now())
		ON CONFLICT (partition_name)
		DO UPDATE SET
			surface = EXCLUDED.surface,
			root_table = EXCLUDED.root_table,
			month_start = EXCLUDED.month_start,
			month_end = EXCLUDED.month_end,
			auto_drop = EXCLUDED.auto_drop,
			archive_required = EXCLUDED.archive_required,
			updated_at = now()
	`,
		spec.LeafTable,
		spec.Surface,
		spec.RootTable,
		spec.MonthStart.Format("2006-01-02"),
		spec.MonthEnd.Format("2006-01-02"),
		spec.AutoDrop,
		spec.ArchiveRequired,
	)
	return err
}

func makeAPILogPartitionSpec(surface string, month time.Time) apiLogPartitionSpec {
	month = monthStartUTC(month)
	surface = normalizeAPILogSurface(surface)
	root := "api_logs_unknown"
	autoDrop := false
	archiveRequired := false
	switch surface {
	case "business":
		root = "api_logs_business"
		archiveRequired = true
	case "internal":
		root = "api_logs_internal"
		autoDrop = true
	case "unknown":
		root = "api_logs_unknown"
	}
	return apiLogPartitionSpec{
		Surface:         surface,
		RootTable:       root,
		LeafTable:       fmt.Sprintf("api_logs_%s_%04d%02d", surface, month.Year(), int(month.Month())),
		MonthStart:      month,
		MonthEnd:        month.AddDate(0, 1, 0),
		AutoDrop:        autoDrop,
		ArchiveRequired: archiveRequired,
	}
}

func normalizeAPILogSurface(surface string) string {
	switch strings.TrimSpace(strings.ToLower(surface)) {
	case "business":
		return "business"
	case "internal":
		return "internal"
	default:
		return "unknown"
	}
}

func monthStartUTC(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthStartsBetween(from, to time.Time) []time.Time {
	from = monthStartUTC(from)
	to = monthStartUTC(to)
	if to.Before(from) {
		from, to = to, from
	}
	var out []time.Time
	for t := from; !t.After(to); t = t.AddDate(0, 1, 0) {
		out = append(out, t)
	}
	return out
}

func uniqueMonthStarts(in []time.Time) []time.Time {
	seen := make(map[string]time.Time, len(in))
	for _, t := range in {
		m := monthStartUTC(t)
		seen[m.Format("2006-01")] = m
	}
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	// keys are YYYY-MM so lexical sort is chronological.
	sort.Strings(keys)
	out := make([]time.Time, 0, len(keys))
	for _, k := range keys {
		out = append(out, seen[k])
	}
	return out
}

func uniqueLegacyAPILogTableName() string {
	return "api_logs_legacy_unpartitioned_" + time.Now().UTC().Format("20060102150405")
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type LogEntry struct {
	ID           int64  `json:"id"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	Duration     int64  `json:"duration_ms"`
	IP           string `json:"ip"`
	RequestID    string `json:"request_id"`
	ActorID      string `json:"actor_id"`
	APISurface   string `json:"api_surface"`
	Route        string `json:"route"`
	AuthResult   string `json:"auth_result"`
	RequestBody  string `json:"request_body"`
	ResponseBody string `json:"response_body"`
	CreatedAt    string `json:"created_at"`
}

type logWriteTier int

const (
	logWriteTierT1 logWriteTier = iota
	logWriteTierT2
)

type queuedLogRecord struct {
	Entry     LogEntry  `json:"entry"`
	ReqBody   string    `json:"req_body"`
	ResBody   string    `json:"res_body"`
	CreatedAt time.Time `json:"created_at"`
}

type LogRepository struct {
	DB *sql.DB

	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup
	stopCh    chan struct{}

	t1Queue chan queuedLogRecord
	t2Queue chan queuedLogRecord

	fallbackPath string
	fallbackMu   sync.Mutex

	droppedLogsTotal atomic.Uint64
	closed           atomic.Bool

	partitionWarned atomic.Bool
}

// NewLogRepository constructs log repository from shared DB wrapper.
// Called from: main() for logging middleware and logs controller.
func NewLogRepository(db *DB) *LogRepository {
	r := &LogRepository{
		fallbackPath: filepath.Join(os.TempDir(), "dk_api_logs_t1_fallback.jsonl"),
	}
	if db != nil {
		r.DB = db.Conn
	}
	if r.Enabled() {
		r.startBackground()
	}
	return r
}

// Enabled reports whether repository can execute SQL operations.
// Called from: middleware/controller guards before DB access.
func (r *LogRepository) Enabled() bool {
	return r != nil && r.DB != nil
}

// DroppedLogsTotal returns the number of T2 log entries dropped due to queue pressure or DB errors.
func (r *LogRepository) DroppedLogsTotal() uint64 {
	if r == nil {
		return 0
	}
	return r.droppedLogsTotal.Load()
}

// Close stops background log workers and attempts a best-effort drain.
func (r *LogRepository) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() {
		r.closed.Store(true)
		if r.stopCh != nil {
			close(r.stopCh)
		}
	})
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (r *LogRepository) startBackground() {
	if !r.Enabled() {
		return
	}
	r.startOnce.Do(func() {
		r.stopCh = make(chan struct{})
		r.t1Queue = make(chan queuedLogRecord, logQueueBufferSize)
		r.t2Queue = make(chan queuedLogRecord, logQueueBufferSize)

		r.wg.Add(3)
		go r.runT1Worker()
		go r.runT2Worker()
		go r.runMaintenanceWorker()
	})
}

// Insert stores one API log row into api_logs.
// Why needed: request/response tracing for auditing and debugging.
// Called from: middleware.RequestLogger after each request.
// Behavior: asynchronous enqueue by default; DB writes happen in background workers.
func (r *LogRepository) Insert(ctx context.Context, entry LogEntry, reqBody, resBody string) error {
	if !r.Enabled() {
		return nil
	}
	if r.closed.Load() {
		return errLogRepositoryClosed
	}
	r.startBackground()

	rec := queuedLogRecord{
		Entry:     entry,
		ReqBody:   reqBody,
		ResBody:   resBody,
		CreatedAt: time.Now().UTC(),
	}
	return r.enqueue(ctx, rec)
}

func (r *LogRepository) enqueue(ctx context.Context, rec queuedLogRecord) error {
	switch classifyLogTier(rec.Entry) {
	case logWriteTierT1:
		select {
		case r.t1Queue <- rec:
			return nil
		default:
			// Never silently drop T1; spill to append-only fallback file.
			if err := r.appendToFallback(rec, "t1_queue_full"); err != nil {
				log.Printf("request logger t1 fallback append failed: %v", err)
				return err
			}
			return nil
		}
	default:
		select {
		case r.t2Queue <- rec:
			return nil
		default:
			r.recordT2Drop(rec, "t2_queue_full")
			return nil
		case <-ctxDone(ctx):
			r.recordT2Drop(rec, "request_context_done")
			return nil
		}
	}
}

func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

func classifyLogTier(entry LogEntry) logWriteTier {
	surface := strings.TrimSpace(strings.ToLower(entry.APISurface))
	if surface == "business" {
		return logWriteTierT1
	}
	path := strings.TrimSpace(strings.ToLower(entry.Path))
	route := strings.TrimSpace(strings.ToLower(entry.Route))
	if strings.HasPrefix(route, "/api/business/") || strings.HasPrefix(route, "/api/v1/") ||
		strings.HasPrefix(path, "/api/business/") || strings.HasPrefix(path, "/api/v1/") {
		return logWriteTierT1
	}
	return logWriteTierT2
}

func (r *LogRepository) runT1Worker() {
	defer r.wg.Done()

	replayTicker := time.NewTicker(logFallbackReplayInterval)
	defer replayTicker.Stop()

	// Replay any residual fallback file on startup.
	r.replayFallback()

	for {
		select {
		case rec := <-r.t1Queue:
			if err := r.insertT1WithRetry(rec); err != nil {
				log.Printf("request logger t1 insert failed: %v", err)
				if fbErr := r.appendToFallback(rec, "t1_insert_failed"); fbErr != nil {
					log.Printf("request logger t1 fallback append failed after insert error: %v", fbErr)
				}
			}
		case <-replayTicker.C:
			r.replayFallback()
		case <-r.stopCh:
			r.drainT1Queue()
			r.replayFallback()
			return
		}
	}
}

func (r *LogRepository) drainT1Queue() {
	for {
		select {
		case rec := <-r.t1Queue:
			if err := r.insertT1WithRetry(rec); err != nil {
				log.Printf("request logger t1 insert failed during drain: %v", err)
				if fbErr := r.appendToFallback(rec, "t1_drain_insert_failed"); fbErr != nil {
					log.Printf("request logger t1 fallback append failed during drain: %v", fbErr)
				}
			}
		default:
			return
		}
	}
}

func (r *LogRepository) insertT1WithRetry(rec queuedLogRecord) error {
	if err := r.insertSync(context.Background(), rec); err == nil {
		return nil
	} else {
		timer := time.NewTimer(logRetryInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.stopCh:
			// Continue and try one last time during shutdown.
		}
		if err2 := r.insertSync(context.Background(), rec); err2 == nil {
			return nil
		} else {
			return fmt.Errorf("t1 insert retry failed: %w", err2)
		}
	}
}

func (r *LogRepository) runT2Worker() {
	defer r.wg.Done()
	for {
		select {
		case rec := <-r.t2Queue:
			if err := r.insertSync(context.Background(), rec); err != nil {
				r.recordT2Drop(rec, "t2_insert_failed")
				log.Printf("request logger t2 insert failed (dropped): %v", err)
			}
		case <-r.stopCh:
			r.drainT2Queue()
			return
		}
	}
}

func (r *LogRepository) drainT2Queue() {
	for {
		select {
		case rec := <-r.t2Queue:
			if err := r.insertSync(context.Background(), rec); err != nil {
				r.recordT2Drop(rec, "t2_drain_insert_failed")
				log.Printf("request logger t2 insert failed during drain (dropped): %v", err)
			}
		default:
			return
		}
	}
}

func (r *LogRepository) runMaintenanceWorker() {
	defer r.wg.Done()

	// Run once on startup.
	r.runRetentionMaintenance()
	r.runPartitionMaintenance()

	ticker := time.NewTicker(logMaintenanceInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.runRetentionMaintenance()
			r.runPartitionMaintenance()
		case <-r.stopCh:
			return
		}
	}
}

func (r *LogRepository) runRetentionMaintenance() {
	if !r.Enabled() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Remove internal-surface logs older than 12 months.
	if res, err := r.DB.ExecContext(ctx, `
		DELETE FROM api_logs
		WHERE created_at < $1
		  AND api_surface = 'internal'
	`, time.Now().UTC().Add(-logInternalRetentionInterval)); err != nil {
		log.Printf("api_logs retention cleanup (internal) failed: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("api_logs retention cleanup: deleted %d internal rows older than 12 months", n)
	}

	// One-time style cleanup for legacy noise routes that should no longer be logged.
	if res, err := r.DB.ExecContext(ctx, `
		DELETE FROM api_logs
		WHERE path IN ('/api/health', '/api/hello', '/dashboard', '/login', '/')
		   OR path LIKE '/api/debug/%'
	`); err != nil {
		log.Printf("api_logs legacy noise cleanup failed: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("api_logs legacy noise cleanup: deleted %d rows", n)
	}

	// Business retention is 7 years, but archival is required before deletion.
	// This code intentionally does not auto-delete business rows.
	_ = logBusinessRetentionInterval
}

func (r *LogRepository) runPartitionMaintenance() {
	if !r.Enabled() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	isPartitioned, err := isPartitionedTable(ctx, r.DB, "api_logs")
	if err != nil {
		if !r.partitionWarned.Swap(true) {
			log.Printf("api_logs partition maintenance check failed: %v", err)
		}
		return
	}
	if !isPartitioned && !r.partitionWarned.Swap(true) {
		log.Printf("api_logs partition maintenance: table is not partitioned; using row-based retention cleanup only")
		return
	}
	now := time.Now().UTC()
	if err := ensureAPILogMonthlyPartitions(ctx, r.DB, monthStartsBetween(monthStartUTC(now), monthStartUTC(now.AddDate(0, 1, 0)))); err != nil {
		log.Printf("api_logs partition maintenance ensure future partitions failed: %v", err)
		return
	}
	if err := dropExpiredInternalAPILogPartitions(ctx, r.DB, now.Add(-logInternalRetentionInterval)); err != nil {
		log.Printf("api_logs partition maintenance drop expired internal partitions failed: %v", err)
	}
}

func dropExpiredInternalAPILogPartitions(ctx context.Context, db *sql.DB, cutoff time.Time) error {
	rows, err := db.QueryContext(ctx, `
		SELECT partition_name
		FROM api_log_partition_registry
		WHERE surface = 'internal'
		  AND auto_drop = true
		  AND dropped_at IS NULL
		  AND month_end < $1::date
		ORDER BY month_start ASC
	`, cutoff.UTC().Format("2006-01-02"))
	if err != nil {
		return err
	}
	defer rows.Close()

	var partitions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		partitions = append(partitions, strings.TrimSpace(name))
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range partitions {
		if name == "" {
			continue
		}
		dropSQL := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quoteIdent(name))
		if _, err := db.ExecContext(ctx, dropSQL); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE api_log_partition_registry
			SET dropped_at = now(), updated_at = now()
			WHERE partition_name = $1
		`, name); err != nil {
			return err
		}
		log.Printf("api_logs partition maintenance: dropped expired internal partition %s", name)
	}
	return nil
}

func (r *LogRepository) recordT2Drop(rec queuedLogRecord, reason string) {
	total := r.droppedLogsTotal.Add(1)
	log.Printf("request logger dropped T2 log (reason=%s dropped_logs_total=%d method=%s path=%s request_id=%s)",
		reason,
		total,
		rec.Entry.Method,
		rec.Entry.Path,
		rec.Entry.RequestID,
	)
}

func (r *LogRepository) appendToFallback(rec queuedLogRecord, reason string) error {
	if !r.Enabled() {
		return nil
	}
	r.fallbackMu.Lock()
	defer r.fallbackMu.Unlock()

	// #nosec G301 -- fallback dir permissions intentionally allow owner/group read/execute in existing deployments.
	if err := os.MkdirAll(filepath.Dir(r.fallbackPath), 0o755); err != nil {
		return fmt.Errorf("mkdir fallback dir: %w", err)
	}
	f, err := os.OpenFile(r.fallbackPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open fallback file: %w", err)
	}
	defer f.Close()

	line := struct {
		Reason string          `json:"reason"`
		Record queuedLogRecord `json:"record"`
	}{
		Reason: reason,
		Record: rec,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("marshal fallback line: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("append fallback line: %w", err)
	}
	return nil
}

func (r *LogRepository) replayFallback() {
	if !r.Enabled() {
		return
	}
	r.fallbackMu.Lock()
	defer r.fallbackMu.Unlock()

	f, err := os.Open(r.fallbackPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Printf("request logger fallback replay open failed: %v", err)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 2*1024*1024)

	var pending [][]byte
	var replayed int

	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var payload struct {
			Reason string          `json:"reason"`
			Record queuedLogRecord `json:"record"`
		}
		if err := json.Unmarshal(line, &payload); err != nil {
			log.Printf("request logger fallback replay decode failed: %v", err)
			// Discard invalid lines rather than poison the file forever.
			continue
		}
		if payload.Record.CreatedAt.IsZero() {
			payload.Record.CreatedAt = time.Now().UTC()
		}
		if err := r.insertSync(context.Background(), payload.Record); err != nil {
			pending = append(pending, line)
			continue
		}
		replayed++
	}
	if err := scanner.Err(); err != nil {
		log.Printf("request logger fallback replay scan failed: %v", err)
		return
	}

	switch len(pending) {
	case 0:
		if err := os.Remove(r.fallbackPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("request logger fallback cleanup failed: %v", err)
		}
	default:
		tmpPath := r.fallbackPath + ".tmp"
		// #nosec G304 -- fallbackPath is server-side configured repository state, not request input.
		tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.Printf("request logger fallback rewrite open failed: %v", err)
			return
		}
		for _, line := range pending {
			if _, err := tmp.Write(append(line, '\n')); err != nil {
				_ = tmp.Close()
				log.Printf("request logger fallback rewrite failed: %v", err)
				return
			}
		}
		if err := tmp.Close(); err != nil {
			log.Printf("request logger fallback rewrite close failed: %v", err)
			return
		}
		if err := os.Rename(tmpPath, r.fallbackPath); err != nil {
			log.Printf("request logger fallback rename failed: %v", err)
			return
		}
	}

	if replayed > 0 {
		log.Printf("request logger fallback replayed %d queued T1 log entries", replayed)
	}
}

func (r *LogRepository) insertSync(ctx context.Context, rec queuedLogRecord) error {
	if !r.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	query := `
		INSERT INTO api_logs (
			method, path, status, duration_ms, ip, request_id,
			actor_id, api_surface, route, auth_result,
			request_body, response_body, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`
	_, err := r.DB.ExecContext(ctx, query,
		rec.Entry.Method,
		rec.Entry.Path,
		rec.Entry.Status,
		rec.Entry.Duration,
		rec.Entry.IP,
		rec.Entry.RequestID,
		rec.Entry.ActorID,
		rec.Entry.APISurface,
		rec.Entry.Route,
		rec.Entry.AuthResult,
		rec.ReqBody,
		rec.ResBody,
		createdAt,
	)
	return err
}

// List returns recent API logs with optional filtering by status, method, path, and free-text search.
// Called from: LogsController.List.
func (r *LogRepository) List(ctx context.Context, limit, offset int, status int, method, path, search string) ([]LogEntry, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	args := []any{}
	whereParts := []string{}

	if status > 0 {
		args = append(args, status)
		whereParts = append(whereParts, fmt.Sprintf("status = $%d", len(args)))
	}
	if method != "" {
		args = append(args, strings.ToUpper(strings.TrimSpace(method)))
		whereParts = append(whereParts, fmt.Sprintf("upper(method) = $%d", len(args)))
	}
	if path != "" {
		args = append(args, "%"+strings.TrimSpace(path)+"%")
		whereParts = append(whereParts, fmt.Sprintf("path ilike $%d", len(args)))
	}
	if search != "" {
		s := "%" + strings.TrimSpace(search) + "%"
		args = append(args, s)
		idx := len(args)
		whereParts = append(whereParts, fmt.Sprintf(
			"(path ilike $%d OR route ilike $%d OR ip ilike $%d OR request_id ilike $%d OR actor_id ilike $%d OR api_surface ilike $%d OR auth_result ilike $%d OR request_body ilike $%d OR response_body ilike $%d)",
			idx, idx, idx, idx, idx, idx, idx, idx, idx,
		))
	}

	query := `SELECT id, method, path, status, duration_ms, ip, request_id, actor_id, api_surface, route, auth_result,
			left(coalesce(request_body,''), 500)  AS request_body,
			left(coalesce(response_body,''), 500) AS response_body,
			created_at::text
		FROM api_logs`
	if len(whereParts) > 0 {
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}
	// #nosec G202 -- only placeholders are concatenated; all data values remain parameterized.
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(
			&e.ID,
			&e.Method,
			&e.Path,
			&e.Status,
			&e.Duration,
			&e.IP,
			&e.RequestID,
			&e.ActorID,
			&e.APISurface,
			&e.Route,
			&e.AuthResult,
			&e.RequestBody,
			&e.ResponseBody,
			&e.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
