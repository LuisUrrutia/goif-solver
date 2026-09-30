# README research

Reviewed on 2026-09-30. These recommendations guide the README rewrite; they
do not establish project capabilities or select a license.

## What the primary sources recommend

GitHub describes a README as the repository's introduction: explain its purpose,
value, setup, support, and maintainers. Keep the information needed to start using
or contributing to the project in the README, with longer documentation elsewhere.
Use relative links for repository files. GitHub already generates a heading-based
outline, so a hand-maintained table of contents is optional.
Source: https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes.

GitHub's Open Source Guides recommends stating when a project is not ready for
production, explaining how to report problems, and documenting contribution
requirements. Its launch checklist includes a README, license, contribution
guidelines, and code of conduct. These are separate responsibilities; a longer
README does not replace a license or establish community policy.
Source: https://opensource.guide/starting-a-project/.

Google's release guidance advises a plain description and examples that help
people start using the code. Its organization-specific licensing and release
policies are not requirements for this repository.
Source: https://opensource.google/documentation/reference/releasing/preparing/#include-a-readme-file.

## Recommended structure for this repository

This sequence is an editorial recommendation based on the sources above and the
current repository, rather than a universal README template:

1. **Purpose and current scope.** Explain what an intent solver does and what this
   implementation provides. State the present EVM escrow/testnet scope and link
   the supported OIF subset. Describe extension points without implying SVM/TVM
   support or production readiness.
2. **Quick start.** Give prerequisites, checkout/start commands, and an observable
   success check. Explain that the default memory service is idle, needs no
   credentials, and loses state on exit. Reserve funded execution for its guide.
3. **Configuration and operation.** Point to the development and testnet examples.
   Explain when Redis is needed and link its deployment and recovery contract.
4. **Documentation.** Link a short set of reader tasks: configure routes, understand
   adapters, check OIF compatibility, deploy, recover, and inspect test evidence.
5. **Development and help.** Give the single quality-gate command and its prerequisite
   guide. Tell bug reporters which version, reproduction, and redacted logs help.
6. **License.** Report the actual repository status until the owner chooses terms.

Prefer direct explanations over feature slogans. Keep one runnable path prominent;
avoid an undifferentiated block of local checks, public-network checks, and Docker
commands. Move audit history and transaction evidence behind links. Add badges,
diagrams, or a manual contents list only if they answer a reader's question.

The current [development configuration](../config/development.json) has memory
storage and no sources or providers; [dev.sh](../scripts/dev.sh) builds and runs it.
The [quality guide](quality.md) separates runtime prerequisites from the full gate.
At review time, `git ls-files` contains no project `LICENSE`, `CONTRIBUTING`,
`CODE_OF_CONDUCT`, or `SECURITY` file. Do not invent their contents, a license badge,
a support promise, or a private reporting address.
