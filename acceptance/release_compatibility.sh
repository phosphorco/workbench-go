#!/usr/bin/env bash
set -euo pipefail

runner_temp="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
results_root="$runner_temp/workbench-go-release-compatibility-results"
mkdir -p "$results_root"
printf '%s\n' 'compatibility expectations: legacy build/seal/verify/check-fresh/materialize exit codes must match (0 success, 1 refusal); only typed resolve/receipt and digest-only public JSON may differ.'
printf '%s\n' 'compatibility defects: any changed legacy acceptance or any five-command exit mismatch fails this gate.'

write_workbench() {
  local root="$1"
  cat > "$root/workbench.pkl" <<'EOF'
amends "package://github.com/phosphorco/workbench-go/releases/download/0.6.0/workbench@0.6.0#/Repository.pkl"
buildables {
  ["compat"] = new Buildable {
    inputDetection = new GitHeadTreeInputDetection { paths { "producer.txt" } }
    buildCommand = new BuildCommand { executable = "true" }
    manifest = new ManifestContract {
      schemaVersion = 1
      kind = "compatibility-manifest"
      contractId = "compatibility-v1"
      expectedSource { ["repository"] = "synthetic/compatibility" }
      requiredSourceFields { "revision" }
      requiredCapabilities { "compatibility-v1" }
    }
    candidates {
      new BuildableCandidate { root = ".local-build/compat"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild the local compatibility candidate." }
      new BuildableCandidate { root = ".ci-build/compat"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore the committed compatibility candidate." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput {
        os { "linux" }
        arch { "amd64" }
        outputs { new BuildableOutput { path = "compat.bin"; destination = "bin/compat"; kind = "executable"; executable = true } }
      }
    }
  }
}
EOF
}

prepare_fixture() {
  local root="$1"
  local valid="$2"
  local candidate="$root/.local-build/compat"
  mkdir -p "$candidate"
  write_workbench "$root"
  printf 'producer\n' > "$root/producer.txt"
  if [ "$valid" = true ]; then
    printf '#!/bin/sh\nprintf compat\\n\n' > "$candidate/compat.bin"
    chmod 0755 "$candidate/compat.bin"
    printf '%s\n' '{"source":{"repository":"synthetic/compatibility","revision":"fixture"},"capabilities":["compatibility-v1"]}' > "$candidate/.workbench-buildable-source.json"
  fi
  git -C "$root" init -q -b main
  git -C "$root" config user.name "Workbench release compatibility"
  git -C "$root" config user.email "workbench-release@example.invalid"
  git -C "$root" add producer.txt workbench.pkl
  git -C "$root" commit -qm fixture
  git -C "$root" update-ref refs/remotes/origin/main HEAD
}

invoke() {
  local version="$1"
  local root="$2"
  shift 2
  (cd "$root" && mise exec "github:phosphorco/workbench-go@$version" -- workbench "$@")
}

prepare_case() {
  local version="$1"
  local operation="$2"
  local success="$3"
  local root
  root="$(mktemp -d "$runner_temp/workbench-go-release-compatibility-${version}-${operation}-${success}.XXXXXX")"
  if [ "$success" = false ] && { [ "$operation" = build ] || [ "$operation" = seal ]; }; then
    prepare_fixture "$root" false
  else
    prepare_fixture "$root" true
  fi

  if [ "$operation" != build ] && [ "$success" = true ]; then
    invoke "$version" "$root" buildable seal --name compat --candidate-root .local-build/compat
  fi
  if [ "$operation" != build ] && [ "$success" = false ]; then
    case "$operation" in
      seal)
        :
        ;;
      verify|materialize)
        invoke "$version" "$root" buildable seal --name compat --candidate-root .local-build/compat
        printf 'tampered\n' > "$root/.local-build/compat/compat.bin"
        ;;
      check-fresh)
        invoke "$version" "$root" buildable seal --name compat --candidate-root .local-build/compat
        old_head="$(git -C "$root" rev-parse HEAD)"
        printf 'changed producer\n' > "$root/producer.txt"
        git -C "$root" add producer.txt
        git -C "$root" commit -qm "producer changed"
        git -C "$root" update-ref refs/remotes/origin/main "$old_head"
        printf 'producer\n' > "$root/producer.txt"
        ;;
    esac
  fi
  printf '%s\n' "$root"
}

run_case() {
  local version="$1"
  local label="$2"
  local root="$3"
  shift 3
  local output="$results_root/${version}-${label}.stdout"
  local error="$results_root/${version}-${label}.stderr"
  local status
  set +e
  invoke "$version" "$root" "$@" >"$output" 2>"$error"
  status=$?
  set -e
  printf '%s\n' "$status" > "$results_root/${version}-${label}.status"
  wc -c < "$output" | tr -d ' ' > "$results_root/${version}-${label}.bytes"
}

