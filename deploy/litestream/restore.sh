#!/usr/bin/env bash
# One-command disaster recovery for the Flow API SQLite database.
#
# Restores the latest Litestream replica (or a point-in-time snapshot) from
# Cloudflare R2, verifies integrity with sqlite3 when available, and replaces
# the live database atomically. Docker is not involved.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CONFIG="${SCRIPT_DIR}/litestream.yml"
DEFAULT_ENV_FILE="/etc/flow-api/litestream.env"
DEFAULT_APP_ENV_FILE="/etc/flow-api/flow-api.env"

CONFIG="$DEFAULT_CONFIG"
ENV_FILE="$DEFAULT_ENV_FILE"
APP_ENV_FILE="$DEFAULT_APP_ENV_FILE"
OUTPUT=""
OUTPUT_EXPLICIT=0
TIMESTAMP=""
FORCE=0
TMP_DB=""
BACKUP_DB=""
BACKUP_WAL=""
BACKUP_SHM=""
SERVICE_NAME="flow-api.service"
LITESTREAM_SERVICE_NAME="litestream.service"

usage() {
  cat <<'EOF'
Usage: restore.sh [options]

Restore the Flow API SQLite database from its Litestream replica.

Options:
  --config PATH        Litestream config (default: litestream.yml next to this script)
  --env-file PATH      Litestream/R2 environment file
                       (default: /etc/flow-api/litestream.env)
  --app-env-file PATH  Application environment file, read only for DB_PATH when
                       it exists and is readable
                       (default: /etc/flow-api/flow-api.env)
  --output PATH        Destination database path (default: $LITESTREAM_DB_PATH).
                       Required when DB_PATH and LITESTREAM_DB_PATH differ.
  --timestamp TIME     Point-in-time recovery timestamp (RFC 3339, for example
                       2026-05-01T12:30:00Z). Defaults to the latest replica.
  --force              Replace an existing database. Existing -wal/-shm files are
                       moved aside first and restored if the replacement fails.
  -h, --help           Show this help.

Required environment (from the env file or the current shell):
  LITESTREAM_DB_PATH, R2_ACCOUNT_ID, R2_BUCKET, R2_PREFIX,
  R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY

Env files are parsed literally. Blank lines and lines starting with # are
ignored, an optional leading "export " is accepted, and matching outer single
or double quotes are stripped. Values are never evaluated, so command
substitution and variable expansion do not run. Inline comments and line
continuations are not supported. Variables already set in the environment are
kept and are not overwritten by the env file.

The flow-api and litestream services must be stopped before restoring.
EOF
}

log() {
  printf '[restore] %s\n' "$*"
}

warn() {
  printf '[restore] warning: %s\n' "$*" >&2
}

die() {
  printf '[restore] error: %s\n' "$*" >&2
  exit 1
}

