Output docs/assets/gallery/form.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 680
Set Padding 20
Set Margin 0
Set Framerate 24
Set TypingSpeed 55ms
Set LetterSpacing 0
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 0.7

Hide
Type `export WUKO_REPO="$PWD" HOME="$(mktemp -d)"`
Enter
Type `export XDG_CONFIG_HOME="$HOME/.config" PATH="$HOME/bin:$PATH" PS1='$ '`
Enter
Type `mkdir -p "$HOME/bin" && ln -s "$WUKO_REPO/wuko" "$HOME/bin/wuko"`
Enter
Type `rm -rf /tmp/wuko-gallery && mkdir -p /tmp/wuko-gallery`
Enter
Type `cp -R docs/demos/gallery/. /tmp/wuko-gallery/ && cd /tmp/wuko-gallery`
Enter
Type "clear"
Enter
Show

Sleep 800ms
Type "wuko run form"
Enter
Wait+Screen@10s /Deploy a service/
Sleep 1s
Type "Billing API"
Sleep 300ms
Tab
Wait+Screen@5s /lowercase letters/
Sleep 1.5s
Shift+Tab
Sleep 300ms
Backspace 11
Type "billing-api"
Sleep 300ms
Tab
Sleep 600ms
Down
Sleep 600ms
Tab
Sleep 600ms
Down
Sleep 300ms
Space
Sleep 600ms
Tab
Sleep 600ms
Ctrl+S
Wait+Screen@5s /at least two replicas/
Sleep 1.5s
Backspace
Type "3"
Sleep 800ms
Ctrl+S
Wait+Line@30s /^\$\s*$/
Sleep 3s

Hide
Type `cd "$WUKO_REPO" && rm -rf /tmp/wuko-gallery`
Enter
