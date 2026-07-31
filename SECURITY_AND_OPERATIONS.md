# Security and operations

- Keep the GitHub repository private unless source publication is intentional.
- Never commit OLEX databases, RTZ libraries, passwords, `.env` files, or generated `/data` content.
- Use a unique bootstrap administrator password and change it after first login.
- Create separate named accounts instead of sharing the administrator account.
- Disable accounts immediately when access is no longer required.
- Use organization workspaces to prevent one customer from seeing another customer's files.
- Keep `LRP_PASSWORD_PEPPER` unchanged after the first deployment. Changing it invalidates existing password verification.
- Keep HTTPS enabled. Render terminates public TLS and forwards the request to the application.
- Review audit logs under the persistent data root and maintain off-platform backups.
- Perform an independent security review before allowing external customers or uploading commercially sensitive marine data.

## Navigational limitation

The OLEX background, historical corridor, generated route, and waypoint editor are decision-support layers. They are not approved chart products and are not a replacement for the vessel's required navigational systems and checks.