# Read KEY=value lines without sourcing the file. Values are assigned with
# printf -v and exported, never evaluated by the shell. Variables that already
# exist in the process environment keep their value.
load_env_file() {
  local file="$1"
  local line key value first last

  [ -f "$file" ] || die "environment file not found: $file"
  [ -r "$file" ] || die "environment file is not readable: $file"
  log "loading environment from $file"

  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"

    case "$line" in
      '' | '#'*)
        continue
        ;;
    esac

    # Require at least one whitespace so exportFOO=bar is not mistaken for an
    # export prefix.
    if [[ "$line" =~ ^export[[:space:]]+ ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi

    case "$line" in
      *=*) ;;
      *)
        warn "skipping malformed line in $file: $line"
        continue
        ;;
    esac

    key="${line%%=*}"
    value="${line#*=}"
    key="${key%"${key##*[![:space:]]}"}"

    if ! [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      warn "skipping invalid variable name in $file: $key"
      continue
    fi

    if [ "${#value}" -ge 2 ]; then
      first="${value:0:1}"
      last="${value: -1}"
      if { [ "$first" = "'" ] && [ "$last" = "'" ]; } ||
        { [ "$first" = '"' ] && [ "$last" = '"' ]; }; then
        value="${value:1:${#value}-2}"
      fi
    fi

    if declare -p "$key" >/dev/null 2>&1; then
      continue
    fi

    printf -v "$key" '%s' "$value"
    export "$key"
  done <"$file"
}

# Print one variable from an env file in a subshell so the application env file
# cannot leak unrelated variables into this process.
read_env_value() {
  local file="$1"
  local name="$2"

  (
    load_env_file "$file" >/dev/null
    if declare -p "$name" >/dev/null 2>&1; then
      printf '%s' "${!name}"
    fi
  )
}

restore_backups() {
  local failed=0

  if [ -n "$BACKUP_DB" ] && [ -e "$BACKUP_DB" ]; then
    mv -f -- "$BACKUP_DB" "$OUTPUT" || failed=1
  fi
  if [ -n "$BACKUP_WAL" ] && [ -e "$BACKUP_WAL" ]; then
    mv -f -- "$BACKUP_WAL" "${OUTPUT}-wal" || failed=1
  fi
  if [ -n "$BACKUP_SHM" ] && [ -e "$BACKUP_SHM" ]; then
    mv -f -- "$BACKUP_SHM" "${OUTPUT}-shm" || failed=1
  fi

  if [ "$failed" -eq 0 ]; then
    BACKUP_DB=""
    BACKUP_WAL=""
    BACKUP_SHM=""
  fi

  return "$failed"
}

cleanup() {
  local status=$?

  trap - EXIT

  if [ -n "$TMP_DB" ] && [ -e "$TMP_DB" ]; then
    rm -f -- "$TMP_DB" "${TMP_DB}-wal" "${TMP_DB}-shm" 2>/dev/null || true
  fi

  if [ -n "$BACKUP_DB" ] || [ -n "$BACKUP_WAL" ] || [ -n "$BACKUP_SHM" ]; then
    if [ -e "$OUTPUT" ]; then
      [ -z "$BACKUP_DB" ] || rm -f -- "$BACKUP_DB"
      [ -z "$BACKUP_WAL" ] || rm -f -- "$BACKUP_WAL"
      [ -z "$BACKUP_SHM" ] || rm -f -- "$BACKUP_SHM"
    elif ! restore_backups; then
      warn "original database files could not be restored; backups remain in ${OUTPUT_DIR:-$OUTPUT}"
    fi
  fi

  exit "$status"
}

trap cleanup EXIT

while [ "$#" -gt 0 ]; do
  case "$1" in
    --config)
      [ "$#" -ge 2 ] || die "--config requires a path"
      CONFIG="$2"
      shift 2
      ;;
    --config=*)
      CONFIG="${1#*=}"
      shift
      ;;
    --env-file)
      [ "$#" -ge 2 ] || die "--env-file requires a path"
      ENV_FILE="$2"
      shift 2
      ;;
    --env-file=*)
      ENV_FILE="${1#*=}"
      shift
      ;;
    --app-env-file)
      [ "$#" -ge 2 ] || die "--app-env-file requires a path"
      APP_ENV_FILE="$2"
      shift 2
      ;;
    --app-env-file=*)
      APP_ENV_FILE="${1#*=}"
      shift
      ;;
    --output)
      [ "$#" -ge 2 ] || die "--output requires a path"
      OUTPUT="$2"
      OUTPUT_EXPLICIT=1
      shift 2
      ;;
    --output=*)
      OUTPUT="${1#*=}"
      OUTPUT_EXPLICIT=1
      shift
      ;;
    --timestamp)
      [ "$#" -ge 2 ] || die "--timestamp requires an RFC 3339 value"
      TIMESTAMP="$2"
      shift 2
      ;;
    --timestamp=*)
      TIMESTAMP="${1#*=}"
      shift
      ;;
    --force)
      FORCE=1
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1 (use --help)"
      ;;
  esac
done

[ -n "$CONFIG" ] || die "a Litestream config path is required"
[ -f "$CONFIG" ] || die "Litestream config not found: $CONFIG"
[ -n "$ENV_FILE" ] || die "an environment file path is required"

load_env_file "$ENV_FILE"

command -v litestream >/dev/null 2>&1 || die "litestream binary not found in PATH"
command -v sqlite3 >/dev/null 2>&1 || warn "sqlite3 not installed; integrity check will be skipped"

required_vars=(
  R2_ACCOUNT_ID
  R2_BUCKET
  R2_PREFIX
  R2_ACCESS_KEY_ID
  R2_SECRET_ACCESS_KEY
  LITESTREAM_DB_PATH
)

missing=()
for name in "${required_vars[@]}"; do
  value="${!name:-}"
  if [ -z "$value" ]; then
    missing+=("$name")
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  die "missing required environment variables: ${missing[*]}"
fi

if [ -z "$OUTPUT" ]; then
  OUTPUT="$LITESTREAM_DB_PATH"
fi

[ -n "$OUTPUT" ] || die "destination database path is empty"

DB_PATH_VALUE="${DB_PATH:-}"
if [ -z "$DB_PATH_VALUE" ] && [ -n "$APP_ENV_FILE" ] && [ -f "$APP_ENV_FILE" ] && [ -r "$APP_ENV_FILE" ]; then
  DB_PATH_VALUE="$(read_env_value "$APP_ENV_FILE" DB_PATH)" ||
    die "failed to read DB_PATH from $APP_ENV_FILE"
fi

if [ -n "$DB_PATH_VALUE" ] && [ -n "$LITESTREAM_DB_PATH" ] &&
  [ "$DB_PATH_VALUE" != "$LITESTREAM_DB_PATH" ] && [ "$OUTPUT_EXPLICIT" -ne 1 ]; then
  die "DB_PATH ($DB_PATH_VALUE) and LITESTREAM_DB_PATH ($LITESTREAM_DB_PATH) differ; pass --output PATH to choose the destination explicitly"
fi

if [ -d "$OUTPUT" ]; then
  die "destination is a directory, not a database file: $OUTPUT"
fi

