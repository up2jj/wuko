Output docs/assets/workflow-picker.gif

Set Shell bash
Set FontSize 20
Set FontFamily "Hack Nerd Font Mono"
Set Width 1200
Set Height 700
Set Padding 24
Set Margin 0
Set Framerate 30
Set TypingSpeed 35ms
Set LetterSpacing 0
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 0.75
Set LoopOffset 20%

Hide
Type `export WUKO_BIN="$PWD/wuko"`
Enter
Type `export HOME="$(mktemp -d)"`
Enter
Type `export XDG_CONFIG_HOME="$HOME/.config"`
Enter
Type "rm -rf /tmp/wuko-vhs-demo"
Enter
Type "mkdir -p /tmp/wuko-vhs-demo"
Enter
Type "cp -R docs/demos/fixtures/.wuko /tmp/wuko-vhs-demo/"
Enter
Type "cd /tmp/wuko-vhs-demo"
Enter
Type "clear"
Enter
Show

Type `"$WUKO_BIN"`
Enter
Wait+Screen@10s /Workflows \(sort: name\)/
Sleep 1s
Type "/"
Type "release"
Wait+Screen@5s /release-demo/
Sleep 1s
Escape
Down
Sleep 500ms
Up
Sleep 500ms
Ctrl+C
Hide
Type "rm -rf /tmp/wuko-vhs-demo"
Enter
