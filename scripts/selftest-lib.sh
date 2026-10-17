# Sourced by selftests that build scratch git repositories. Not executable on its own.
#
# Git worktrees share one .git/config, and a git hook exports GIT_DIR/GIT_INDEX_FILE. A selftest that configures or commits
# in what it believes is its scratch repository must prove it is not touching the real one: a stray `git config user.name`
# once landed in every lane's identity.

# assert_scratch_repo <dir> [<root>]: the repository git sees from the current directory must be <dir>'s own .git, and <dir>
# must be outside the real repository <root> (default: the repository this script lives in). Exits 2 otherwise.
assert_scratch_repo() {
  local dir=$1 want got top
  want=$(realpath "$dir/.git") || exit 2
  got=$(realpath "$(git rev-parse --git-dir 2>/dev/null)" 2>/dev/null) || got=
  if [ "$got" != "$want" ]; then
    echo "selftest: refusing to continue: git operates on '${got:-nothing}', not on the scratch repository '$want'" >&2
    exit 2
  fi
  top=$(realpath "${2:-$(dirname "${BASH_SOURCE[0]}")/..}")
  case "$want" in
    "$top"/*) echo "selftest: refusing to continue: the scratch repository '$want' is inside the real repository '$top'" >&2; exit 2 ;;
  esac
}
