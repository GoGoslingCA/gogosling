# Desktop icons

The native icons are format conversions of the bundled [Gosli image](../../web/public/brand/gosli.png). The artwork is unchanged. The source image is 192 × 192 pixels; larger representations are resampled, not higher-resolution artwork.

Regenerate on macOS, using the system `sips` and `iconutil` tools:

```sh
node apps/desktop/scripts/generate-icons.mjs
```

The committed ICNS, ICO, and PNG files let native packaging use the Gogosling identity without downloading assets or requiring image-conversion tools on each build host.
