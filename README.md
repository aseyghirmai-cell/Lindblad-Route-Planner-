# Lindblad Route Planner Cloud 1.0

A browser-based, multi-user expedition route planner with persistent OLEX and RTZ libraries, historical-corridor route generation, route assessment, and an interactive waypoint editor.

## Deploy without installing software

This repository is prepared for **Render Blueprint deployment**. You only need a web browser, a GitHub account, and a Render account.

1. Create a new GitHub repository named `lindblad-route-planner-cloud`.
2. Upload all files from this repository package to the root of that repository.
3. In Render, choose **New → Blueprint**, connect the GitHub repository, and deploy `render.yaml`.
4. Render asks for `LRP_BOOTSTRAP_ADMIN_PASSWORD`. Enter a long, unique administrator password.
5. Open the generated `https://...onrender.com` address and sign in as `admin`.

Read [DEPLOY_ON_RENDER.md](DEPLOY_ON_RENDER.md) for the exact browser-only procedure.

## Included application features

- Individual usernames and passwords.
- Administrator, planner, and read-only roles.
- Organization-separated workspaces.
- Persistent OLEX databases, RTZ routes, uploads, accounts, and saved route plans.
- Resumable chunked browser uploads.
- Historical RTZ corridor routing and combined OLEX support assessment.
- Editable waypoint names, positions, turn radii, XTD, speed, wheel-over, geometry, and remarks.
- Interactive route review with zoom, pan, OLEX-derived background traces, and draggable waypoints.
- RTZ, OLEX plot, and Route JSON export.

## Storage model

`render.yaml` provisions one 200 GB persistent disk at `/data`. The application has no hard-coded database-count limit, but real capacity is limited by the purchased disk size, network bandwidth, and processing time. Increase the Render disk before uploading data that will not fit alongside indexes, route files, temporary chunks, and backups.

The current build is intentionally a **single-instance deployment** because its account state, route plans, manifests, and indexed marine data are file-backed. It is not a horizontally scaled SaaS architecture yet.

## Safety status

This software is a planning aid and an engineering MVP. It has not received independent penetration testing, type approval, classification approval, or operational validation against the complete production OLEX collection. It does not replace approved ENC/ECDIS, OLEX, UKC/XTD calculations, company SMS procedures, or bridge-team route checking.
