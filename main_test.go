package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestParseCLIArgs(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "state.vscdb")
	userDataDir := filepath.Join(tempDir, "user-data")

	tests := []struct {
		name    string
		args    []string
		want    cliOptions
		wantErr string
	}{
		{
			name: "refresh token only",
			args: []string{"tool", "refresh-token"},
			want: cliOptions{
				refreshToken: "refresh-token",
			},
		},
		{
			name: "email refresh and db path",
			args: []string{"tool", "user@example.com", "refresh-token", dbPath},
			want: cliOptions{
				email:        "user@example.com",
				refreshToken: "refresh-token",
				dbPath:       dbPath,
			},
		},
		{
			name: "refresh and db path",
			args: []string{"tool", "refresh-token", dbPath, "--no-restart"},
			want: cliOptions{
				refreshToken: "refresh-token",
				dbPath:       dbPath,
				noRestart:    true,
			},
		},
		{
			name: "flag and positional merge",
			args: []string{"tool", "--user-data-dir", userDataDir, "user@example.com", "refresh-token"},
			want: cliOptions{
				email:        "user@example.com",
				refreshToken: "refresh-token",
				userDataDir:  userDataDir,
			},
		},
		{
			name:    "conflicting refresh token",
			args:    []string{"tool", "--refresh-token", "flag-token", "positional-token"},
			wantErr: "refresh-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.email != tt.want.email {
				t.Fatalf("unexpected email: %q", got.email)
			}
			if got.refreshToken != tt.want.refreshToken {
				t.Fatalf("unexpected refresh token: %q", got.refreshToken)
			}
			if got.dbPath != tt.want.dbPath {
				t.Fatalf("unexpected db path: %q", got.dbPath)
			}
			if got.userDataDir != tt.want.userDataDir {
				t.Fatalf("unexpected user data dir: %q", got.userDataDir)
			}
			if got.noRestart != tt.want.noRestart {
				t.Fatalf("unexpected noRestart: %v", got.noRestart)
			}
		})
	}
}

func TestRunRejectsMissingRefreshTokenWithoutInteractiveInput(t *testing.T) {
	err := run([]string{"tool"}, bytes.NewBuffer(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "缺少 refresh_token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDBPathFromUserDataDir(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDBPath("", tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dbPath {
		t.Fatalf("unexpected db path: %q", got)
	}
}

func TestExtractUserDataDirFromCommandLine(t *testing.T) {
	tempDir := t.TempDir()
	got := extractUserDataDirFromCommandLine(`"/Applications/Antigravity.app/Contents/MacOS/Antigravity" --user-data-dir "` + tempDir + `" --flag`)
	if got != tempDir {
		t.Fatalf("unexpected user data dir: %q", got)
	}

	got = extractUserDataDirFromCommandLine(`Antigravity --user-data-dir=` + tempDir)
	if got != tempDir {
		t.Fatalf("unexpected inline user data dir: %q", got)
	}
}

func TestDetectDBFormats(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?), (?, ?)`,
		"antigravityUnifiedStateSync.oauthToken", "x",
		"jetskiStateSync.agentManagerInitState", "y",
	); err != nil {
		t.Fatal(err)
	}

	formats, err := detectDBFormats(db)
	if err != nil {
		t.Fatal(err)
	}
	if !formats.newFormat || !formats.oldFormat {
		t.Fatalf("unexpected formats: %+v", formats)
	}
}

func TestValidateDBPath(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "state.vscdb")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE OtherTable (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := validateDBPath(dbPath); err == nil || !strings.Contains(err.Error(), "ItemTable") {
		t.Fatalf("unexpected error: %v", err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}

	if err := validateDBPath(dbPath); err != nil {
		t.Fatal(err)
	}
}

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
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "antigravityUnifiedStateSync.oauthToken", "seed"); err != nil {
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

func TestInjectTokensOldFormatOnly(t *testing.T) {
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

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.oauthToken").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected new format row count: %d", count)
	}

	var onboarding string
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityOnboarding").Scan(&onboarding); err != nil {
		t.Fatal(err)
	}
	if onboarding != "true" {
		t.Fatalf("unexpected onboarding value: %s", onboarding)
	}
}

func TestRemoveFieldRejectsMalformedLength(t *testing.T) {
	data := append(encodeVarint((2<<3)|2), 10)
	if _, err := removeField(data, 1); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("unexpected error: %v", err)
	}
}
