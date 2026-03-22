# antigravity-switcher

[English README](./README.md)

`antigravity-switcher` 是一个 Go 命令行工具，用来更新 Antigravity 本地 `state.vscdb` 数据库中的 Google OAuth 数据。

它会使用有效的 Google `refresh_token` 换取新的 `access_token`，更新 Antigravity 数据库中的相关记录，先创建备份，再按需重启 Antigravity。

## 功能

- 用 Google `refresh_token` 换取新的 `access_token`
- 尽可能自动获取账号邮箱
- 在 macOS、Linux、Windows 上自动定位 `state.vscdb`
- 兼容已知的两种 Antigravity 存储格式
- 写入前自动创建带时间戳的 `.bak` 备份
- 默认自动关闭并重启 Antigravity，可显式关闭

## 依赖

- 本地构建需要 Go `1.26` 或更高版本
- 可用的 Google `refresh_token`
- 对本机 Antigravity 用户数据目录的访问权限

## 构建

```bash
make build
```

产物会输出到 `dist/`。

构建所有预设发布目标：

```bash
make release
```

## 测试

```bash
make test
```

## 用法

```bash
./dist/antigravity-switcher-<os>-<arch> [flags]
./dist/antigravity-switcher-<os>-<arch> <refresh-token>
./dist/antigravity-switcher-<os>-<arch> <email> <refresh-token>
./dist/antigravity-switcher-<os>-<arch> <refresh-token> <db-path>
./dist/antigravity-switcher-<os>-<arch> <email> <refresh-token> <db-path>
```

### 参数

- `--email`：账号邮箱；如果可以通过 Google user info 自动获取则可省略
- `--refresh-token`：Google refresh token
- `--db-path`：`state.vscdb` 的绝对或相对路径
- `--user-data-dir`：Antigravity 用户数据目录；程序会自动拼接 `User/globalStorage/state.vscdb`
- `--no-restart`：不关闭也不重启 Antigravity

### 环境变量

- `ANTIGRAVITY_DB_PATH`：显式指定数据库路径
- `ANTIGRAVITY_USER_DATA_DIR`：显式指定 Antigravity 用户数据目录
- `ANTIGRAVITY_APP_PATH`：显式指定 Antigravity 应用路径，用于重启和便携版路径探测

## 示例

只传 `refresh_token`，让程序自动解析邮箱和数据库路径：

```bash
./dist/antigravity-switcher-darwin-arm64 your-refresh-token
```

显式传入全部参数：

```bash
./dist/antigravity-switcher-darwin-arm64 \
  --email user@example.com \
  --refresh-token your-refresh-token \
  --db-path ~/Library/Application\ Support/Antigravity/User/globalStorage/state.vscdb
```

使用自定义用户目录且不重启应用：

```bash
./dist/antigravity-switcher-darwin-arm64 \
  --refresh-token your-refresh-token \
  --user-data-dir /path/to/Antigravity/data/user-data \
  --no-restart
```

## 数据库定位顺序

未提供 `--db-path` 时，程序会依次尝试：

- 当前运行中的 Antigravity 进程命令行中的 `--user-data-dir`
- `ANTIGRAVITY_DB_PATH`
- `--user-data-dir` 或 `ANTIGRAVITY_USER_DATA_DIR`
- 检测到的应用路径下的便携版目录结构
- 当前操作系统的默认 Antigravity 路径

## 行为说明

- 写入前会先创建形如 `state.vscdb.YYYYMMDDHHMMSS.bak` 的备份
- 新格式和旧格式的认证记录都会在可用时更新
- 注入 token 后会写入 `antigravityOnboarding=true`
- 在交互式终端里，缺少必要参数时会尝试提示输入

## 许可证

本项目基于 Apache License 2.0 开源，详见 [LICENSE](./LICENSE)。
