Output docs/assets/tui-date.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 850
Set Padding 20
Set Margin 0
Set Framerate 8
Set TypingSpeed 80ms
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 1

Hide
Type "clear"
Enter
Show
Type "./wuko run --file examples/tui-date.yaml"
Enter
Wait+Screen@10s /Choose the release date/
Sleep 1s
Screenshot docs/assets/tui-date.png
Sleep 500ms
Right
Sleep 500ms
Tab
Ctrl+A
Ctrl+K
Type "+2d"
Sleep 1s
Screenshot docs/assets/tui-date-shortcut.png
Sleep 500ms
Enter
Wait+Screen@10s /Release schedule/
Sleep 1s
Screenshot docs/assets/tui-date-form.png
Sleep 500ms
Tab
Enter
Wait+Screen@5s /Release date/
Right
Sleep 1s
Enter
Ctrl+S
Sleep 1s
