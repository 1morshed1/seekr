#!/usr/bin/env bash
#
# demo.sh — spin up a local 3-shard seekr cluster, index sample documents, and
# run a few searches through the coordinator. This is the fastest way to see the
# whole system working end to end.
#
# Usage:
#   ./scripts/demo.sh
#
# It builds the binaries, starts 3 shards + 1 coordinator in the background,
# indexes docs from testdata/sample_docs.json, runs example queries, and cleans
# everything up on exit.

set -euo pipefail
cd "$(dirname "$0")/.."

COORD_ADDR=":8080"
COORD_URL="http://localhost:8080"
SHARD_PORTS=(9001 9002 9003)
SHARD_URLS="http://localhost:9001,http://localhost:9002,http://localhost:9003"

PIDS=()
cleanup() {
  echo
  echo "==> shutting down cluster"
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

echo "==> building binaries"
mkdir -p bin
go build -o bin/seekr-shard ./cmd/seekr-shard
go build -o bin/seekr-coordinator ./cmd/seekr-coordinator

echo "==> starting shards on ${SHARD_PORTS[*]}"
i=0
for port in "${SHARD_PORTS[@]}"; do
  ./bin/seekr-shard --id "$i" --addr ":$port" &
  PIDS+=($!)
  i=$((i + 1))
done

echo "==> starting coordinator on ${COORD_ADDR}"
./bin/seekr-coordinator --addr "$COORD_ADDR" --shards "$SHARD_URLS" &
PIDS+=($!)

# Give servers a moment to bind their ports.
sleep 1

echo "==> indexing sample documents"
# Each line of the sample file is one document JSON object; wrap it as an index
# request and POST it to the coordinator, which routes it to the owning shard.
while IFS= read -r doc; do
  [ -z "$doc" ] && continue
  curl -s -X POST "$COORD_URL/api/v1/index" \
    -H 'Content-Type: application/json' \
    -d "{\"document\": $doc}" >/dev/null
done < testdata/sample_docs.jsonl

echo "==> cluster stats (documents per shard)"
curl -s "$COORD_URL/api/v1/stats" | sed 's/,/,\n  /g'
echo

run_query() {
  local q="$1"
  echo
  echo "----------------------------------------------------------------------"
  echo "QUERY: $q"
  echo "----------------------------------------------------------------------"
  curl -s -G "$COORD_URL/api/v1/search" --data-urlencode "q=$q" --data-urlencode "k=3" \
    | python3 -c '
import sys, json
d = json.load(sys.stdin)
results = d.get("results", [])
if not results:
    print("  (no results)")
for r in results:
    title = r["document"]["fields"].get("title", "")
    print("  {:.4f}  {:12}  {}".format(r["score"], r["doc_id"], title))
'
}

run_query "distributed systems"
run_query "kafka OR rabbitmq"
run_query "database AND storage"
run_query "\"write ahead log\""
run_query "deploy*"
run_query "distributed NOT python"

echo
echo "==> demo complete"
