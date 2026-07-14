# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- PATH-shim recording engine: `pathshim record --cmd git,docker -- CMD`
  creates a temp directory of symlink shims (single binary, argv[0]
  dispatch), runs the wrapped command with it prepended to PATH, and
  captures every intercepted invocation — argv, consumed stdin, stdout,
  stderr, exit code (incl. `128+signal`), duration — while streams flow
  through untouched.
- JSON cassette format v1: pretty-printed, atomically written, text bodies
  stored readably and binary/ANSI bodies base64-boxed with byte counts;
  strict load-time validation and a documented compatibility promise
  (`docs/cassette-format.md`).
- Offline replay: `pathshim replay` answers shimmed calls purely from the
  cassette, consuming each recording once (earliest-first), with `--ordered`,
  `--match-stdin`, and `--match-env` strictness dials and a `--require-all`
  completeness gate.
- Miss handling: policies `fail` (distinctive exit 51 plus a ranked
  closest-candidates diagnosis), `passthrough` (hybrid replay against the
  real tool), and `empty`; the parent session fails on any miss even when
  the wrapped script swallowed the shim's exit code.
- Record-time redaction (`--redact REGEX`, applied to captured streams
  only, never argv), per-interaction env capture (`--env KEY`), and
  per-stream capture caps (`--max-capture`) with truncation flags.
- Concurrency safety: flock-serialized journal appends and
  find/consume state transactions, so parallel invocations (`make -j`,
  backgrounded pipelines) record and replay correctly.
- `inspect` (summary table, JSON, per-interaction detail) and `verify`
  (schema check with truncation warnings) subcommands.
- Runnable example (`examples/record-replay.sh`) and the cassette format
  reference (`docs/cassette-format.md`).
- 90 deterministic offline tests (unit, in-process shim/CLI, and
  compiled-binary end-to-end record→replay flows) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/pathshim/releases/tag/v0.1.0
