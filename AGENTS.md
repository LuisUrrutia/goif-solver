# Completion gate

After implementation, run `make check` from this checkout before reporting completion or committing. Install the prerequisites listed in `README.md` under "Development tools" first. The command runs the same gate as `.github/workflows/quality.yml`. Run it again after subsequent code or configuration changes. Report the command and exit result; an unavailable check remains a blocker.

Read `docs/quality.md` when changing tool versions, lint exclusions, or the gate. Keep one local/CI entry point. Fix findings in source; a narrowly scoped suppression requires the invariant or trust boundary beside it.
