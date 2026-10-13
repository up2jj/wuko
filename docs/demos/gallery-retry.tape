Output docs/assets/gallery/retry.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 640
Set Padding 20
Set Margin 0
Set Framerate 24
Set TypingSpeed 35ms
Set LetterSpacing 0
Set CursorBlink false
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 1.0

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

Type "wuko run retry"
Enter
Wait+Line@30s /^\$\s*$/
Sleep 3.5s

Hide
Type `cd "$WUKO_REPO" && rm -rf /tmp/wuko-gallery`
Enter
