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

type cliOptions struct {
	email        string
	refreshToken string
	dbPath       string
	userDataDir  string
	noRestart    bool
	interactive  bool
}

type positionalArgs struct {
	email        string
	refreshToken string
	dbPath       string
}

func main() {
	if err := run(os.Args, os.Stdin, os.Stdout); err != nil {
		fail(err.Error())
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	options, err := parseCLIArgs(args)
	if err != nil {
		return err
	}
	options.interactive = isInteractiveInput(stdin)

	reader := bufio.NewReader(stdin)

	refreshToken := options.refreshToken
	if refreshToken == "" {
		if !options.interactive {
			return errors.New("缺少 refresh_token，请使用参数直接传递")
		}
		refreshToken = prompt(reader, stdout, "Refresh Token")
	}
	if refreshToken == "" {
		return errors.New("refresh_token 不能为空")
	}

	tokenResp, err := refreshAccessToken(refreshToken)
	if err != nil {
		return err
	}

	email := options.email
	if resolvedEmail, err := fetchUserEmail(tokenResp.AccessToken); err == nil && resolvedEmail != "" {
		email = resolvedEmail
	}
	if email == "" {
		if !options.interactive {
			return errors.New("缺少 email，且无法通过 access_token 自动获取")
		}
		email = prompt(reader, stdout, "Email")
	}
	if email == "" {
		return errors.New("email 不能为空")
	}

	dbPath, err := resolveDBPath(options.dbPath, options.userDataDir)
	if err != nil {
		if !options.interactive {
			return err
		}
		fmt.Fprintln(stdout, err.Error())
		dbPath = prompt(reader, stdout, "state.vscdb 路径")
		dbPath, err = normalizePath(dbPath)
		if err != nil {
			return err
		}
		if dbPath == "" {
			return errors.New("未提供 state.vscdb 路径")
		}
	}
	if err := validateDBPath(dbPath); err != nil {
		return err
	}

	wasRunning := false
	if !options.noRestart {
		wasRunning = isAntigravityRunning()
		if wasRunning {
			if err := stopAntigravity(); err != nil {
				return err
			}
		}
	}

	restarted := false
	if wasRunning && !options.noRestart {
		defer func() {
			if restarted {
				return
			}
			_ = startAntigravity()
		}()
	}

	backupPath, err := backupFile(dbPath)
	if err != nil {
		return err
	}

	expiry := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix()
	if err := injectTokens(dbPath, email, tokenResp.AccessToken, refreshToken, expiry); err != nil {
		return err
	}

	if wasRunning && !options.noRestart {
		if err := startAntigravity(); err != nil {
			return err
		}
		restarted = true
	}

	fmt.Fprintln(stdout, "切号完成")
	fmt.Fprintln(stdout, "Email:", email)
	fmt.Fprintln(stdout, "DB:", dbPath)
	fmt.Fprintln(stdout, "Backup:", backupPath)
	return nil
}

func parseCLIArgs(args []string) (cliOptions, error) {
	name := "antigravity-tools"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		name = filepath.Base(args[0])
	}
	parseArgs := []string{}
	if len(args) > 1 {
		parseArgs = args[1:]
	}
	flagArgs, positionalValues, err := splitCLIArgs(parseArgs)
	if err != nil {
		return cliOptions{}, err
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	emailFlag := fs.String("email", "", "")
	refreshTokenFlag := fs.String("refresh-token", "", "")
	dbPathFlag := fs.String("db-path", "", "")
	userDataDirFlag := fs.String("user-data-dir", "", "")
	noRestartFlag := fs.Bool("no-restart", false, "")

	if err := fs.Parse(flagArgs); err != nil {
		return cliOptions{}, fmt.Errorf("参数解析失败: %w", err)
	}

	positional, err := parsePositionalArgs(positionalValues)
	if err != nil {
		return cliOptions{}, err
	}

	email, err := mergeCLIValue("email", *emailFlag, positional.email)
	if err != nil {
		return cliOptions{}, err
	}
	refreshToken, err := mergeCLIValue("refresh-token", *refreshTokenFlag, positional.refreshToken)
	if err != nil {
		return cliOptions{}, err
	}
	dbPath, err := mergeCLIValue("db-path", *dbPathFlag, positional.dbPath)
	if err != nil {
		return cliOptions{}, err
	}

	dbPath, err = normalizePath(dbPath)
	if err != nil {
		return cliOptions{}, err
	}
	userDataDir, err := normalizePath(*userDataDirFlag)
	if err != nil {
		return cliOptions{}, err
	}

	return cliOptions{
		email:        email,
		refreshToken: refreshToken,
		dbPath:       dbPath,
		userDataDir:  userDataDir,
		noRestart:    *noRestartFlag,
	}, nil
}

func splitCLIArgs(args []string) ([]string, []string, error) {
	var flagArgs []string
	var positionalArgs []string

	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if arg == "--" {
			for _, value := range args[index+1:] {
				value = strings.TrimSpace(value)
				if value != "" {
					positionalArgs = append(positionalArgs, value)
				}
			}
			break
		}

		switch {
		case isBoolFlag(arg), hasInlineValueFlag(arg):
			flagArgs = append(flagArgs, arg)
		case requiresSeparateValueFlag(arg):
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return nil, nil, fmt.Errorf("参数 %s 缺少值", arg)
			}
			flagArgs = append(flagArgs, arg, strings.TrimSpace(args[index+1]))
			index++
		case strings.HasPrefix(arg, "-"):
			return nil, nil, fmt.Errorf("参数解析失败: %s", arg)
		default:
			positionalArgs = append(positionalArgs, arg)
		}
	}

	return flagArgs, positionalArgs, nil
}

