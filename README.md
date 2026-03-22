# antigravity-switcher

[中文 README](./README.zh-CN.md)

`antigravity-switcher` is a small Go CLI that updates the Google OAuth data stored in Antigravity's local `state.vscdb` database.

It takes a valid Google `refresh_token`, exchanges it for a new `access_token`, updates the relevant records in the Antigravity database, creates a backup, and restarts Antigravity when needed.

## Features

- Exchanges a Google `refresh_token` for a fresh `access_token`
- Detects the account email automatically when possible
- Locates `state.vscdb` automatically on macOS, Linux, and Windows
- Supports both known Antigravity storage formats
- Creates a timestamped `.bak` backup before writing
- Stops and restarts Antigravity automatically unless disabled

## Requirements

- Go `1.26` or later for local builds
- A valid Google `refresh_token`
- Access to the local Antigravity user data directory

## Build

```bash
make build
```

The binary is written to `dist/`.

To build release binaries for all configured targets:

```bash
make release
```

## Test

```bash
make test
```

## Usage

```bash
./dist/antigravity-switcher-<os>-<arch> [flags]
./dist/antigravity-switcher-<os>-<arch> <refresh-token>
./dist/antigravity-switcher-<os>-<arch> <email> <refresh-token>
./dist/antigravity-switcher-<os>-<arch> <refresh-token> <db-path>
./dist/antigravity-switcher-<os>-<arch> <email> <refresh-token> <db-path>
```

### Flags

- `--email`: account email; optional when it can be fetched from Google user info
- `--refresh-token`: Google refresh token
- `--db-path`: absolute or relative path to `state.vscdb`
- `--user-data-dir`: Antigravity user data directory; the program appends `User/globalStorage/state.vscdb`
- `--no-restart`: do not stop or restart Antigravity

### Environment variables

- `ANTIGRAVITY_DB_PATH`: explicit database path override
- `ANTIGRAVITY_USER_DATA_DIR`: Antigravity user data directory override
- `ANTIGRAVITY_APP_PATH`: explicit Antigravity application path override for restart and portable path detection

## Examples

Use only a refresh token and let the program resolve email and database path automatically:

```bash
./dist/antigravity-switcher-darwin-arm64 your-refresh-token
```

Pass everything explicitly:

```bash
./dist/antigravity-switcher-darwin-arm64 \
  --email user@example.com \
  --refresh-token your-refresh-token \
  --db-path ~/Library/Application\ Support/Antigravity/User/globalStorage/state.vscdb
```

Use a custom user data directory without restarting the app:

```bash
./dist/antigravity-switcher-darwin-arm64 \
  --refresh-token your-refresh-token \
  --user-data-dir /path/to/Antigravity/data/user-data \
  --no-restart
```

## Database resolution

If `--db-path` is not provided, the program tries the following sources:

- The current Antigravity process command line, if it contains `--user-data-dir`
- `ANTIGRAVITY_DB_PATH`
- `--user-data-dir` or `ANTIGRAVITY_USER_DATA_DIR`
- A portable install layout under the detected application path
- The default Antigravity path for the current operating system

## Behavior

- A backup named like `state.vscdb.YYYYMMDDHHMMSS.bak` is created before any write
- The tool updates the new storage format key and the legacy format key when present
- `antigravityOnboarding=true` is written after token injection
- In interactive terminals, missing values can be prompted instead of failing immediately

## License

This project is licensed under the Apache License 2.0. See [LICENSE](./LICENSE).
