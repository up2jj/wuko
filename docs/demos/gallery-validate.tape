Output docs/assets/gallery/validate.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 380
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
Type `cp -R docs/demos/gallery/broken/. /tmp/wuko-gallery/ && cd /tmp/wuko-gallery`
Enter
Type "clear"
Enter
Show

Sleep 800ms
Type "wuko validate deploy"
Enter
Wait+Screen@10s /did you mean/
Sleep 3s
Type "sed -i.bak 's/vars.targt/vars.target/' .wuko/workflows/deploy.yaml"
Enter
Sleep 500ms
Type "wuko validate deploy"
Enter
Wait+Line@10s /^\$\s*$/
Sleep 1s
Type "wuko run deploy --dry-run"
Enter
Wait+Line@10s /^\$\s*$/
Sleep 3s

Hide
Type `cd "$WUKO_REPO" && rm -rf /tmp/wuko-gallery`
Enter
