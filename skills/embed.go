package skills

import "embed"

// Assets contains the portable skills shipped with Wuko.
//
// Keep the embedded paths explicit enough to make the installed binary
// independent of the repository's working directory.
//
//go:embed wuko-*/SKILL.md wuko-*/agents/openai.yaml
var Assets embed.FS
