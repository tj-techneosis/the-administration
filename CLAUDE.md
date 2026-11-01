# Working on the Administration

This file is for working **on** the Administration. The instructions for
**using** it (what an agent does when asked to place a contract) are
`cmd/administration/guide.md`, printed by `administration guide`. The
plugin's skill (`plugin/skills/the-administration/SKILL.md`) is only its
trigger and a pointer there, so the instructions ship with the binary they
describe and an upgrade never needs Claude Code restarted. Change the
skill's own file only to change when it triggers.

## Layout

| path | what |
|---|---|
| `cmd/administration/` | the whole tool, in Go: one static binary (`CGO_ENABLED=0`) |
| `cmd/administration/house/` | what ships inside the binary: the Director's prompt and the suggested open rooms (`guide.md` beside it is embedded too). The framework hires nobody |
| `plugin/` | the Claude Code plugin: the skill, the commands, the Director |
| `.claude-plugin/marketplace.json` | so the repo can be added as a plugin marketplace |
| `examples/demo/` | a complete house to try contracts on: an app, its setup hook, two professionals |
| `tests/` | `tests/isolated.sh`: black-box against the CLI with a fake claude, in a private process namespace; run before every commit |
| `tests/live/` | probes against the real browser (`gate_probe.py`) and claude, run by hand, inside the same namespace |
| `docs/design.md` | the plain-language version: why each piece is the way it is |
| `scripts/package.sh` | builds a release's archives and `SHA256SUMS`; `.github/workflows/release.yml` runs it on a pushed `v*` tag, checks the archives on Linux and macOS, and publishes the release. Nothing built is committed |

## Rules

- **Run the tests with `tests/isolated.sh`**, never bare: the suite starts
  and kills real processes, and the script gives it a private process
  namespace where nothing of the person's is visible. (A cleanup bug once
  matched every process on the machine and ended a desktop session.) Go unit
  tests that signal nothing (`go test ./...`) are safe anywhere.
- **The tests** drive the real binary with a throwaway project,
  books and rooms (`ADMINISTRATION_HOME`, `ADMINISTRATION_ROOMS`) and a fake
  claude (`ADMINISTRATION_CLAUDE`), so they never touch the machine's own
  contracts. A behaviour change comes with a test that fails without it.
- **The wall is the product.** A change to how a professional is started
  (`runProfessional`, `settingsFor`, `mcpFor`, `browserTools`, `childEnv`)
  needs its test updated and a live check on a real machine
  (`tests/live/`). The browser tools are an allowlist; a new Playwright tool
  stays off until someone decides otherwise.
- **The Playwright pin** lives in two places: `playwrightMCP` in `work.go`
  and `mcp=` in `install.sh`. Change them together, and re-run the live
  probe.
- **Exit codes are a contract** with the skill: 0, 1, 2, 3, 4, 5 as in `main.go`.
- **The framework hires nobody.** No professional ships in `house/` or in
  the plugin; projects train their own. The Director's and the verifier's
  prompts are the only persona writing that ships.
- **The README stays in the frame**, and every metaphor maps to real
  behaviour. `docs/design.md` is the plain version.
- **Nothing specific to one project or person** goes in the code, the
  plugin or the docs: no client names, machine names or private paths.
