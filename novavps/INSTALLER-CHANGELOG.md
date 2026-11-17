# NovaVPS v0.3.0-dev — Cloud-style Installer & Manager

This milestone adopts the interactive, menu-driven installation workflow used by the owner's `Cloud` repository while keeping NovaVPS as an independent SSH/IPAM control panel.

## Added

- `install.sh`: root-checked Ubuntu/Debian installer.
- Automatic Docker Engine + Compose plugin installation from Docker's signed APT repository when needed.
- Optional Caddy HTTPS profile with validated hostname input.
- Local-only default deployment; remote access instructions use an SSH tunnel.
- `/usr/local/bin/novavps` command wrapper.
- `scripts/novavps-menu.sh`: interactive terminal menu inspired by the Cloud repository's toolbox menu.
- CLI commands: `start`, `stop`, `restart`, `status`, `logs`, `url`, `backup`, `update`, `doctor`, `uninstall`, `purge`, and `help`.
- Data backup from the persistent Docker volume.
- Uninstall preserves data; destructive purge requires an explicit confirmation phrase.

## Safety improvements over a naive curl-pipe installer

- Installer validates OS, root privileges, public hostname input, and source-tree presence.
- No fixed administrator or database password is inserted.
- No raw management API port is exposed publicly by default.
- Public mode is opt-in and requires a real DNS name pointing at the machine.
- Uninstall and data deletion are separate operations.
- IPAM records remain reservations only; this installer does not claim to route or assign public IPs.

## Current limitations

- This installer expects the complete NovaVPS source tree beside `install.sh` (for example, after extracting the release ZIP). A public GitHub one-line `curl | bash` command should only be published after NovaVPS has a canonical repository and release URL.
- Automatic installation targets Ubuntu/Debian with systemd and APT.
- Public HTTPS requires a domain, DNS pointing to the server, and reachable inbound ports 80/443.
- Browser terminal, SFTP, agent networking, KVM provisioning, and real guest IP attachment are not implemented by this milestone.
- Full end-to-end Docker image build is not claimed as tested in this environment.
