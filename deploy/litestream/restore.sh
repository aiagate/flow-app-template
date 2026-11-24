#!/usr/bin/env bash
# One-command disaster recovery for the Flow API SQLite database.
#
# Restores the latest Litestream replica (or a point-in-time snapshot) from
# Cloudflare R2, verifies integrity with sqlite3 when available, and replaces
# the live database atomically. Docker is not involved.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CONFIG="${SCRIPT_DIR}/litestream.yml"

CONFIG="$DEFAULT_CONFIG"
ENV_FILE=""
OUTPUT=""
TIMESTAMP=""
FORCE=0
TMP_DB=""
SERVICE_NAME="flow-api.service"
LITESTREAM_SERVICE_NAME="litestream.service"

usage() {
  cat <<'EOF'
Usage: restore.sh [options]

Restore the Flow API SQLite database from its Litestream replica.

Options:
  --config PATH       Litestream config (default: litestream.yml next to this script)
  --env-file PATH     Environment file to load. When omitted, the script looks for
                      /etc/flow-api/flow-api.env, then deploy/systemd/flow-api.env,
                      then ../../.env, then ./.env.
  --output PATH       Destination database path (default: $LITESTREAM_DB_PATH)
  --timestamp TIME    Point-in-time recovery timestamp (RFC 3339, for example
                      2026-05-01T12:30:00Z). Defaults to the latest replica.
  --force             Replace an existing database, removing stale -wal/-shm files.
  -h, --help          Show this help.

Required environment (from the env file or the current shell):
  LITESTREAM_DB_PATH, R2_ACCOUNT_ID, R2_BUCKET, R2_PREFIX,
  R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY

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

cleanup() {
  if [ -n "$TMP_DB" ] && [ -e "$TMP_DB" ]; then
    rm -f "$TMP_DB" "${TMP_DB}-wal" "${TMP_DB}-shm"
  fi
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
    --output)
      [ "$#" -ge 2 ] || die "--output requires a path"
      OUTPUT="$2"
      shift 2
      ;;
    --output=*)
      OUTPUT="${1#*=}"
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
    -h|--help)
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

load_env_file() {
  local file="$1"
  [ -f "$file" ] || die "environment file not found: $file"
  log "loading environment from $file"
  set -a
  # shellcheck disable=SC1090
  . "$file"
  set +a
}

if [ -n "$ENV_FILE" ]; then
  load_env_file "$ENV_FILE"
else
  for candidate in \
    "/etc/flow-api/flow-api.env" \
    "${SCRIPT_DIR}/../systemd/flow-api.env" \
    "${SCRIPT_DIR}/../../.env" \
    "./.env"; do
    if [ -f "$candidate" ]; then
      load_env_file "$candidate"
      break
    fi
  done
fi

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
rm -f "$TMP_DB" "${TMP_DB}-wal" "${TMP_DB}-shm"

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

rm -f "${TMP_DB}-wal" "${TMP_DB}-shm"

if [ -e "$OUTPUT" ]; then
  log "replacing existing database and clearing stale WAL/SHM files"
  rm -f "${OUTPUT}-wal" "${OUTPUT}-shm"
fi

mv -f "$TMP_DB" "$OUTPUT" || die "failed to move restored database into place"
TMP_DB=""

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
