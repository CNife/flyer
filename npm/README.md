# @cnife/flyer

Publish HTML to a shareable URL. Defaults to a 1-hour page; optional password, permanent pages, and terminal QR code.

```bash
npx -y @cnife/flyer@latest publish report.html
npm install -g @cnife/flyer
```

The package is a small launcher. On first run it downloads the matching `flyer` binary from [GitHub Releases](https://github.com/CNife/flyer/releases), verifies `checksums.txt`, and caches it under `~/.cache/flyer/` (or `%LOCALAPPDATA%\flyer\cache` on Windows). Set `FLYER_CACHE_DIR` to override; set `FLYER_RELEASE_BASE` only for mirrors or testing.

## Usage

```bash
flyer publish report.html
flyer publish report.html --ttl 24h
flyer publish report.html --permanent
flyer publish report.html --password secret
cat report.html | flyer publish -
```

`stdout` is only the URL; metadata and the QR code go to `stderr`. Use `--no-qr` in scripts and `--json` for structured output.

Connect to a server:

```bash
flyer config set server https://flyer.example.com
flyer config set key fly_...
```

Server setup, API, and source: <https://github.com/CNife/flyer>.

MIT licensed.
