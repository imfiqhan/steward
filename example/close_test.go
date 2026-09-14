package main

import (
	"testing"
	"time"

	steward "github.com/imfiqhan/steward"
)

func newClosableApp(t *testing.T) *steward.Panel {
	t.Helper()
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: testDB(t), SecretKey: []byte("close-test-secret-key-0000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	return app
}

// Close waits for the worker, so a Close that returns is a worker that has
// stopped. One that ignored the signal would leave this waiting until the test
// binary's own deadline.
func TestCloseStopsTheExportWorker(t *testing.T) {
	app := newClosableApp(t)

	done := make(chan error, 1)
	go func() { done <- app.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: the export worker ignored it")
	}
}

// A panel is closed by whoever owns it, which may be two pieces of code that
// do not know about each other.
func TestCloseIsRepeatableAndSafeWithoutBuild(t *testing.T) {
	app := newClosableApp(t)
	for i := 0; i < 3; i++ {
		if err := app.Close(); err != nil {
			t.Fatalf("Close %d: %v", i+1, err)
		}
	}

	unbuilt, err := steward.New(steward.Config{
		Prefix: "/admin", DB: testDB(t), SecretKey: []byte("close-test-secret-key-0000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := unbuilt.Close(); err != nil {
		t.Errorf("closing a panel that was never built: %v", err)
	}
}

// Nothing to stop is not an error, and must not hang either.
func TestCloseWithTheWorkerSwitchedOff(t *testing.T) {
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: testDB(t), SecretKey: []byte("close-test-secret-key-0000"),
		DisableExportWorker: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a panel with no worker")
	}
}
