#!/usr/bin/env bash
# Usage: scripts/deploy-preflight.sh [--force]
# Called first by the host deploy.sh (M3-X2 adds the call). Asks the RUNNING instance for its live
# generation attempts (`metamusic runpod status --json` inside the container) and exits non-zero while
# any exist: a deploy would strand a paid render (D-M3-33). It refuses as well while a pod is live
# (`live_pods` > 0, C396.6): a deploy restarts the app and a pod outside it would be billed and forgotten.
# A status with no `live_pods` field is an instance older than the pod backend: it has no pod, so it counts 0.
# Runbook order: disarm -> drain (wait until `status` shows no live attempt and no live pod) -> deploy -> arm.
# --force proceeds anyway (operator override).
# Without --force a status that cannot be read or has no `live_attempts` field fails closed; with --force
# everything proceeds, saying so on stderr.
# MM_PREFLIGHT_STATUS_CMD overrides the status command (tests; the host's container name).
set -euo pipefail
force=0
for a in "$@"; do
  case "$a" in
    --force) force=1 ;;
    *) echo "usage: $0 [--force]" >&2; exit 2 ;;
  esac
done
cmd=${MM_PREFLIGHT_STATUS_CMD:-docker exec metamusic metamusic runpod status --json}
if ! out=$(bash -c "$cmd" 2>/dev/null); then
  if [ "$force" = 1 ]; then
    echo "deploy-preflight: status command failed; proceeding because of --force" >&2
    exit 0
  fi
  echo "deploy-preflight: cannot read provider status (is the instance up?); refusing (use --force to override)" >&2
  exit 1
fi
n=$(printf '%s' "$out" | jq -er '.live_attempts | if type == "array" then length elif type == "number" then . else error("bad") end' 2>/dev/null) || {
  if [ "$force" = 1 ]; then
    echo "deploy-preflight: status unreadable; proceeding because of --force" >&2
    exit 0
  fi
  echo "deploy-preflight: status has no readable live_attempts; refusing (use --force to override)" >&2
  exit 1
}
if [ "$n" -gt 0 ]; then
  if [ "$force" = 1 ]; then
    echo "deploy-preflight: $n live attempt(s); proceeding because of --force" >&2
    exit 0
  fi
  echo "deploy-preflight: $n live attempt(s) in flight; a deploy would strand paid renders." >&2
  echo "deploy-preflight: disarm, wait until 'runpod status' shows none, deploy, then arm (or --force)." >&2
  exit 1
fi
pods=$(printf '%s' "$out" | jq -er 'if has("live_pods") then (.live_pods | if type == "array" then length elif type == "number" then . else error("bad") end) else 0 end' 2>/dev/null) || {
  if [ "$force" = 1 ]; then
    echo "deploy-preflight: live_pods unreadable; proceeding because of --force" >&2
    exit 0
  fi
  echo "deploy-preflight: status has an unreadable live_pods; refusing (use --force to override)" >&2
  exit 1
}
if [ "$pods" -gt 0 ]; then
  if [ "$force" = 1 ]; then
    echo "deploy-preflight: $pods live pod(s); proceeding because of --force" >&2
    exit 0
  fi
  echo "deploy-preflight: $pods live pod(s) at RunPod; a deploy would strand billed pods." >&2
  echo "deploy-preflight: disarm runpod-pod, wait until 'runpod status --backend runpod-pod' shows none, deploy, then arm (or --force)." >&2
  exit 1
fi
echo "deploy-preflight: no live attempts, no live pods"
