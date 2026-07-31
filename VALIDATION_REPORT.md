# Validation report — Local First 1.0

Validation performed in the build environment:

- JavaScript syntax checked with Node.js for `app.js`, `olex-worker.js` and `service-worker.js`.
- Static site loaded successfully over HTTP in desktop Chromium with IndexedDB, OPFS and `DecompressionStream` available.
- Imported the included RTZ sample and opened its three waypoints as an editable route.
- Indexed the included raw gzip sample: 29,141 soundings into one local geographic tile.
- Indexed the existing planner sample `.olxidx.gz`: 722,371 cells into 61 local geographic tiles.
- Rendered OLEX traces, historical RTZ, land and the editable route together in the route-review canvas.
- Confirmed route name, waypoint name and turn-radius edits survived a complete page reload.
- Confirmed OLEX library metadata survived a complete page reload.
- GitHub Pages workflow YAML, manifest, required assets and HTML/JavaScript element references were checked.

Not yet validated:

- The user's complete 50–60 GB OLEX archive.
- Long-duration indexing interruption and recovery on the user's computer. The first gzip index build currently must remain open until completion.
- Independent navigational, cybersecurity or regulatory approval.
- Browser storage quota and performance on every target computer.
