# Deploy on GitHub Pages

## Upgrade an existing planner repository

1. Extract the package.
2. Open the repository on GitHub.
3. Use **Add file → Upload files**.
4. Drag in everything inside the extracted folder, including `.github` and `assets`.
5. Confirm replacement of existing files and commit to `main`.
6. Open **Settings → Pages** and select **GitHub Actions**.
7. Open **Actions** and wait for **Deploy GitHub Pages** to finish.
8. Open the site and use `Ctrl+Shift+R` once to remove the old cached application shell.

The new package fixes the prior 404 problem by using `./app.js`, `./assets/...`, and other repository-relative paths.

## Important hidden folder

Windows may hide `.github`. It must be uploaded because it contains the Pages deployment workflow. GitHub's web uploader can accept it when the whole extracted folder contents are dragged into the upload area.
