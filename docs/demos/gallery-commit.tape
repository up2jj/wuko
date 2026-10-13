Output docs/assets/gallery/commit.gif

Set Shell bash
Set FontSize 18
Set FontFamily "Hack Nerd Font Mono"
Set Width 1100
Set Height 640
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
Type `export GIT_AUTHOR_DATE=2026-01-01T09:00:00Z GIT_COMMITTER_DATE=2026-01-01T09:00:00Z`
Enter
Type `git init -q -b main && git config user.email demo@example.com && git config user.name Demo`
Enter
Type `printf "package api\n\nfunc Greet() string {\n\treturn \"hello\"\n}\n" > api.go && git add api.go .wuko && git commit -qm "feat(api): add greeting"`
Enter
Type `printf "package api\n\nfunc Greet(name string) string {\n\treturn \"hello, \" + name\n}\n" > api.go`
Enter
Type "clear"
Enter
Show

Sleep 800ms
Type "wuko run commit"
Enter
Sleep 2s
Sleep 2.5s
Enter
Sleep 1.5s
Sleep 700ms
Down
Sleep 500ms
Enter
Sleep 1.5s
Type "greet callers by name"
Sleep 500ms
Enter
Wait+Line@30s /^\$\s*$/
Sleep 3.5s

Hide
Type `cd "$WUKO_REPO" && rm -rf /tmp/wuko-gallery`
Enter
