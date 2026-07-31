# Browser-only deployment on Render

No Docker, Caddy, Git, Linux, or command-line software is required on your computer.

## 1. Create the GitHub repository

1. Sign in to GitHub in your browser.
2. Select **New repository**.
3. Name it `lindblad-route-planner-cloud`.
4. Choose **Private** unless you deliberately want the source public.
5. Do not add a README, license, or `.gitignore` because those files are already included.
6. Create the repository.
7. Select **uploading an existing file** or **Add file → Upload files**.
8. Extract the supplied ZIP on your computer, open the extracted folder, and drag all files and folders into the GitHub upload page.
9. Commit the files to the `main` branch.

The marine databases must not be committed to GitHub. OLEX and RTZ files are uploaded through the running planner and are stored on the Render persistent disk.

## 2. Deploy the Blueprint

1. Create or sign in to a Render account.
2. Connect Render to GitHub and allow access to `lindblad-route-planner-cloud`.
3. In the Render dashboard, select **New → Blueprint**.
4. Choose the repository.
5. Render detects `render.yaml`.
6. Enter a strong value for `LRP_BOOTSTRAP_ADMIN_PASSWORD` when prompted.
7. Review the selected **Standard** web-service plan and **200 GB** persistent disk. These are paid resources; change them only after checking that the resulting storage is sufficient.
8. Select **Deploy Blueprint**.

Render builds the Go application inside its own infrastructure. Your computer does not need build tools.

## 3. Sign in

When the deploy shows **Live**, open the generated `onrender.com` URL.

- Username: `admin`
- Password: the bootstrap password entered during deployment

Change the password after first login and create named user accounts from the administrator page.

## 4. Upload OLEX and RTZ data

Use **Manage Databases** and **Manage RTZ Routes** in the web application. Browser uploads are sent in persistent chunks and can resume when the same file is selected again after interruption.

A 50–60 GB database can take many hours over a normal office connection. Ensure the Render disk has room for the compressed upload, generated indexes, temporary data, RTZ libraries, saved routes, and backups before starting.

## 5. Custom domain

The initial Render URL works automatically. To use a company domain, add it in the Render service settings and set:

`LRP_PUBLIC_URL=https://planner.your-domain.example`

Then redeploy. This pins security checks and session handling to the custom hostname.

## 6. Persistence and backups

Everything below `/data` survives application restarts and redeploys because `render.yaml` mounts a persistent disk there. Keep independent backups. A persistent disk is not a substitute for a tested backup and restore process.

## Current architecture limitation

The attached persistent disk restricts this version to one application instance. A later high-availability version should move accounts and route metadata to PostgreSQL, move source objects to object storage, and coordinate indexing through a durable job queue.
