# OLEX compatibility

Supported local source formats:

1. `.olxidx.gz` using the `OLXGRID1` indexed format.
2. Gzip-compressed text exports containing latitude, longitude and depth rows.

The application streams the compressed source and writes one-degree geographic tiles into the browser's private local file system. The original source file is not changed and is not uploaded.

A proprietary native OLEX backup whose internal format is not one of the formats above cannot be interpreted automatically.

## Large files

The application does not impose a fixed source-size limit, but actual capacity depends on:

- free workstation disk space;
- browser storage quota;
- whether persistent storage was granted;
- browser stability during the initial index build.

A complete 50–60 GB operational collection must be tested on the intended workstation before relying on it. Keep the tab open until indexing reports completion.