func isBoolFlag(arg string) bool {
	return arg == "-no-restart" ||
		arg == "--no-restart" ||
		arg == "-h" ||
		arg == "--help" ||
		strings.HasPrefix(arg, "-no-restart=") ||
		strings.HasPrefix(arg, "--no-restart=")
}

func hasInlineValueFlag(arg string) bool {
	for _, name := range []string{"email", "refresh-token", "db-path", "user-data-dir"} {
		if strings.HasPrefix(arg, "-"+name+"=") || strings.HasPrefix(arg, "--"+name+"=") {
			return true
		}
	}
	return false
}

func requiresSeparateValueFlag(arg string) bool {
	for _, name := range []string{"email", "refresh-token", "db-path", "user-data-dir"} {
		if arg == "-"+name || arg == "--"+name {
			return true
		}
	}
	return false
}

func parsePositionalArgs(args []string) (positionalArgs, error) {
	values := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			values = append(values, arg)
		}
	}

	switch len(values) {
	case 0:
		return positionalArgs{}, nil
	case 1:
		return positionalArgs{refreshToken: values[0]}, nil
	case 2:
		if looksLikeEmail(values[0]) {
			return positionalArgs{email: values[0], refreshToken: values[1]}, nil
		}
		return positionalArgs{refreshToken: values[0], dbPath: values[1]}, nil
	case 3:
		if !looksLikeEmail(values[0]) {
			return positionalArgs{}, errors.New("位置参数格式错误，仅支持: <refresh-token> | <email> <refresh-token> | <refresh-token> <db-path> | <email> <refresh-token> <db-path>")
		}
		return positionalArgs{
			email:        values[0],
			refreshToken: values[1],
			dbPath:       values[2],
		}, nil
	default:
		return positionalArgs{}, errors.New("位置参数过多，仅支持: <refresh-token> | <email> <refresh-token> | <refresh-token> <db-path> | <email> <refresh-token> <db-path>")
	}
}

func mergeCLIValue(name, flagValue, positionalValue string) (string, error) {
	flagValue = strings.TrimSpace(flagValue)
	positionalValue = strings.TrimSpace(positionalValue)
	if flagValue == "" {
		return positionalValue, nil
	}
	if positionalValue == "" || positionalValue == flagValue {
		return flagValue, nil
	}
	return "", fmt.Errorf("参数 %s 同时通过 flag 和位置参数传入且值不一致", name)
}

func looksLikeEmail(value string) bool {
	return strings.Count(value, "@") == 1 && !strings.ContainsAny(value, `/\`)
}

func normalizePath(path string) (string, error) {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析用户目录失败: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("规范化路径失败: %w", err)
	}
	return filepath.Clean(absPath), nil
}

func isInteractiveInput(stdin io.Reader) bool {
	file, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func prompt(reader *bufio.Reader, writer io.Writer, label string) string {
	fmt.Fprintf(writer, "%s: ", label)
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
	var err error
	dbPath, err = normalizePath(dbPath)
	if err != nil {
		return "", err
	}
	if dbPath != "" {
		return dbPath, nil
	}

	if value := strings.TrimSpace(os.Getenv("ANTIGRAVITY_DB_PATH")); value != "" {
		value, err = normalizePath(value)
		if err != nil {
			return "", err
		}
		return value, nil
	}

	if userDataDir == "" {
		userDataDir = strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_DATA_DIR"))
	}
	userDataDir, err = normalizePath(userDataDir)
	if err != nil {
		return "", err
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

func validateDBPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("数据库不存在: %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("数据库路径是目录: %s", path)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer db.Close()

	var tableName string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, "ItemTable").Scan(&tableName)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("数据库缺少 ItemTable")
	}
	if err != nil {
		return fmt.Errorf("校验数据库失败: %w", err)
	}
	return nil
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

	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return "", fmt.Errorf("写入备份失败: %w", err)
	}
	if err := target.Close(); err != nil {
		return "", fmt.Errorf("关闭备份文件失败: %w", err)
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
		if offset+8 > len(data) {
			return offset, errors.New("protobuf fixed64 字段越界")
		}
		return offset + 8, nil
	case 2:
		length, next, err := readVarint(data, offset)
		if err != nil {
			return offset, err
		}
		end := next + int(length)
		if end < next || end > len(data) {
			return offset, errors.New("protobuf 字段越界")
		}
		return end, nil
	case 5:
		if offset+4 > len(data) {
			return offset, errors.New("protobuf fixed32 字段越界")
		}
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
