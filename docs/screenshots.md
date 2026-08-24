# Screenshots and demos

Wuko's README media is generated from [Charmbracelet VHS](https://github.com/charmbracelet/vhs)
tapes. The tapes run deterministic local fixtures, so recording does not need API credentials,
network access, external agents, or a particular user configuration.

## Prerequisites

Install VHS, `ttyd`, and `ffmpeg`. VHS uses `ttyd` and `ffmpeg` when it renders terminal output.
The CI validator only parses tapes and does not render or modify the checked-in assets.

## Regenerate the media

From the repository root:

```sh
just screenshots
```

This builds `./wuko` and renders the tapes in `docs/demos/` into `docs/assets/`. The generated
files are intentionally checked in so README readers do not need VHS installed.

To validate the tapes without rendering:

```sh
just validate-screenshots
```

## Fixtures and recording conventions

The workflows under `docs/demos/fixtures/` are documentation fixtures, not production examples.
They avoid network calls, secrets, timestamps, random values, and external commands so that the
recordings stay understandable and reproducible.

Each tape fixes the shell, `Hack Nerd Font Mono` font, terminal dimensions, theme, padding, frame
rate, letter spacing, cursor behavior, typing speed, and playback speed. Install that font as well
if you want matching local renders. Interactive tapes use `Wait` for visible prompts before sending
keys. The picker tape sets temporary `HOME` and `XDG_CONFIG_HOME` directories so persisted picker
state never affects a recording.

When the CLI or TUI changes, run `just screenshots`, inspect the resulting GIFs and PNG, and commit
the updated media together with the tape or fixture changes. CI validates the tape syntax and checks
that all committed assets are present; it deliberately does not re-record media.
