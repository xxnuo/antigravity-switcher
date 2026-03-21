package main

import (
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInjectTokens(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "state.vscdb")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}

	oldBlob := append(encodeStringField(9, "keep"), createEmailField("old@example.com")...)
	oldBlob = append(oldBlob, createOAuthField("old-access", "old-refresh", 111)...)
	oldB64 := base64.StdEncoding.EncodeToString(oldBlob)
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "jetskiStateSync.agentManagerInitState", oldB64); err != nil {
		t.Fatal(err)
	}

	if err := injectTokens(dbPath, "new@example.com", "new-access", "new-refresh", 222); err != nil {
		t.Fatal(err)
	}

	var newValue string
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.oauthToken").Scan(&newValue); err != nil {
		t.Fatal(err)
	}

	outer, err := base64.StdEncoding.DecodeString(newValue)
	if err != nil {
		t.Fatal(err)
	}
	inner1, ok, err := findField(outer, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	inner2, ok, err := findField(inner1, 2)
	if err != nil || !ok {
		t.Fatal(err)
	}
	oauthInfoB64, ok, err := findField(inner2, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	oauthInfo, err := base64.StdEncoding.DecodeString(string(oauthInfoB64))
	if err != nil {
		t.Fatal(err)
	}
	newRefresh, ok, err := findField(oauthInfo, 3)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if string(newRefresh) != "new-refresh" {
		t.Fatalf("unexpected refresh token: %s", string(newRefresh))
	}

	var oldValue string
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "jetskiStateSync.agentManagerInitState").Scan(&oldValue); err != nil {
		t.Fatal(err)
	}

	oldDecoded, err := base64.StdEncoding.DecodeString(oldValue)
	if err != nil {
		t.Fatal(err)
	}
	emailField, ok, err := findField(oldDecoded, 2)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if string(emailField) != "new@example.com" {
		t.Fatalf("unexpected email: %s", string(emailField))
	}
	oauthField, ok, err := findField(oldDecoded, 6)
	if err != nil || !ok {
		t.Fatal(err)
	}
	oldRefresh, ok, err := findField(oauthField, 3)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if string(oldRefresh) != "new-refresh" {
		t.Fatalf("unexpected old refresh token: %s", string(oldRefresh))
	}

	var onboarding string
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityOnboarding").Scan(&onboarding); err != nil {
		t.Fatal(err)
	}
	if onboarding != "true" {
		t.Fatalf("unexpected onboarding value: %s", onboarding)
	}

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal(err)
	}
}
