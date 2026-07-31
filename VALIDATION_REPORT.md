# Validation report — AI Corridor 3.0 Local First

Validation date: 2026-07-31

## Passed automated checks

- JavaScript syntax: `app.js`, `planner-worker.js`, `olex-worker.js`, and `service-worker.js`.
- GitHub Pages path audit: no root-absolute application script, stylesheet, worker or fetch paths.
- HTML element audit: 143 unique IDs, no duplicate IDs, and all 125 JavaScript element references resolve to an HTML element.
- Historical planner database parsing:
  - 1,768 historical routes;
  - 5,528 destination records;
  - 8 NG Resolution engine configurations;
  - 29,082 historical graph nodes;
  - 336,266 graph edges.
- Historical route generation:
  - Ushuaia → Beagle Channel: 12 editable waypoints;
  - Ushuaia → Port Lockroy: 36 editable waypoints.
- Local OLEX worker test using the included gzip sample:
  - 29,141 valid records;
  - one local geographic tile;
  - manifest and bounds generated successfully.
- Core voyage calculations: route distance, required speed, engine selection and editable direct-route fallback.
- GitHub Actions Pages workflow and static resource existence checks.

## Compatibility retained

The IndexedDB database name, object stores and Origin Private File System OLEX folder remain the same as Local First 1.0. When deployed at the same GitHub Pages URL, completed local indexes and saved records are intended to remain available.

## Testing not completed here

- A complete 50–60 GB operational OLEX collection was not available for end-to-end indexing.
- Browser storage quota behavior varies by workstation and browser policy.
- The application has not been independently penetration-tested or approved for operational navigation.
- The browser route engine uses the embedded historical graph but does not reproduce every Go desktop corridor-centering and land-mask heuristic byte-for-byte.

## Required operational validation

Before bridge use, test on the intended workstation with representative RTZ and OLEX data and compare generated/exported routes against OLEX, approved ENCs and ECDIS route checking.
