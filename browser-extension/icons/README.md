# Extension Icons

The following PNG icon files are required by `manifest.json` but are **not checked in** as binary files.

Generate them from the SVG source below using any tool of your choice (e.g. Inkscape, `rsvg-convert`, or an online converter).

## Required files

| File | Size |
|------|------|
| `icon16.png` | 16×16 px |
| `icon48.png` | 48×48 px |
| `icon128.png` | 128×128 px |

## SVG source

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128">
  <!-- Cloud -->
  <path d="M96 56a28 28 0 00-2.3-11.2A32 32 0 0040 42a24 24 0 00-16 42h72a28 28 0 000-28z"
        fill="#6366f1" />
  <!-- Monitor -->
  <rect x="32" y="72" width="64" height="40" rx="4" fill="#1a1d27" stroke="#6366f1" stroke-width="3"/>
  <!-- Screen glow -->
  <rect x="38" y="78" width="52" height="28" rx="2" fill="#22c55e" opacity="0.15"/>
  <!-- Stand -->
  <rect x="56" y="112" width="16" height="6" rx="1" fill="#8b8fa7"/>
  <rect x="48" y="118" width="32" height="4" rx="2" fill="#8b8fa7"/>
  <!-- Check mark on screen -->
  <polyline points="56,90 64,98 80,82" fill="none" stroke="#22c55e" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
```

## Quick generation (Linux / macOS)

```bash
# Using rsvg-convert (from librsvg)
rsvg-convert -w 16 -h 16 icon.svg -o icon16.png
rsvg-convert -w 48 -h 48 icon.svg -o icon48.png
rsvg-convert -w 128 -h 128 icon.svg -o icon128.png

# Using Inkscape CLI
inkscape icon.svg -w 16 -h 16 -o icon16.png
inkscape icon.svg -w 48 -h 48 -o icon48.png
inkscape icon.svg -w 128 -h 128 -o icon128.png
```
