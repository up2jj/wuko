Output docs/assets/interactive-workflow.gif

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
Set LoopOffset 45%

Hide
Type `export PS1='$ '`
Enter
Type "cd docs/demos/fixtures"
Enter
Type "clear"
Enter
Show

Type "../../../wuko run --file .wuko/workflows/release-demo.yaml --reporter plain"
Enter
Wait+Screen@10s /Enter the release name/
Type "spring-release"
Enter
Wait+Screen@10s /Select the deployment environment/
Down
Enter
Wait+Screen@10s /Format: diff/
Right
Enter
Wait+Screen@10s /Workflow release-demo succeeded/
Sleep 2s
