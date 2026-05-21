package utils

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Scheduler struct {
	db     *sql.DB
	db_dir string

	event_callback func(string, int64, string)

	create_stmt *sql.Stmt
	cancel_stmt *sql.Stmt
	stopChan    chan struct{}
	wakeChan    chan struct{}
	callbackSem chan struct{}
	mu          sync.Mutex
	loopWg      sync.WaitGroup
}

const db_version = 2

func (sched *Scheduler) Start() {
	if err := os.MkdirAll(sched.db_dir, 0755); err != nil {
		PanicOnErr(err, "Could not create database directory: %v", err, true)
	}

	db, err := sql.Open("sqlite3", "file:"+filepath.Join(sched.db_dir, "scheduler.db")+"?_journal_mode=WAL&_synchronous=1")
	PanicOnErr(err, "Could not open scheduler DB: %v", err, true)

	sched.db = db
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	sched.createTable()

	istmt, err := db.Prepare(`INSERT INTO scheduled_events (name, send_at, payload) VALUES (?, ?, ?)`)
	PanicOnErr(err, "Failed to prepare insert statement: %v", err, true)
	sched.create_stmt = istmt

	cstmt, err := db.Prepare(`DELETE FROM scheduled_events WHERE event_id = ?`)
	PanicOnErr(err, "Failed to prepare cancel statement: %v", err, true)
	sched.cancel_stmt = cstmt

	sched.loopWg.Add(1)
	go sched.loop()
}

func (sched *Scheduler) CreateEvent(name string, sendAt int64, payload string) (int64, error) {
	if len(payload) > 4096 {
		return 0, errors.New("payload must be less than 4096 in length")
	}

	sched.mu.Lock()
	defer sched.mu.Unlock()
	res, err := sched.create_stmt.Exec(name, sendAt, payload)
	if err != nil {
		return 0, err
	}

	select {
	case sched.wakeChan <- struct{}{}:
	default:
	}

	return res.LastInsertId()
}

func (sched *Scheduler) CancelEvent(eventID int64) int64 {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	res, err := sched.cancel_stmt.Exec(eventID)
	if err != nil {
		return -1
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return -1
	}

	return rowsAffected
}

func (sched *Scheduler) Close() error {
	close(sched.stopChan)
	sched.loopWg.Wait()

	sched.create_stmt.Close()
	sched.cancel_stmt.Close()
	return sched.db.Close()
}

func (sched *Scheduler) createTable() {
	var version int
	err := sched.db.QueryRow(`PRAGMA user_version;`).Scan(&version)
	PanicOnErr(err, "Failed to read PRAGMA user_version: %v", err, true)

	if version == db_version {
		return
	}

	tx, err := sched.db.Begin()
	PanicOnErr(err, "Failed to begin table transaction: %v", err, true)

	for version < db_version {
		switch version {
		case 0:
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS scheduled_events (
					event_id INTEGER PRIMARY KEY AUTOINCREMENT,
					send_at INTEGER NOT NULL,
					payload TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_send_at ON scheduled_events (send_at);
			`)
			PanicOnErr(err, "Failed to apply schema v1: %v", err, true)
			version = 1

		case 1:
			_, err := tx.Exec(`ALTER TABLE scheduled_events ADD COLUMN name TEXT DEFAULT '';`)
			PanicOnErr(err, "Failed to apply schema v2: %v", err, true)
			version = 2
		}

		_, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
		PanicOnErr(err, "Failed to set user_version: %v", err, true)
	}

	err = tx.Commit()
	PanicOnErr(err, "Failed to commit table transaction: %v", err, true)
}

func (sched *Scheduler) loop() {
	defer sched.loopWg.Done()

	selectStmt, err := sched.db.Prepare(`
		SELECT name, event_id, payload FROM scheduled_events 
		WHERE send_at <= ? 
		ORDER BY send_at LIMIT 100`)
	if err != nil {
		fmt.Printf("Failed to prepare select statement: %v\n", err)
		return
	}
	defer selectStmt.Close()

	deleteStmt, err := sched.db.Prepare(`DELETE FROM scheduled_events WHERE event_id = ?`)
	if err != nil {
		fmt.Printf("Failed to prepare delete statement: %v\n", err)
		return
	}
	defer deleteStmt.Close()

	ticker := time.NewTicker(sched.nextPollInterval())
	defer ticker.Stop()

	for {
		select {
		case now := <-ticker.C:
			rows, err := selectStmt.Query(now.Unix())
			if err != nil {
				ticker.Stop()
				ticker = time.NewTicker(sched.nextPollInterval())
				continue
			}

			var toDelete = make([]int64, 0, 100)

			for rows.Next() {
				var name string
				var event_id int64
				var payload string
				if err := rows.Scan(&name, &event_id, &payload); err != nil {
					continue
				}

				toDelete = append(toDelete, event_id)
				sched.callbackSem <- struct{}{}
				go func(n string, eid int64, p string) {
					defer func() { <-sched.callbackSem }()
					sched.event_callback(n, eid, p)
				}(name, event_id, payload)
			}
			rows.Close()

			if len(toDelete) > 0 {
				tx, err := sched.db.Begin()
				if err != nil {
					ticker.Stop()
					ticker = time.NewTicker(sched.nextPollInterval())
					continue
				}

				commitOK := true
				for _, id := range toDelete {
					if _, err := tx.Stmt(deleteStmt).Exec(id); err != nil {
						fmt.Printf("Failed to delete scheduled event %d: %v\n", id, err)
						commitOK = false
					}
				}
				if commitOK {
					if err := tx.Commit(); err != nil {
						fmt.Printf("Failed to commit scheduled event deletions: %v\n", err)
						tx.Rollback()
					}
				} else {
					tx.Rollback()
				}
			}

			ticker.Stop()
			ticker = time.NewTicker(sched.nextPollInterval())

		case <-sched.wakeChan:
			ticker.Stop()
			ticker = time.NewTicker(sched.nextPollInterval())

		case <-sched.stopChan:
			return
		}
	}
}

func (sched *Scheduler) nextPollInterval() time.Duration {
	var nextSendAt sql.NullInt64
	sched.db.QueryRow(`SELECT MIN(send_at) FROM scheduled_events`).Scan(&nextSendAt)

	if !nextSendAt.Valid {
		return 30 * time.Second
	}

	d := time.Duration(nextSendAt.Int64-time.Now().Unix()) * time.Second
	if d < time.Second {
		return time.Second
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func NewScheduler(db_dir string, event_callback func(string, int64, string)) *Scheduler {
	return &Scheduler{
		db_dir:         db_dir,
		event_callback: event_callback,
		stopChan:       make(chan struct{}),
		wakeChan:       make(chan struct{}, 1),
		callbackSem:    make(chan struct{}, 10),
	}
}
