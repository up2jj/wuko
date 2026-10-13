Output docs/assets/interactive-workflow.gif

Set Shell bash
Set FontSize 20
Set FontFamily "Hack Nerd Font Mono"
Set Width 1200
Set Height 700
Set Padding 24
Set Margin 0
Set Framerate 30
Set TypingSpeed 90ms
Set LetterSpacing 0
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 0.6
Set LoopOffset 45%

Hide
Type `export PS1='$ '`
Enter
Type "cd docs/demos/fixtures"
Enter
Type "clear"
Enter
Show
Sleep 1s

Type "../../../wuko run --file .wuko/workflows/release-demo.yaml --reporter plain"
Enter
Wait+Screen@10s /Enter the release name/
Sleep 1.2s
Type "spring-release"
Enter
Wait+Screen@10s /Select the deployment environment/
Sleep 1.2s
Down
Sleep 800ms
Enter
Wait+Screen@10s /Format: diff/
Sleep 1.2s
Right
Sleep 800ms
Enter
Wait+Screen@10s /Workflow release-demo succeeded/
Sleep 3s
