# NovaVPS 0.2.0-dev — Phase 2: IPv4/IPv6 IPAM

## Added

- Networking page in the NovaVPS dashboard.
- IPv4 and IPv6 CIDR pool registration.
- Validation for public unicast address ranges, including common private, shared, documentation, link-local, multicast, and reserved blocks.
- Pool overlap detection.
- Optional in-prefix gateway metadata.
- Address reservations for named servers, VMs, containers, and other targets.
- Reservation release and protection against deleting pools that still have reservations.
- Persistent IPAM state in `network.json` under the NovaVPS data directory.
- Authenticated API endpoints for pool and allocation management.
- Backend tests for address validation, allocation order, persistence, and ownership attestation.

## Important limitations

This release is an IP address management (IPAM) control-plane milestone. It does not:

- Obtain IP addresses from an ISP, hosting provider, RIR, or upstream network.
- Verify registry ownership or upstream routing; the administrator attests to authorization.
- Configure Linux interfaces, bridges, nftables/iptables, DHCP, IPv6 router advertisements, NAT, BGP, or static routes.
- Attach public addresses to Docker containers, KVM guests, or ordinary VMs.
- Guarantee public reachability.

A reservation is only a record in NovaVPS. The next networking milestone must add a least-privilege node agent, provider-aware routing checks, guest network configuration, rollback, and external reachability validation. Shared IPv4 NAT must remain clearly distinguished from unique dedicated public IPv4 addresses.
