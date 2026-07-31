# Start here — replace the current GitHub Pages files

This package is a static, local-first upgrade of the AI Corridor 2.5 planner. No server, Docker, Caddy or paid hosting is required.

## Publish it

1. Extract the ZIP.
2. Open the GitHub repository currently used for the planner.
3. Delete the old planner files, or upload these files and choose **Commit changes** so matching files are replaced.
4. Important: upload the **contents of this folder**. `index.html` must be at the repository root.
5. In **Settings → Pages**, keep **Source: GitHub Actions**.
6. Open **Actions → Deploy GitHub Pages** and wait for a green check.
7. Refresh the planner with `Ctrl+Shift+R` once after deployment. The service worker then updates to version 3.

All links and workers use relative paths, so the planner works under a GitHub project URL such as:

`https://aseyghirmai-cell.github.io/Lindblad-Route-Planner/`

## First use on each workstation

1. Open the planner in current Chrome or Edge.
2. Choose **Local Libraries** and click **Request Persistent Local Storage** in the OLEX manager.
3. Add the workstation's OLEX database and keep the tab open until indexing finishes.
4. Add any additional historical RTZ routes.
5. Generate a route, open the interactive editor, zoom into the OLEX traces and drag waypoints as required.
6. Click **Save Locally** and export the final RTZ/OLEX plot when ready.

## Persistence

The browser database name and storage layout remain compatible with the previous Local First 1.0 package. If this upgrade is deployed at the same GitHub Pages URL, in the same browser profile, existing local routes, RTZ records and completed OLEX indexes should remain available.

Data is private to the workstation and browser profile. It is not synchronized to other computers.
