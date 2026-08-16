package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"qqtrafficbot/internal/traffic"
)

const (
	maxTrafficBytes    = int64(500 << 20)
	targetTrafficBytes = int64(450 << 20)
	retentionDays      = 7
)

var trafficFilePattern = regexp.MustCompile(`^traffic-(\d{4}-\d{2}-\d{2})\.db$`)

type PendingAlert struct {
	ID         int64
	IP         string
	Bytes      uint64
	OccurredAt time.Time
}

type Store struct {
	baseDir string
	loc     *time.Location
	state   *sql.DB
	mu      sync.Mutex
	day     string
	dayDB   *sql.DB
}

func New(baseDir string, loc *time.Location) (*Store, error) {
	absolute, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	state, err := openDB(filepath.Join(absolute, "state.db"))
	if err != nil {
		return nil, err
	}
	s := &Store{baseDir: absolute, loc: loc, state: state}
	if err := s.initState(); err != nil {
		state.Close()
		return nil, err
	}
	return s, nil
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return db, nil
}

func (s *Store) initState() error {
	_, err := s.state.Exec(`
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS snapshot (
    ip TEXT PRIMARY KEY,
    bytes INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS recent (
    ts INTEGER NOT NULL,
    ip TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    PRIMARY KEY (ts, ip)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS recent_ip_ts ON recent(ip, ts);
CREATE TABLE IF NOT EXISTS alert_state (
    ip TEXT PRIMARY KEY,
    last_alert INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pending_alerts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    occurred_at INTEGER NOT NULL,
    sent_at INTEGER
);
CREATE TABLE IF NOT EXISTS seen_messages (
    id TEXT PRIMARY KEY,
    seen_at INTEGER NOT NULL
) WITHOUT ROWID;
`)
	return err
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.dayDB != nil {
		errs = append(errs, s.dayDB.Close())
	}
	errs = append(errs, s.state.Close())
	return errors.Join(errs...)
}

