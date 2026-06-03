# Demo recording for codexmon.
#
# Generate docs/demo.gif with https://github.com/charmbracelet/vhs:
#   make build && PATH="$PWD:$PATH" vhs docs/demo.tape
#
# It points codexmon at a bundled fake codex (docs/fakecodex.sh) and a throwaway
# state dir, so the recording needs no Codex install, network, or auth.

Output docs/demo.gif

Require codexmon

Set Shell "bash"
Set FontSize 16
Set Width 1120
Set Height 760
Set Padding 18
Set Theme "Dracula"

# --- setup (hidden) ---
Hide
Type "export CODEXMON_CODEX=$PWD/docs/fakecodex.sh CODEXMON_HOME=$(mktemp -d)" Enter
Type "clear" Enter
Show
Sleep 500ms

# --- 1. confirm codex is usable ---
Type "codexmon doctor" Enter
Sleep 1500ms

# --- 2. launch a monitored review in the background ---
Type "codexmon start -- exec review --uncommitted" Enter
Sleep 2500ms

# --- 3. poll its health any time (never blocks) ---
Type "codexmon status" Enter
Sleep 2500ms

# --- 4. block until it finishes, then print the result ---
Type "codexmon wait" Enter
Sleep 5s
