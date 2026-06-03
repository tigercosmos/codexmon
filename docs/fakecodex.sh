#!/bin/sh
# Minimal fake `codex` used only to record the codexmon demo (docs/demo.tape).
# It emits a realistic `codex exec --json` event stream with small pauses, so the
# recording needs no Codex install, network, or auth.
case "$1" in
  --version)
    echo "codex-cli 0.0.0-fake"; exit 0 ;;
  doctor)
    echo '{"ok":true,"checks":[]}'; exit 0 ;;
  exec)
    # find codexmon's injected --output-last-message file
    out=""; prev=""
    for a in "$@"; do
      if [ "$prev" = "-o" ] || [ "$prev" = "--output-last-message" ]; then out="$a"; fi
      prev="$a"
    done
    echo '{"type":"thread.started","thread_id":"demo-0001"}'
    echo '{"type":"turn.started"}'; sleep 1
    echo '{"type":"item.started","item":{"id":"c0","type":"command_execution","command":"git diff --stat","status":"in_progress"}}'; sleep 1
    echo '{"type":"item.completed","item":{"id":"c0","type":"command_execution","command":"git diff --stat","exit_code":0,"status":"completed"}}'; sleep 1
    echo '{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"go test ./...","status":"in_progress"}}'; sleep 2
    echo '{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"go test ./...","exit_code":0,"status":"completed"}}'; sleep 1
    msg="Reviewed the diff: 1 issue — Divide() panics when b == 0; return an error instead of dividing."
    printf '{"type":"item.completed","item":{"id":"m0","type":"agent_message","text":"%s"}}\n' "$msg"
    echo '{"type":"turn.completed","usage":{"input_tokens":1234,"cached_input_tokens":1024,"output_tokens":56,"reasoning_output_tokens":20}}'
    [ -n "$out" ] && printf '%s' "$msg" > "$out"
    exit 0 ;;
  *)
    echo "fake codex: unsupported invocation: $*" >&2; exit 0 ;;
esac
