package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const clientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
const clientSecret = "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"
const tokenURL = "https://oauth2.googleapis.com/token"
const userInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type userInfo struct {
	Email string `json:"email"`
}

func main() {
	emailFlag := flag.String("email", "", "")
	refreshTokenFlag := flag.String("refresh-token", "", "")
	dbPathFlag := flag.String("db-path", "", "")
	userDataDirFlag := flag.String("user-data-dir", "", "")
	noRestartFlag := flag.Bool("no-restart", false, "")
	flag.Parse()

	reader := bufio.NewReader(os.Stdin)

	email := strings.TrimSpace(*emailFlag)
	if email == "" {
		email = prompt(reader, "Email")
	}

	refreshToken := strings.TrimSpace(*refreshTokenFlag)
	if refreshToken == "" {
		refreshToken = prompt(reader, "Refresh Token")
	}

	if email == "" || refreshToken == "" {
		fail("email 和 refresh_token 不能为空")
	}

	tokenResp, err := refreshAccessToken(refreshToken)
	if err != nil {
		fail(err.Error())
	}

	if resolvedEmail, err := fetchUserEmail(tokenResp.AccessToken); err == nil && resolvedEmail != "" {
		email = resolvedEmail
	}

	dbPath, err := resolveDBPath(strings.TrimSpace(*dbPathFlag), strings.TrimSpace(*userDataDirFlag))
	if err != nil {
		fmt.Println(err.Error())
		dbPath = strings.TrimSpace(prompt(reader, "state.vscdb 路径"))
		if dbPath == "" {
			fail("未提供 state.vscdb 路径")
		}
	}

	if _, err := os.Stat(dbPath); err != nil {
		fail(fmt.Sprintf("数据库不存在: %s", dbPath))
	}

	wasRunning := false
	if !*noRestartFlag {
		wasRunning = isAntigravityRunning()
		if wasRunning {
			if err := stopAntigravity(); err != nil {
				fail(err.Error())
			}
		}
	}

	restarted := false
	if wasRunning && !*noRestartFlag {
		defer func() {
			if restarted {
				return
			}
			_ = startAntigravity()
		}()
	}

	backupPath, err := backupFile(dbPath)
	if err != nil {
		fail(err.Error())
	}

	expiry := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix()
	if err := injectTokens(dbPath, email, tokenResp.AccessToken, refreshToken, expiry); err != nil {
		fail(err.Error())
	}

	if wasRunning && !*noRestartFlag {
		if err := startAntigravity(); err != nil {
			fail(err.Error())
		}
		restarted = true
	}

	fmt.Println("切号完成")
	fmt.Println("Email:", email)
	fmt.Println("DB:", dbPath)
	fmt.Println("Backup:", backupPath)
}

func prompt(reader *bufio.Reader, label string) string {
	fmt.Printf("%s: ", label)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func refreshAccessToken(refreshToken string) (*tokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("client_secret", clientSecret)
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "antigravity-tools")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("刷新 access_token 失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if len(body) == 0 {
			return nil, fmt.Errorf("刷新 access_token 失败: %s", resp.Status)
		}
		return nil, fmt.Errorf("刷新 access_token 失败: %s", strings.TrimSpace(string(body)))
	}

	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析 access_token 响应失败: %w", err)
	}
	if result.AccessToken == "" {
		return nil, errors.New("未获取到 access_token")
	}
	return &result, nil
}

func fetchUserEmail(accessToken string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, userInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "antigravity-tools")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo status %s", resp.Status)
	}

	var info userInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Email), nil
}

func resolveDBPath(dbPath, userDataDir string) (string, error) {
	if dbPath != "" {
		return dbPath, nil
	}

	if value := strings.TrimSpace(os.Getenv("ANTIGRAVITY_DB_PATH")); value != "" {
		return value, nil
	}

	if userDataDir == "" {
		userDataDir = strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_DATA_DIR"))
	}
	if userDataDir != "" {
		path := filepath.Join(userDataDir, "User", "globalStorage", "state.vscdb")
		if fileExists(path) {
			return path, nil
		}
	}

	if path := portableDBPath(); path != "" && fileExists(path) {
		return path, nil
	}

	path := defaultDBPath()
	if path != "" && fileExists(path) {
		return path, nil
	}

	return "", fmt.Errorf("未自动找到 state.vscdb，默认路径尝试为: %s", path)
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb")
	default:
		return filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	}
}

func portableDBPath() string {
	exePath := standardExecutablePath()
	if exePath == "" {
		return ""
	}
	parent := filepath.Dir(exePath)
	if runtime.GOOS == "darwin" && strings.HasSuffix(exePath, ".app") {
		parent = filepath.Dir(exePath)
	}
	return filepath.Join(parent, "data", "user-data", "User", "globalStorage", "state.vscdb")
}

