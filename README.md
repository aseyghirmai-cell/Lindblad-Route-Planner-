# Lindblad Route Planner — AI Corridor 3.0 Local First

A zero-hosting-cost GitHub Pages route planner that retains the AI Corridor 2.5 layout and embedded historical route catalogue while adding a fully interactive local map editor.

## Included functions

- Embedded historical catalogue: 1,768 routes and 5,528 destination records.
- Browser-side historical graph route generation.
- Start/destination search and coordinate entry.
- Departure/arrival ship-time and UTC-offset conversion.
- Required speed, engine configuration and estimated fuel calculation.
- Local OLEX import, tiled indexing, enable/disable, rename and removal.
- Local RTZ library management.
- Original route summary, support distribution, validation and waypoint review.
- Interactive route review with OLEX traces, land, RTZ overlays, zoom and pan.
- Drag, add, append, insert and delete waypoints.
- Edit waypoint name, coordinates, turn radius, speed, XTD, wheel-over, geometry and remarks.
- Undo and redo.
- Persistent local route plans and automatic draft recovery.
- RTZ, OLEX plot gzip, JSON and CSV exports.
- Route/RTZ backup and restore.
- GitHub Actions deployment workflow.

## Privacy and cost

The website itself is hosted by GitHub Pages. Selected OLEX and RTZ files are processed locally by the browser and are not uploaded. There are no central accounts or synchronized databases.

## Browser support

Use a current Chrome or Edge release over HTTPS. Large OLEX indexing depends on IndexedDB, Origin Private File System, streaming gzip and persistent browser storage.

## Operational limitation

This remains a planning aid. It is not an ECDIS and does not replace approved ENCs, OLEX, UKC/XTD checks, SMS procedures or bridge-team approval.
