#!/usr/bin/env bash
# Optional: pulls new upstream Bifrost changes (maximhq/bifrost) into this fork's `dev` branch.
#
# Usage: scripts/fork/sync-upstream.sh [--no-verify] [--push]
#   --no-verify   skip scripts/fork/verify.sh after merging
#   --push        push dev to origin when done
#
# Merges upstream `dev` into the current fork branch (`dev`) with git rerere on, so a conflict
# resolved once is resolved the same way next time, and regenerates the bundled OpenAPI spec
# when it is the only conflict left.
#
# Exit codes: 0 merged or already up to date; 1 conflicts left to resolve by hand (the merge
# stays in progress: fix the files, `git commit --no-edit`, then scripts/fork/verify.sh);
# 2 usage or repository-state error; 3 merged but verification failed.
set -euo pipefail

upstream_url="https://github.com/maximhq/bifrost.git"
upstream_branch="dev"
branch="dev"
verify=true
push=false
while [ $# -gt 0 ]; do
  case "$1" in
    --no-verify) verify=false ;;
    --push) push=true ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "working tree has uncommitted changes; commit or stash them first" >&2
  exit 2
fi
if [ -e "$(git rev-parse --git-path MERGE_HEAD)" ]; then
  echo "a merge is already in progress; finish or abort it first" >&2
  exit 2
fi

if ! git remote get-url upstream >/dev/null 2>&1; then
  git remote add upstream "$upstream_url"
fi
git fetch --no-tags upstream "$upstream_branch"
upstream_ref="upstream/$upstream_branch"
upstream_sha="$(git rev-parse --short "$upstream_ref")"

git checkout -q "$branch"
git config rerere.enabled true
git config rerere.autoupdate true

if git merge-base --is-ancestor "$upstream_ref" HEAD; then
  echo "$branch already contains $upstream_ref ($upstream_sha)"
else
  merge_failed=false
  git merge --no-ff --no-edit -m "Merge upstream $upstream_branch ($upstream_sha)" "$upstream_ref" || merge_failed=true

  if [ "$merge_failed" = true ]; then
    # The bundled spec is generated from the YAML sources, so it is never merged by hand: once
    # every other conflict is resolved (the sources may be among them), it is rebuilt.
    openapi_json="docs/openapi/openapi.json"
    unresolved="$(git diff --name-only --diff-filter=U | grep -vx "$openapi_json" || true)"
    if [ -z "$unresolved" ] && git diff --name-only --diff-filter=U | grep -qx "$openapi_json"; then
      git checkout --theirs -- "$openapi_json"
      if (cd docs/openapi && python3 bundle.py >/dev/null); then
        git add "$openapi_json"
        echo "regenerated $openapi_json"
      else
        echo "could not regenerate $openapi_json (needs python3 with PyYAML)" >&2
      fi
    fi

    unresolved="$(git diff --name-only --diff-filter=U)"
    if [ -n "$unresolved" ]; then
      echo
      echo "Conflicts left to resolve (see FORK.md, 'Updating from upstream'):"
      while IFS= read -r file; do
        echo "  $file"
      done <<<"$unresolved"
      echo
      echo "Resolve them (regenerate $openapi_json with 'cd docs/openapi && python3 bundle.py' last),"
      echo "then: git add <files> && git commit --no-edit && scripts/fork/verify.sh"
      exit 1
    fi
    git commit --no-edit
  fi
  echo "merged $upstream_ref ($upstream_sha) into $branch"
fi

if [ "$verify" = true ]; then
  if ! scripts/fork/verify.sh; then
    echo "merged, but fork verification failed; fix it before pushing" >&2
    exit 3
  fi
fi

if [ "$push" = true ]; then
  git push origin "$branch"
fi