if [ -e "$OUTPUT" ] && [ "$FORCE" -ne 1 ]; then
  die "refusing to overwrite existing database: $OUTPUT (pass --force to replace it)"
fi

if command -v systemctl >/dev/null 2>&1; then
  active=()
  for unit in "$SERVICE_NAME" "$LITESTREAM_SERVICE_NAME"; do
    if systemctl is-active --quiet "$unit"; then
      active+=("$unit")
    fi
  done
  if [ "${#active[@]}" -gt 0 ]; then
    die "services are running: ${active[*]}. Stop them first: systemctl stop ${LITESTREAM_SERVICE_NAME} ${SERVICE_NAME}"
  fi
fi

OUTPUT_DIR="$(dirname -- "$OUTPUT")"
if [ ! -d "$OUTPUT_DIR" ]; then
  mkdir -p "$OUTPUT_DIR" || die "cannot create destination directory: $OUTPUT_DIR"
fi
[ -w "$OUTPUT_DIR" ] || die "destination directory is not writable: $OUTPUT_DIR"

umask 077
TMP_DB="${OUTPUT}.restore.$$.tmp"
rm -f -- "$TMP_DB" "${TMP_DB}-wal" "${TMP_DB}-shm"

log "config: $CONFIG"
log "database in replica: $LITESTREAM_DB_PATH"
log "destination: $OUTPUT"
if [ -n "$TIMESTAMP" ]; then
  log "point-in-time: $TIMESTAMP"
else
  log "point-in-time: latest"
fi

if [ -n "$TIMESTAMP" ]; then
  if ! litestream restore -config "$CONFIG" -o "$TMP_DB" -timestamp "$TIMESTAMP" "$LITESTREAM_DB_PATH"; then
    die "litestream restore failed"
  fi
else
  if ! litestream restore -config "$CONFIG" -o "$TMP_DB" "$LITESTREAM_DB_PATH"; then
    die "litestream restore failed"
  fi
fi

[ -s "$TMP_DB" ] || die "litestream produced an empty database: $TMP_DB"

if command -v sqlite3 >/dev/null 2>&1; then
  integrity="$(sqlite3 "$TMP_DB" "PRAGMA integrity_check;")"
  if [ "$integrity" != "ok" ]; then
    die "sqlite3 integrity_check failed: $integrity"
  fi
  log "integrity_check: ok"
fi

rm -f -- "${TMP_DB}-wal" "${TMP_DB}-shm"

# Move the current database and its WAL/SHM sidecars to backup paths in the
# same directory. Nothing is deleted until the restored database is in place.
if [ -e "$OUTPUT" ] || [ -e "${OUTPUT}-wal" ] || [ -e "${OUTPUT}-shm" ]; then
  log "replacing existing database; WAL/SHM files are kept until the new database is in place"
  BACKUP_DB="${OUTPUT}.restore.$$.bak"
  BACKUP_WAL="${OUTPUT}-wal.restore.$$.bak"
  BACKUP_SHM="${OUTPUT}-shm.restore.$$.bak"
  rm -f -- "$BACKUP_DB" "$BACKUP_WAL" "$BACKUP_SHM"

  if [ -e "$OUTPUT" ]; then
    mv -f -- "$OUTPUT" "$BACKUP_DB" || die "failed to move existing database aside: $OUTPUT"
  fi
  if [ -e "${OUTPUT}-wal" ]; then
    mv -f -- "${OUTPUT}-wal" "$BACKUP_WAL" || die "failed to move existing WAL aside: ${OUTPUT}-wal"
  fi
  if [ -e "${OUTPUT}-shm" ]; then
    mv -f -- "${OUTPUT}-shm" "$BACKUP_SHM" || die "failed to move existing SHM aside: ${OUTPUT}-shm"
  fi
fi

if mv -f -- "$TMP_DB" "$OUTPUT"; then
  TMP_DB=""
  rm -f -- "$BACKUP_DB" "$BACKUP_WAL" "$BACKUP_SHM"
  BACKUP_DB=""
  BACKUP_WAL=""
  BACKUP_SHM=""
else
  restore_backups || warn "could not restore the original database files; backups remain in $OUTPUT_DIR"
  die "failed to move restored database into place"
fi

chmod 0640 "$OUTPUT" 2>/dev/null || warn "could not set mode 0640 on $OUTPUT"

if [ "$(id -u)" -eq 0 ] && id -u flow-api >/dev/null 2>&1; then
  FLOW_GROUP="$(id -gn flow-api 2>/dev/null || printf '%s' flow-api)"
  if chown "flow-api:${FLOW_GROUP}" "$OUTPUT"; then
    log "ownership: flow-api:${FLOW_GROUP}"
  else
    warn "restored database is not owned by flow-api; fix it before starting the service"
  fi
fi

log "restore complete: $OUTPUT"
cat <<EOF

Next steps:
  systemctl start ${SERVICE_NAME}
  systemctl start ${LITESTREAM_SERVICE_NAME}
  curl -fsS http://127.0.0.1:8080/healthz

Verify memberships, balances, and recent audit rows before re-enabling traffic.
EOF
