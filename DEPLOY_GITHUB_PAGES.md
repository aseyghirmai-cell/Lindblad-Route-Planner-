# Deploy with GitHub Pages — browser only

No software installation and no payment method are required for a public repository.

1. Open the GitHub repository intended for the planner.
2. Delete server-only files from the previous Render edition, or create a new repository such as `lindblad-route-planner-local-first`.
3. Upload **the contents of this package** to the repository root. `index.html` must be visible at the top level.
4. Commit the files to the `main` branch.
5. Open **Settings → Pages**.
6. Under **Build and deployment**, choose **GitHub Actions** as the source.
7. Open the **Actions** tab and wait for `Deploy GitHub Pages` to finish successfully.
8. Return to **Settings → Pages** to see the public HTTPS address.

The expected address normally resembles:

```text
https://YOUR-GITHUB-NAME.github.io/REPOSITORY-NAME/
```

## Updating

Replace changed files in GitHub and commit them. The included workflow redeploys the static site automatically.

## Important storage behavior

- OLEX, RTZ and route data belong to the browser profile and device where they were selected.
- Opening the same public URL on another computer starts with a separate empty local library.
- Browser restarts preserve data, provided site data is not cleared and storage remains available.
- Private/incognito browsing must not be used because local data may be deleted when the window closes.
- A browser backup does not include OLEX indexes. Original OLEX files must be retained.
