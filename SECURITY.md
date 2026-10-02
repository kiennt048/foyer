# Security policy

Foyer authenticates access to networks. A vulnerability here is not an
inconvenience — it is unauthorised access to somebody's factory, office or hotel.

## Reporting a vulnerability

**Do not open a public issue.**

Email the maintainer, **Nguyễn Trung Kiên**, at **kiennt048@gmail.com**, or use
GitHub's private vulnerability reporting (Security → Report a vulnerability) on
this repository.

Please include what you were doing, what happened, and what you expected. A
proof-of-concept helps but is not required to report.

We will acknowledge your report, keep you updated while we investigate, and
credit you in the advisory unless you prefer otherwise.

## Supported versions

Pre-release. Until the first tagged release, only `main` is supported.

## Scope

In scope: the Go control plane, the generated FreeRADIUS configuration, both
frontends, the Docker images, and anything under `lab/` that could mislead a
contributor into shipping an unsafe assumption.

Out of scope: vulnerabilities in FreeRADIUS itself (report those to
[the FreeRADIUS project](https://www.freeradius.org/security/)), and in NAS
vendor firmware.

**In scope and worth saying explicitly:** if you find that Foyer *generates*
FreeRADIUS configuration that is insecure — a missing
`require_message_authenticator`, a permissive client definition, weak EAP
settings — that is a Foyer vulnerability even though the resulting file is
"just FreeRADIUS config". Generating correct configuration is the product.

## Known and accepted limitations

These are documented design constraints, not vulnerabilities. Reporting them is
welcome but they are already known:

- **A captive portal is a billing and consent boundary, not a security
  boundary.** Grants are bound to a MAC address, which is transmitted in the
  clear on every frame and can be spoofed. Use 802.1X where security matters.
- **EAP-TLS is the only 802.1X method the generator emits.** EAP-TTLS/PAP is
  planned. PEAP-MSCHAPv2 is not generated: it requires storing an NT hash, which is
  password-equivalent, so if it is ever offered it will be an explicit per-identity
  opt-in with the hash isolated in its own encrypted store.
- **Quota enforcement is sampled**, bounded by the RADIUS accounting interval.
- **The pre-authentication walled garden is unauthenticated egress** by
  definition. It is quota-limited and rate-limited, but it is reachable without
  identity.

## What we ask of deployers

- Do not expose the admin interface to the guest network.
- Do not disable `require_message_authenticator` warnings without understanding
  [BlastRADIUS](https://www.freeradius.org/vul_notifications/2024/07/09/blastradius.html).
- Keep the container images updated; FreeRADIUS security releases matter to you
  even though you never configure it.
