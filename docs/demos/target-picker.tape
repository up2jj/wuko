Output docs/assets/target-picker.gif

Set Shell bash
Set FontSize 20
Set FontFamily "Hack Nerd Font Mono"
Set Width 1200
Set Height 700
Set Padding 24
Set Margin 0
Set Framerate 20
Set TypingSpeed 60ms
Set CursorBlink false
Set Theme "Catppuccin Mocha"

Hide
Type `export WUKO_BIN="$PWD/wuko"`
Enter
Type `export WUKO_TARGET_DEMO="$(mktemp -d)"`
Enter
Type `export HOME="$WUKO_TARGET_DEMO/home" XDG_CONFIG_HOME="$WUKO_TARGET_DEMO/config"`
Enter
Type `mkdir -p "$HOME" "$WUKO_TARGET_DEMO/.wuko/workflows"`
Enter
Type `cp docs/demos/fixtures/.wuko/workflows/targets-demo.yaml "$WUKO_TARGET_DEMO/.wuko/workflows/"`
Enter
Type `cd "$WUKO_TARGET_DEMO"`
Enter
Type "clear"
Enter
Show

Type `"$WUKO_BIN"`
Enter
Wait+Screen@10s /Workflows \(sort: name\)/
Sleep 1s
Type "t"
Wait+Screen@5s /Targets for targets-demo/
Sleep 1s
Down
Sleep 1s
Escape
Wait+Screen@5s /Workflows \(sort: name\)/
Sleep 1s
Enter
Wait+Screen@5s /Targets for targets-demo/
Sleep 1s
Ctrl+C
Hide
Type `cd /tmp && rm -rf "$WUKO_TARGET_DEMO"`
Enter