func standardExecutablePath() string {
	if value := strings.TrimSpace(os.Getenv("ANTIGRAVITY_APP_PATH")); value != "" {
		return value
	}

	switch runtime.GOOS {
	case "darwin":
		path := "/Applications/Antigravity.app"
		if fileExists(path) {
			return path
		}
	case "windows":
		var candidates []string
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			candidates = append(candidates, filepath.Join(local, "Programs", "Antigravity", "Antigravity.exe"))
		}
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Antigravity", "Antigravity.exe"))
		}
		if programFilesX86 := os.Getenv("ProgramFiles(x86)"); programFilesX86 != "" {
			candidates = append(candidates, filepath.Join(programFilesX86, "Antigravity", "Antigravity.exe"))
		}
		for _, candidate := range candidates {
			if fileExists(candidate) {
				return candidate
			}
		}
	default:
		candidates := []string{
			"/usr/bin/antigravity",
			"/opt/Antigravity/antigravity",
			"/usr/share/antigravity/antigravity",
		}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append([]string{filepath.Join(home, ".local", "bin", "antigravity")}, candidates...)
		}
		for _, candidate := range candidates {
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isAntigravityRunning() bool {
	switch runtime.GOOS {
	case "darwin":
		return commandSucceeded("pgrep", "-x", "Antigravity") || commandSucceeded("pgrep", "-f", "/Antigravity.app/Contents/MacOS/Antigravity")
	case "windows":
		output, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Antigravity.exe").CombinedOutput()
		if err != nil {
			return false
		}
		return bytes.Contains(bytes.ToLower(output), []byte("antigravity.exe"))
	default:
		return commandSucceeded("pgrep", "-x", "antigravity") || commandSucceeded("pgrep", "-x", "Antigravity")
	}
}

func stopAntigravity() error {
	switch runtime.GOOS {
	case "darwin":
		_, _ = exec.Command("osascript", "-e", `tell application "Antigravity" to quit`).CombinedOutput()
		time.Sleep(2 * time.Second)
		if isAntigravityRunning() {
			_, _ = exec.Command("pkill", "-x", "Antigravity").CombinedOutput()
		}
	case "windows":
		if _, err := exec.Command("taskkill", "/IM", "Antigravity.exe", "/F").CombinedOutput(); err != nil && isAntigravityRunning() {
			return fmt.Errorf("关闭 Antigravity 失败: %w", err)
		}
	default:
		_, _ = exec.Command("pkill", "-x", "antigravity").CombinedOutput()
		_, _ = exec.Command("pkill", "-x", "Antigravity").CombinedOutput()
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !isAntigravityRunning() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("等待 Antigravity 退出超时")
}

func startAntigravity() error {
	switch runtime.GOOS {
	case "darwin":
		appPath := standardExecutablePath()
		if appPath != "" {
			if err := exec.Command("open", "-a", appPath).Start(); err == nil {
				return nil
			}
		}
		return exec.Command("open", "-a", "Antigravity").Start()
	case "windows":
		exePath := standardExecutablePath()
		if exePath == "" {
			return errors.New("未找到 Antigravity.exe")
		}
		return exec.Command("cmd", "/c", "start", "", exePath).Start()
	default:
		exePath := standardExecutablePath()
		if exePath == "" {
			exePath = "antigravity"
		}
		return exec.Command(exePath).Start()
	}
}

func commandSucceeded(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func backupFile(path string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开数据库失败: %w", err)
	}
	defer source.Close()

	backupPath := fmt.Sprintf("%s.%s.bak", path, time.Now().Format("20060102150405"))
	target, err := os.Create(backupPath)
	if err != nil {
		return "", fmt.Errorf("创建备份失败: %w", err)
	}
	defer target.Close()

	if _, err := io.Copy(target, source); err != nil {
		return "", fmt.Errorf("写入备份失败: %w", err)
	}
	return backupPath, nil
}

func injectTokens(dbPath, email, accessToken, refreshToken string, expiry int64) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("设置 busy_timeout 失败: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	if err := writeNewFormat(tx, accessToken, refreshToken, expiry); err != nil {
		return err
	}
	if err := writeOldFormat(tx, email, accessToken, refreshToken, expiry); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`, "antigravityOnboarding", "true"); err != nil {
		return fmt.Errorf("写入 onboarding 标记失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

func writeNewFormat(tx *sql.Tx, accessToken, refreshToken string, expiry int64) error {
	oauthInfo := createOAuthInfo(accessToken, refreshToken, expiry)
	oauthInfoB64 := base64.StdEncoding.EncodeToString(oauthInfo)
	inner2 := encodeStringField(1, oauthInfoB64)
	inner1 := encodeStringField(1, "oauthTokenInfoSentinelKey")
	inner := append(inner1, encodeLenDelimField(2, inner2)...)
	outer := encodeLenDelimField(1, inner)
	outerB64 := base64.StdEncoding.EncodeToString(outer)

	if _, err := tx.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`, "antigravityUnifiedStateSync.oauthToken", outerB64); err != nil {
		return fmt.Errorf("写入新格式认证失败: %w", err)
	}
	return nil
}