run_version() {
  local version="$1"
  export HOME="$runner_temp/workbench-go-release-compatibility-${version}-home"
  export MISE_DATA_DIR="$runner_temp/workbench-go-release-compatibility-${version}-data"
  export MISE_CACHE_DIR="$runner_temp/workbench-go-release-compatibility-${version}-cache"
  export MISE_STATE_DIR="$runner_temp/workbench-go-release-compatibility-${version}-state"
  export MISE_CONFIG_DIR="$runner_temp/workbench-go-release-compatibility-${version}-config"
  export GH_TOKEN=""
  export GITHUB_TOKEN=""
  export MISE_GITHUB_GH_CLI_TOKENS="false"
  export MISE_GITHUB_USE_GIT_CREDENTIALS="false"
  mkdir -p "$HOME" "$MISE_DATA_DIR" "$MISE_CACHE_DIR" "$MISE_STATE_DIR" "$MISE_CONFIG_DIR"
  mise install "github:phosphorco/workbench-go@$version"

  local root
  root="$(prepare_case "$version" build true)"
  run_case "$version" build-success "$root" buildable build --name compat --platform linux-x86_64
  root="$(prepare_case "$version" build false)"
  run_case "$version" build-refusal "$root" buildable build --name compat --platform linux-x86_64

  root="$(prepare_case "$version" seal true)"
  run_case "$version" seal-success "$root" buildable seal --name compat --candidate-root .local-build/compat
  root="$(prepare_case "$version" seal false)"
  run_case "$version" seal-refusal "$root" buildable seal --name compat --candidate-root .local-build/compat

  root="$(prepare_case "$version" verify true)"
  run_case "$version" verify-success "$root" buildable verify --name compat --candidate-root .local-build/compat
  root="$(prepare_case "$version" verify false)"
  run_case "$version" verify-refusal "$root" buildable verify --name compat --candidate-root .local-build/compat

  root="$(prepare_case "$version" check-fresh true)"
  run_case "$version" check-fresh-success "$root" buildable check-fresh --name compat --candidate-root .local-build/compat --built-from HEAD --against origin/main
  root="$(prepare_case "$version" check-fresh false)"
  run_case "$version" check-fresh-refusal "$root" buildable check-fresh --name compat --candidate-root .local-build/compat --built-from HEAD --against origin/main

  root="$(prepare_case "$version" materialize true)"
  run_case "$version" materialize-success "$root" buildable materialize --name compat --platform linux-x86_64 --destination materialized
  root="$(prepare_case "$version" materialize false)"
  run_case "$version" materialize-refusal "$root" buildable materialize --name compat --platform linux-x86_64 --destination materialized
}

run_version 0.6.1
run_version 0.6.2

printf 'command/scenario                  0.6.1 exit  0.6.2 exit  0.6.1 stdout  0.6.2 stdout\n'
for label in \
  build-success build-refusal \
  seal-success seal-refusal \
  verify-success verify-refusal \
  check-fresh-success check-fresh-refusal \
  materialize-success materialize-refusal; do
  old_status="$(< "$results_root/0.6.1-${label}.status")"
  new_status="$(< "$results_root/0.6.2-${label}.status")"
  old_bytes="$(< "$results_root/0.6.1-${label}.bytes")"
  new_bytes="$(< "$results_root/0.6.2-${label}.bytes")"
  printf '%-34s %10s  %10s  %12s  %12s\n' "$label" "$old_status" "$new_status" "$old_bytes" "$new_bytes"
  case "$label" in
    *-success)
      test "$old_status" -eq 0
      test "$new_status" -eq 0
      ;;
    *-refusal)
      test "$old_status" -eq 1
      test "$new_status" -eq 1
      ;;
  esac
done

old_materialize_stdout="$results_root/0.6.1-materialize-success.stdout"
new_materialize_stdout="$results_root/0.6.2-materialize-success.stdout"
test ! -s "$old_materialize_stdout"
grep -Eq '"candidate":"local"' "$new_materialize_stdout"
grep -Eq '"digest":"[0-9a-f]{64}"' "$new_materialize_stdout"
! grep -Fq '"sha256"' "$new_materialize_stdout"
printf 'materialize receipt 0.6.1 stdout bytes=%s; 0.6.2 stdout:\n' "$(< "$results_root/0.6.1-materialize-success.bytes")"
cat "$new_materialize_stdout"
