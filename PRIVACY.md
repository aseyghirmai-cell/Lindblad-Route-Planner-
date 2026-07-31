# Privacy

This static edition has no application backend and no account system.

- Selected OLEX and RTZ files are read by browser APIs on the user's device.
- OLEX geographic indexes are written to the browser's local Origin Private File System.
- Route plans and library metadata are written to IndexedDB in the same browser profile.
- The application does not transmit selected file contents to GitHub or another application server.
- GitHub Pages may still receive ordinary web-host access information when serving the application files, such as the page request and standard network metadata.
- Clearing browser site data deletes the local planner library for that device.
