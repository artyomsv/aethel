---
name: staged-dir-trust-boundary
description: VerifyStaged walks manifest→disk only and never enumerates the staged dir; but QUIL_HOME write access is already code execution via plugins, so gaps there are hygiene, not vulnerabilities
metadata:
  type: project
---

Two facts that belong together. Reviewing the first without the second produces
an inflated severity — I did exactly that on PR #193 and had to withdraw a HIGH.

**Structural fact.** `internal/update/stage.go` `VerifyStaged(dir, m, required)`
iterates `m.Files` (manifest → disk) and the `required` name list. It does **not**
`os.ReadDir` the staged version dir. A file in
`<QUIL_HOME>/update/staged/<ver>/` that the manifest does not declare is never
opened and never hashed. `os.Stat(stagedPath) == nil` proves existence, never
verification. So any apply-path read of that dir must consult `Manifest.Files`.

**Blast-radius fact — check this BEFORE assigning severity to anything under
QUIL_HOME.** Write access to QUIL_HOME is *already* unconditional arbitrary code
execution, by pre-existing design: `config.PluginsDir()` is
`filepath.Join(QuilDir(), "plugins")`; `plugin.CommandConfig` carries `Cmd`,
`Args`, `Env` and `Path` ("full path to binary (overrides PATH lookup)"); the
daemon `LoadFromDir`s that directory at startup and spawns from it. Drop a
`plugins/*.toml`, or overwrite `terminal.toml`, and the next pane spawn runs your
binary — no staged update, no version compare, no toast click.

**How to apply:** an attacker who can reach the staged dir can reach the plugins
dir, so a missing hash check there grants them nothing new. Rate such gaps as
hygiene/corruption-resistance (a corrupt staged file installed unchecked), not as
privilege escalation. The same test applies to config.toml, instances.json and
anything else under QuilDir(). Reserve real severity for paths that cross OUT of
QUIL_HOME's blast radius — into a protected install dir, another user, or a
remote host.

Related: [[project_remote_daemon_taint]] for the case where the boundary IS real
because the data came off another machine.
