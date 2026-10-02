# internal/redact — CONTRACT

How a guest's identifiers appear in a process log line. A leaf package: it imports
nothing of Foyer's, so every package that logs can use it.

## Invariants

1. **A log line never carries a guest's MAC, phone number or IP address whole.** A
   container log is personal data no erasure reaches and every support bundle copies
   (`docs/operator/personal-data.md`), so the line carries the masked form:
   - `MAC` — the last two octets, `••:••:••:••:11:22`, the audit log's form for an
     erased subject (`store.Subject.Masked` is this function);
   - `Phone` — the last three digits;
   - `IP` — the network: the /24 of IPv4, the /48 of IPv6;
   - `Email` — the first character of the local part and the top-level domain.
2. **A value of the wrong shape is not echoed.** `MAC("guest@example.com")` is
   `(not a MAC)`: a caller that passed the wrong field must not leak it verbatim.
3. **`ErrorLog` is every `http.Server`'s error log in `cmd/foyer`**, because
   `net/http` writes the client's address into a failed handshake's line and on the
   guest portal every phone that refuses a certificate produces one. `Line` masks
   every MAC, IPv4 and bracketed IPv6 address in text somebody else wrote.

## What it does not do

FreeRADIUS's own `auth = yes` lines carry the User-Name and the client's MAC whole; they
are FreeRADIUS's output, not a Go log line. Log rotation in `deploy/compose.yaml`
(`x-logging`) is what bounds them, and the operator document says so.
