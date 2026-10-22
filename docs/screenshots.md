# Screenshots and demos

[Back to the README](../README.md)

Wuko's README media is generated from [Charmbracelet VHS](https://github.com/charmbracelet/vhs)
tapes. The tapes run deterministic local fixtures, so recording does not need API credentials,
network access, external agents, or a particular user configuration.

## Contents

- [Prerequisites](#prerequisites)
- [Regenerate the media](#regenerate-the-media)
- [Fixtures and recording conventions](#fixtures-and-recording-conventions)
- [Gallery demos](#gallery-demos)

## Prerequisites

Install VHS, `ttyd`, and `ffmpeg`. VHS uses `ttyd` and `ffmpeg` when it renders terminal output.
The CI validator only parses tapes and does not render or modify the checked-in assets.

## Regenerate the media

From the repository root:

```sh
just screenshots
```

This builds `./wuko` and renders the tapes in `docs/demos/` into `docs/assets/`. The generated
files are intentionally checked in so README readers do not need VHS installed. Render a single
tape with `just screenshot NAME`, for example `just screenshot gallery-retry`.

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

## Gallery demos

The [demo gallery](gallery.md) is recorded from `docs/demos/gallery-*.tape`. Each tape renders
`docs/assets/gallery/NAME.gif` from a workflow in `docs/demos/gallery/.wuko/workflows/NAME.yaml`;
the validation demo uses its own deliberately broken fixture in `docs/demos/gallery/broken/`.
`TestGalleryFixturesValidate` in `cmd/` keeps every gallery fixture valid.

Gallery tapes share one hidden prelude: a temporary `HOME`, a `wuko` symlink on `PATH`, and a copy
of the fixture directory in `/tmp/wuko-gallery`. Copying matters because discovery also searches
parent directories, so running inside a checkout under your home directory would list your own
workflows. Tapes wait for the shell prompt to return with `Wait+Line /^\$\s*$/` rather than
matching run output: `Wait+Screen` does not see lines that scrolled off the top, which happens
with full-height TUIs and long runs.

To add a demo:

1. Add a short, offline, deterministic workflow to `docs/demos/gallery/.wuko/workflows/`.
2. Copy an existing `docs/demos/gallery-*.tape`, change its `Output`, height, and body.
3. Run `just screenshot gallery-NAME` and inspect the GIF.
4. Add a section to [`gallery.md`](gallery.md) with the GIF and the workflow YAML.

When the CLI or TUI changes, run `just screenshots`, inspect the resulting GIFs and PNG, and commit
the updated media together with the tape or fixture changes. CI validates the tape syntax and checks
that every `Output` and `Screenshot` named by a tape is committed; it deliberately does not re-record media.
