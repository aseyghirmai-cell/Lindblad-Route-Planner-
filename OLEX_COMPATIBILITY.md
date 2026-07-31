# OLEX file compatibility

The static browser edition can index these formats:

1. `.olxidx.gz` files created by the Lindblad planner/indexer, using the `OLXGRID1` binary format.
2. Gzip text sounding exports where each valid row begins with:

```text
latitude longitude depth
```

Blank lines, comments beginning with `#`, and additional columns are ignored.

The browser edition does **not** directly parse proprietary native OLEX backup media, ISO images, TGZ archives, split archives or undocumented internal OLEX database files. Those must first be exported or converted to one of the supported formats.

For very large files, the first browser-side index build may take a long time and requires extra local disk space. Keep the page open until it finishes. The generated geographic index remains local to that browser profile.
