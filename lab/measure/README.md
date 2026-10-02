# `lab/measure` — the host that serves the portal to a phone

A dependency-free Go program that puts the guest-facing surface on the LAN over
**both HTTP and HTTPS at once**, with a request log built to answer one question
the phone cannot answer by itself: *did the client refuse the certificate, or did
it never ask?*

It takes no measurement. A person holds the phone: start it, join the phone to
the same network, open one of the URLs it prints, and keep the log it writes —
the log is the measurement. The questions it is built for are whether the iOS
Captive Network Assistant displays an HTTPS page whose certificate it does not
trust, whether it fetches the RFC 8908 endpoint over that connection, and what
the portal's environment looks like on a real device (viewport, colour scheme,
storage, fonts).

```bash
go run ./lab/measure                       # :8080 and :8443
go run ./lab/measure -http :80             # needs sudo; required for interception
go run ./lab/measure -proxy http://127.0.0.1:5171   # put the real portal behind it
go run ./lab/measure -probe web/portal/dist-probe  # add the portal's own probe page
```

| Flag | |
|---|---|
| `-http`, `-https` | listen addresses. Default `:8080` and `:8443` |
| `-host` | which LAN address to advertise, when the machine has several |
| `-probe` | the portal build's measurement output, served at `/{lang}/probe/` |
| `-dir` | a directory to serve under `/site/` |
| `-proxy` | reverse-proxy anything unmatched to an origin, so the real portal is reachable over the self-signed TLS host |
| `-state` | where the certificate lives. Default `~/.foyer/measure` |
| `-quiet` | drop bare TCP connections from the log |

## What it serves

| Path | |
|---|---|
| `/{lang}/probe/` | **the portal's own measurement page** — `-probe web/portal/dist-probe`; storage, viewport and colour scheme, and the HTTPS question, in the portal's languages and on the portal's stylesheet |
| `/probe` | the measurement page — all five on-device unknowns on one screen |
| `/log` | the request log, readable from the phone itself |
| `/captive-portal/api` | RFC 8908, `application/captive+json` |
| `/captive` | `POST state=false` flips the answer, so auto-dismiss is measurable |
| `/ca.crt` | the generated root, offered so iOS installs it — the fallback branch |
| `/switch` | 302 to the same page on the other scheme |
| `/hotspot-detect.html`, `/generate_204`, `/ncsi.txt`, … | the operating systems' own connectivity probes, logged |

## Three things in here are deliberate rather than incidental

**The certificate is shaped so iOS can only refuse it for being self-signed.**
`usableOnIOS` asserts an IP subjectAltName, a validity period under Apple's
398-day ceiling, `serverAuth`, and a SHA-256 signature — at startup, on the file
that is about to be served. A certificate on disk that fails any of them is
regenerated. Without that, "iOS refused it" could mean "iOS refused *this file*",
and the HTTPS question would have measured nothing.

**Nothing links an asset and no page contains an absolute URL.** The same frozen
rules as `web/portal`, for the same reason: a second request is one the CNA may
not make, and a half-rendered page measures the wrong thing. Asserted in
`pages_test.go`, with a negative control that injects an external stylesheet and
requires the scanner to flag it.

**The webfont is generated, not committed.** A same-origin `@font-face` needs a
real font file; a committed binary and a downloaded one are both worse. `font.go`
writes a TrueType font whose every printable character is one filled triangle, so
the page renders the literal words `NOT LOADED` and a reader sees either
triangles or the sentence. WOFF 2 is not covered — it needs Brotli, which the
standard library does not have, and this tool adds no dependency.

## Also here: the sign-in flood

`login-flood.sh` and `loginflood/` are a separate measurement that shares
nothing with the program above but the directory. With the lab up, the script
caps the control plane at the deployment's 320M, floods the console with
parallel sign-ins from several source addresses, and sends RADIUS
Access-Requests through FreeRADIUS during the flood. `body-flood.sh` is its
sibling: anonymous 1 MiB JSON bodies with no sign-in at all, the flood an
unbounded request decoder does not survive. Each script's header has the
procedure; each prints its numbers and writes them under `lab/.state/`.
