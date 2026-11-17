# Multi-platform deployment support (0.3.0-dev -> 0.4.0-dev)

- Added Vercel static-dashboard build configuration and a same-origin Node.js HTTP API proxy.
- Added Netlify static-dashboard build configuration and a same-origin Edge Function API proxy.
- Added a root Render Blueprint that deploys the existing Dockerized Go API and built dashboard from the `novavps/` monorepo directory.
- Added deployment documentation, environment-variable setup, and platform limitations.
- Clarified that hosted control-plane platforms do not provide KVM host privileges or public IP allocations.
- Documented Render Free idle spin-down and ephemeral filesystem data-loss risks.
- Existing root Cloud/Pterodactyl installer files are unchanged.