func writeOldFormat(tx *sql.Tx, email, accessToken, refreshToken string, expiry int64) error {
	var currentData string
	err := tx.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "jetskiStateSync.agentManagerInitState").Scan(&currentData)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取旧格式认证失败: %w", err)
	}

	blob, err := base64.StdEncoding.DecodeString(currentData)
	if err != nil {
		return fmt.Errorf("解码旧格式认证失败: %w", err)
	}

	cleanData, err := removeField(blob, 1)
	if err != nil {
		return err
	}
	cleanData, err = removeField(cleanData, 2)
	if err != nil {
		return err
	}
	cleanData, err = removeField(cleanData, 6)
	if err != nil {
		return err
	}

	finalData := append(cleanData, createEmailField(email)...)
	finalData = append(finalData, createOAuthField(accessToken, refreshToken, expiry)...)
	finalB64 := base64.StdEncoding.EncodeToString(finalData)

	if _, err := tx.Exec(`UPDATE ItemTable SET value = ? WHERE key = ?`, finalB64, "jetskiStateSync.agentManagerInitState"); err != nil {
		return fmt.Errorf("写入旧格式认证失败: %w", err)
	}
	return nil
}

func encodeVarint(value uint64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, value)
	return buf[:n]
}

func readVarint(data []byte, offset int) (uint64, int, error) {
	value, n := binary.Uvarint(data[offset:])
	if n <= 0 {
		return 0, offset, errors.New("protobuf varint 解析失败")
	}
	return value, offset + n, nil
}

func skipField(data []byte, offset int, wireType uint8) (int, error) {
	switch wireType {
	case 0:
		_, next, err := readVarint(data, offset)
		return next, err
	case 1:
		return offset + 8, nil
	case 2:
		length, next, err := readVarint(data, offset)
		if err != nil {
			return offset, err
		}
		return next + int(length), nil
	case 5:
		return offset + 4, nil
	default:
		return offset, fmt.Errorf("未知 protobuf wire type: %d", wireType)
	}
}

func removeField(data []byte, fieldNum uint32) ([]byte, error) {
	var result []byte
	offset := 0
	for offset < len(data) {
		start := offset
		tag, next, err := readVarint(data, offset)
		if err != nil {
			return nil, err
		}
		wireType := uint8(tag & 7)
		currentField := uint32(tag >> 3)
		offset, err = skipField(data, next, wireType)
		if err != nil {
			return nil, err
		}
		if currentField != fieldNum {
			result = append(result, data[start:offset]...)
		}
	}
	return result, nil
}

func findField(data []byte, targetField uint32) ([]byte, bool, error) {
	offset := 0
	for offset < len(data) {
		tag, next, err := readVarint(data, offset)
		if err != nil {
			return nil, false, err
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		if fieldNum == targetField && wireType == 2 {
			length, contentOffset, err := readVarint(data, next)
			if err != nil {
				return nil, false, err
			}
			end := contentOffset + int(length)
			if end > len(data) {
				return nil, false, errors.New("protobuf 字段越界")
			}
			return data[contentOffset:end], true, nil
		}
		offset, err = skipField(data, next, wireType)
		if err != nil {
			return nil, false, err
		}
	}
	return nil, false, nil
}

func encodeLenDelimField(fieldNum uint32, data []byte) []byte {
	tag := (uint64(fieldNum) << 3) | 2
	var result []byte
	result = append(result, encodeVarint(tag)...)
	result = append(result, encodeVarint(uint64(len(data)))...)
	result = append(result, data...)
	return result
}

func encodeStringField(fieldNum uint32, value string) []byte {
	return encodeLenDelimField(fieldNum, []byte(value))
}

func createOAuthInfo(accessToken, refreshToken string, expiry int64) []byte {
	field1 := encodeStringField(1, accessToken)
	field2 := encodeStringField(2, "Bearer")
	field3 := encodeStringField(3, refreshToken)
	timestamp := append(encodeVarint((1<<3)|0), encodeVarint(uint64(expiry))...)
	field4 := encodeLenDelimField(4, timestamp)
	return append(append(append(field1, field2...), field3...), field4...)
}

func createOAuthField(accessToken, refreshToken string, expiry int64) []byte {
	return encodeLenDelimField(6, createOAuthInfo(accessToken, refreshToken, expiry))
}

func createEmailField(email string) []byte {
	return encodeStringField(2, email)
}
