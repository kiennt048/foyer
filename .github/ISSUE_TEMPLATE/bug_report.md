---
name: Bug report
about: Something in Foyer does not behave as documented
title: ""
labels: bug
assignees: ""
---

<!--
Security vulnerabilities: do NOT open a public issue. See SECURITY.md.

Before posting logs: run deploy/support-bundle.sh, which redacts shared secrets and
passwords, and read what it produced. Raw `radiusd -X` output contains every RADIUS
shared secret in cleartext. Replace real MAC addresses, IP addresses and SSIDs you do
not want public (e.g. 00:00:5e:00:53:01, 192.0.2.10).
-->

## What happened

<!-- What you did, step by step, and what you saw. -->

## What you expected

## Where

- Foyer revision (`foyerctl version`, or the commit you built):
- Install: `deploy/install.sh` / `lab/` / other:
- Host OS:
- Access point / controller (vendor, model, firmware or Network Application version):
- Client device and OS, if the guest portal is involved (e.g. iPhone, iOS 18, captive-portal sheet):
- Sign-in method (voucher, account, phone code, click-through, 802.1X/EAP-TLS):

## Evidence

<!--
Any of: the console screen (screenshot), the reason shown under "Why can't this guest
get online?", the relevant lines from the support bundle, `deploy/verify.sh` output.
-->

## Anything else
