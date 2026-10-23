Output docs/assets/tui-form.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 1100
Set Padding 20
Set Margin 0
Set Framerate 8
Set TypingSpeed 40ms
Set LetterSpacing 0
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 1

Hide
Type "clear"
Enter
Show
Type "./wuko run --file examples/tui-form.yaml"
Enter
Wait+Screen@10s /Deployment settings/
Sleep 1s
Screenshot docs/assets/tui-form.png
Sleep 2s
Ctrl+A
Ctrl+K
Type "x"
Ctrl+S
Wait+Screen@10s /Use at least three/
Sleep 1s
Screenshot docs/assets/tui-form-validation.png
Sleep 2s
Ctrl+A
Ctrl+K
Type "autumn-release"
Ctrl+S
Sleep 1s
