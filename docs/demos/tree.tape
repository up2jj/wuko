Output docs/assets/workflow-tree.png

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

Hide
Type `export PS1='$ '`
Enter
Type "cd docs/demos/fixtures"
Enter
Type "clear"
Enter
Show

Type "../../../wuko tree --file .wuko/workflows/release-demo.yaml"
Enter
Wait+Screen@10s /tui_review/
Sleep 2s
Screenshot docs/assets/workflow-tree.png
