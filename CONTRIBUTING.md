# Contributing to GPT‑Админ

Thanks for your interest in contributing! This project is open-core (AGPL-3.0)
and welcomes community contributions.

## Quick start

```bash
git clone https://github.com/megamen32/gptadmin.git
cd gptadmin
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt   # or: pip install fastapi uvicorn
```

Run the hub + an agent locally:

```bash
go run ./go-hub/cmd/gptadmin-hub   # terminal 1
python client/shellmcp.py   # terminal 2
python tests/test_hub.py    # terminal 3 — smoke test
```

## Development workflow

1. **Fork & branch:** create a branch from `main` (`git checkout -b feat/my-feature`)
2. **Write code:** follow existing style (f-strings, explicit logging, type hints where helpful)
3. **Test:** add or update tests in `tests/`. Run `pytest -q` before committing.
4. **Lint:** `ruff check .` (Python) and `eslint adapters/userscript/` (userscript)
5. **Commit:** use clear commit messages (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`)
6. **PR:** open a pull request against `main`. Describe what changed and why.

## Code style

- **Python:** PEP 8, 4-space indent, f-strings, descriptive names
- **Shell:** `set -euo pipefail`, quote variables, check with `shellcheck`
- **Userscript:** vanilla JS, no bundler, must run in Tampermonkey/Userscripts

## Project structure

See `docs/OPEN_CORE_PLAN.md` for the target structure. Currently in transition —
root-level `.py` files will move into `hub/`, `cli/`, `shellmcp/` folders.

## Reporting bugs

Open a GitHub issue with:
- OS and Python version
- GPT‑Админ version (`cat VERSION`)
- Steps to reproduce
- Expected vs actual behavior
- Relevant logs (redact secrets!)

## Feature requests

Open a GitHub Discussion first to gauge interest before building.

## Code of Conduct

Be respectful and constructive. See `CODE_OF_CONDUCT.md`.