func (s *Store) LoadSnapshot(ctx context.Context) (map[string]uint64, time.Time, error) {
	rows, err := s.state.QueryContext(ctx, `SELECT ip, bytes FROM snapshot`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	result := make(map[string]uint64)
	for rows.Next() {
		var ip string
		var bytes int64
		if err := rows.Scan(&ip, &bytes); err != nil {
			return nil, time.Time{}, err
		}
		if bytes >= 0 {
			result[ip] = uint64(bytes)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	value, err := s.meta(ctx, "snapshot_at")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, err
	}
	var at time.Time
	if value != "" {
		seconds, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr == nil {
			at = time.Unix(seconds, 0)
		}
	}
	return result, at, nil
}

func (s *Store) SaveBaseline(ctx context.Context, counters map[string]uint64, now time.Time) error {
	return s.saveRuntime(ctx, counters, nil, now)
}

func (s *Store) Record(ctx context.Context, deltas, counters map[string]uint64, now time.Time, includeRecent bool) error {
	if len(deltas) > 0 {
		db, err := s.dailyDB(now)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		bucket := now.Truncate(time.Minute).Unix()
		stmt, err := tx.PrepareContext(ctx, `
INSERT INTO traffic(bucket, ip, bytes) VALUES (?, ?, ?)
ON CONFLICT(bucket, ip) DO UPDATE SET bytes = bytes + excluded.bytes`)
		if err != nil {
			tx.Rollback()
			return err
		}
		for ip, bytes := range deltas {
			if bytes == 0 {
				continue
			}
			if _, err := stmt.ExecContext(ctx, bucket, ip, int64(bytes)); err != nil {
				stmt.Close()
				tx.Rollback()
				return err
			}
		}
		if err := stmt.Close(); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	recent := deltas
	if !includeRecent {
		recent = nil
	}
	return s.saveRuntime(ctx, counters, recent, now)
}

func (s *Store) saveRuntime(ctx context.Context, counters, recent map[string]uint64, now time.Time) error {
	tx, err := s.state.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot`); err != nil {
		tx.Rollback()
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO snapshot(ip, bytes) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for ip, bytes := range counters {
		if _, err := stmt.ExecContext(ctx, ip, int64(bytes)); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO meta(key, value) VALUES ('snapshot_at', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(now.Unix(), 10)); err != nil {
		tx.Rollback()
		return err
	}
	if len(recent) > 0 {
		stamp := now.Unix()
		stmt, err := tx.PrepareContext(ctx, `
INSERT INTO recent(ts, ip, bytes) VALUES (?, ?, ?)
ON CONFLICT(ts, ip) DO UPDATE SET bytes = bytes + excluded.bytes`)
		if err != nil {
			tx.Rollback()
			return err
		}
		for ip, bytes := range recent {
			if bytes == 0 {
				continue
			}
			if _, err := stmt.ExecContext(ctx, stamp, ip, int64(bytes)); err != nil {
				stmt.Close()
				tx.Rollback()
				return err
			}
		}
		if err := stmt.Close(); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recent WHERE ts <= ?`, now.Add(-10*time.Minute).Unix()); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) dailyDB(now time.Time) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := now.In(s.loc).Format("2006-01-02")
	if s.dayDB != nil && s.day == day {
		return s.dayDB, nil
	}
	if s.dayDB != nil {
		if err := s.dayDB.Close(); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(s.baseDir, "traffic-"+day+".db")
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS traffic (
    bucket INTEGER NOT NULL,
    ip TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    PRIMARY KEY (bucket, ip)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS traffic_ip_bucket ON traffic(ip, bucket);
`); err != nil {
		db.Close()
		return nil, err
	}
	s.day = day
	s.dayDB = db
	return db, nil
}

func (s *Store) LoadRecent(ctx context.Context, since time.Time) (map[string][]traffic.Point, error) {
	rows, err := s.state.QueryContext(ctx, `SELECT ts, ip, bytes FROM recent WHERE ts > ? ORDER BY ts`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]traffic.Point)
	for rows.Next() {
		var ts int64
		var ip string
		var bytes int64
		if err := rows.Scan(&ts, &ip, &bytes); err != nil {
			return nil, err
		}
		if bytes > 0 {
			result[ip] = append(result[ip], traffic.Point{At: time.Unix(ts, 0), Bytes: uint64(bytes)})
		}
	}
	return result, rows.Err()
}

func (s *Store) LoadAlertTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.state.QueryContext(ctx, `SELECT ip, last_alert FROM alert_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time)
	for rows.Next() {
		var ip string
		var stamp int64
		if err := rows.Scan(&ip, &stamp); err != nil {
			return nil, err
		}
		result[ip] = time.Unix(stamp, 0)
	}
	return result, rows.Err()
}

func (s *Store) SaveAlertTimes(ctx context.Context, values map[string]time.Time) error {
	tx, err := s.state.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_state`); err != nil {
		tx.Rollback()
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO alert_state(ip, last_alert) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for ip, at := range values {
		if _, err := stmt.ExecContext(ctx, ip, at.Unix()); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Query(ctx context.Context, start, end time.Time) (map[string]uint64, error) {
	result := make(map[string]uint64)
	day := midnight(start.In(s.loc))
	lastDay := midnight(end.In(s.loc))
	for !day.After(lastDay) {
		path := filepath.Join(s.baseDir, "traffic-"+day.Format("2006-01-02")+".db")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				day = day.AddDate(0, 0, 1)
				continue
			}
			return nil, err
		}
		db, err := openDB(path)
		if err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, `
SELECT ip, SUM(bytes) FROM traffic
WHERE bucket >= ? AND bucket <= ?
GROUP BY ip`, start.Truncate(time.Minute).Unix(), end.Unix())
		if err != nil {
			db.Close()
			return nil, err
		}
		for rows.Next() {
			var ip string
			var bytes int64
			if err := rows.Scan(&ip, &bytes); err != nil {
				rows.Close()
				db.Close()
				return nil, err
			}
			if bytes > 0 {
				result[ip] += uint64(bytes)
			}
		}
		err = rows.Err()
		rows.Close()
		db.Close()
		if err != nil {
			return nil, err
		}
		day = day.AddDate(0, 0, 1)
	}
	return result, nil
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func (s *Store) Owner(ctx context.Context) (string, error) {
	value, err := s.meta(ctx, "owner_openid")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) BindOwner(ctx context.Context, openID string) error {
	_, err := s.state.ExecContext(ctx, `
INSERT INTO meta(key, value) VALUES ('owner_openid', ?)
ON CONFLICT(key) DO NOTHING`, openID)
	return err
}

func (s *Store) meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.state.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

func (s *Store) MarkMessageSeen(ctx context.Context, id string, now time.Time) (bool, error) {
	result, err := s.state.ExecContext(ctx, `INSERT OR IGNORE INTO seen_messages(id, seen_at) VALUES (?, ?)`, id, now.Unix())
	if err != nil {
		return false, err
	}
	if _, err := s.state.ExecContext(ctx, `DELETE FROM seen_messages WHERE seen_at < ?`, now.Add(-24*time.Hour).Unix()); err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) QueueAlert(ctx context.Context, alert traffic.Alert) error {
	_, err := s.state.ExecContext(ctx, `
INSERT INTO pending_alerts(ip, bytes, occurred_at) VALUES (?, ?, ?)`, alert.IP, int64(alert.Bytes), alert.At.Unix())
	return err
}

func (s *Store) PendingAlerts(ctx context.Context, now time.Time) ([]PendingAlert, error) {
	if _, err := s.state.ExecContext(ctx, `DELETE FROM pending_alerts WHERE sent_at IS NULL AND occurred_at < ?`, now.Add(-24*time.Hour).Unix()); err != nil {
		return nil, err
	}
	rows, err := s.state.QueryContext(ctx, `
SELECT id, ip, bytes, occurred_at FROM pending_alerts
WHERE sent_at IS NULL AND occurred_at >= ?
ORDER BY occurred_at`, now.Add(-24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PendingAlert
	for rows.Next() {
		var item PendingAlert
		var bytes int64
		var occurred int64
		if err := rows.Scan(&item.ID, &item.IP, &bytes, &occurred); err != nil {
			return nil, err
		}
		item.Bytes = uint64(bytes)
		item.OccurredAt = time.Unix(occurred, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) MarkAlertSent(ctx context.Context, id int64, now time.Time) error {
	_, err := s.state.ExecContext(ctx, `UPDATE pending_alerts SET sent_at = ? WHERE id = ?`, now.Unix(), id)
	return err
}

func (s *Store) Cleanup(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return err
	}
	type fileInfo struct {
		name string
		day  time.Time
		size int64
	}
	var files []fileInfo
	cutoff := midnight(now.In(s.loc)).AddDate(0, 0, -(retentionDays - 1))
	current := now.In(s.loc).Format("2006-01-02")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := trafficFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", match[1], s.loc)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, fileInfo{name: entry.Name(), day: day, size: info.Size() + sidecarSize(s.baseDir, entry.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day.Before(files[j].day) })

	kept := files[:0]
	for _, file := range files {
		if file.day.Before(cutoff) && file.day.Format("2006-01-02") != current {
			if err := removeTrafficFile(s.baseDir, file.name); err != nil {
				return err
			}
			continue
		}
		kept = append(kept, file)
	}
	files = kept
	var total int64
	for _, file := range files {
		total += file.size
	}
	if total <= maxTrafficBytes {
		return nil
	}
	for _, file := range files {
		if total <= targetTrafficBytes {
			break
		}
		if file.day.Format("2006-01-02") == current {
			continue
		}
		if err := removeTrafficFile(s.baseDir, file.name); err != nil {
			return err
		}
		total -= file.size
	}
	return nil
}

func sidecarSize(baseDir, name string) int64 {
	var total int64
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(filepath.Join(baseDir, name+suffix))
		if err == nil {
			total += info.Size()
		}
	}
	return total
}

func removeTrafficFile(baseDir, name string) error {
	if !trafficFilePattern.MatchString(name) || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("refuse unsafe traffic filename %q", name)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(baseDir, name+suffix)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
