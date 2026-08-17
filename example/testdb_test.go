package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// testDB opens the database a test runs against: SQLite by default, PostgreSQL
// when STEWARD_TEST_PG names one.
//
//	podman run --rm -p 5433:5432 -e POSTGRES_PASSWORD=steward \
//	  -e POSTGRES_DB=steward_test postgres:17-alpine
//	STEWARD_TEST_PG='postgres://postgres:steward@127.0.0.1:5433/steward_test?sslmode=disable' \
//	  go test ./...
//
// The point is not to test PostgreSQL. It is to find out which parts of the
// framework only ever worked because SQLite is forgiving — a claim nobody could
// check while every test opened SQLite directly.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("STEWARD_TEST_PG")
	if dsn == "" {
		db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/test.db"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}

	// One schema per test. Tests share a server, and several of them migrate a
	// table of the same name — without this they would race each other's DDL.
	schema := fmt.Sprintf("t%d", pgSchemaSeq.Add(1))
	admin := pgAdmin(t, dsn)
	// The counter restarts with the process, so a schema from an earlier run is
	// still there unless it is cleared first.
	if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// TimeZone matters, and not only here. A date filter compares a bare date
	// against a timestamptz column, and PostgreSQL reads that bare date in the
	// *session's* timezone — so a session on UTC against an application on
	// WIB moves every date boundary by seven hours. A deployment has to set
	// this to the zone the application runs in, exactly as this does.
	opts := "search_path=" + schema
	if !strings.Contains(dsn, "TimeZone=") {
		opts += "&TimeZone=" + localZone()
	}
	db, err := gorm.Open(postgres.Open(dsn+sep+opts), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening schema %s: %v", schema, err)
	}
	// A pool per test against one server runs the cluster out of connections
	// long before it runs out of tests, and every failure after that looks like
	// a bug in whatever happened to be running.
	if sql, err := db.DB(); err == nil {
		sql.SetMaxOpenConns(2)
		sql.SetMaxIdleConns(1)
	}
	t.Cleanup(func() {
		if sql, err := db.DB(); err == nil {
			_ = sql.Close()
		}
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
	})
	return db
}

var pgSchemaSeq atomic.Int64

var (
	pgAdminOnce sync.Once
	pgAdminDB   *gorm.DB
	pgAdminErr  error
)

// pgAdmin is one shared connection for creating and dropping the per-test
// schemas, rather than one more pool per test.
func pgAdmin(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	pgAdminOnce.Do(func() {
		pgAdminDB, pgAdminErr = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if pgAdminErr == nil {
			if sql, err := pgAdminDB.DB(); err == nil {
				sql.SetMaxOpenConns(4)
			}
		}
	})
	if pgAdminErr != nil {
		t.Fatalf("connecting to STEWARD_TEST_PG: %v", pgAdminErr)
	}
	return pgAdminDB
}

// localZone names the process's timezone the way PostgreSQL wants it: an IANA
// name. time.Local reports the abbreviation ("WIB"), which the server rejects,
// so the name comes from TZ or from what /etc/localtime points at.
func localZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}
