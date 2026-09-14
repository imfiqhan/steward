package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type paletteRow2 struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

// An empty list is a legitimate answer, so a query the database refused must
// not look like one: the palette answers 200 either way, and the log is not
// somewhere a reader looks.
func TestPaletteSaysWhenASectionCouldNotAnswer(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow2{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&paletteRow2{Title: "A headline"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("palette-failure-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow2](app).Command("Title")
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	ask := func(q string) map[string]any {
		t.Helper()
		code, body := getPath(t, client, srv.URL+"/admin/_command?q="+q)
		if code != http.StatusOK {
			t.Fatalf("palette = %d, want 200", code)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("palette did not answer JSON: %.160s", body)
		}
		return out
	}

	// A term that matches nothing: complete, and empty.
	nothing := ask("zzzznothing")
	if nothing["partial"] != false {
		t.Errorf("a term matching nothing reported an incomplete answer: %v", nothing)
	}

	// Now make the query itself fail, the way a column the engine will not
	// compare does.
	if err := db.Migrator().DropTable(&paletteRow2{}); err != nil {
		t.Fatal(err)
	}
	broken := ask("headline")
	if broken["partial"] != true {
		t.Errorf("a refused query looked like a complete answer: %v", broken)
	}
	if broken["reason"] != "error" {
		t.Errorf("reason = %v, want error — a timeout is a different thing", broken["reason"])
	}
	if rs, _ := broken["results"].([]any); len(rs) != 0 {
		t.Errorf("a refused query returned %d results", len(rs))
	}
}
