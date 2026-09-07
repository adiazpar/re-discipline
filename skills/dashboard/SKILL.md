---
name: dashboard
description: Open the optional community web interface for visual review, publication, membership, and policy management while keeping agent commands available.
---

# Open a community dashboard

Use the `community` tool with `dashboard` and a connected alias or explicit service
URL. Open the returned URL with the host's browser-opening capability, or provide
a clickable link if none is available. Do not assume embedded panels are supported
by every host. The web interface uses the same permissions and API as the plugin.

Sign-in may be necessary. Let the user enter credentials through the authentication
surface. Do not paste tokens into URLs or request secrets in chat. Opening the
dashboard does not authorize publication, membership changes, or an account upgrade.
