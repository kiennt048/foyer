# Personal data in Foyer — what is stored, where, for how long, and how to erase it

*For the operator of a Foyer deployment: the venue, or whoever runs the network on its
behalf.*

> **Tóm tắt (Tiếng Việt).** Foyer lưu địa chỉ MAC, tên đăng nhập, địa chỉ IP, điểm truy
> cập, tên mạng (SSID), trang khách định mở và lịch sử phiên của khách dùng Wi-Fi. Mỗi
> loại dữ liệu có **thời hạn lưu giữ** riêng (bảng bên dưới), cấu hình được trong trang
> quản trị **Privacy**, và một tác vụ chạy định kỳ trong dịch vụ bảo trì của Foyer
> (`foyer maintenance`) tự xoá dữ liệu quá hạn. Khi một người yêu cầu xoá dữ liệu của mình, người vận hành dùng thao tác
> **"Erase one person's data"** (theo địa chỉ MAC, theo tài khoản khách, theo **số điện
> thoại** — với khách đăng nhập bằng mã SMS — hoặc theo **địa chỉ email** khách đã khai):
> Foyer xoá mọi bản ghi của người đó trong mọi bảng liệt kê ở đây và ghi một dòng vào nhật
> ký kiểm toán — dòng đó **không** chứa địa chỉ MAC, số điện thoại hay email đầy đủ. Các
> dòng nhật ký kiểm toán cũ nói về người đó **được giữ lại** (đó là hồ sơ trách nhiệm của
> nhân viên), nhưng mọi chỗ ghi MAC, tên đăng nhập, số điện thoại hay email được thay bằng
> một **bí danh** (`pseudonym:…`) không thể đảo ngược nếu không có khoá của hệ thống. Việc xoá **không** chạm tới bản sao lưu (backup) và nhật ký
> của container; nhật ký được xoay vòng theo dung lượng (tối đa 50 MB mỗi container) và
> Foyer chỉ ghi MAC đã che bớt — xem mục "What this list does not reach". Bạn (người vận hành) là **bên kiểm
> soát dữ liệu** theo Luật Bảo vệ dữ liệu cá nhân 91/2025/QH15; Foyer là công cụ. Tài
> liệu này không phải tư vấn pháp lý.

**This page is checked against the code.** `internal/store/privacy_test.go`
(`TestOperatorDocumentMatchesTheCode`) fails if a table or column below is not the one
the code erases, or if a default period here differs from the code's. Its sibling
`TestEveryPersonalColumnInTheSchemaIsRegistered` fails if the database gains a column
that looks like a MAC, a username, a phone number or an e-mail address and nobody has
said how it is erased. So this list is complete in the sense that matters: a new one
cannot be added silently.

---

## Who is responsible for what

- **You are the data controller** (*bên kiểm soát dữ liệu cá nhân*). You decide why the
  data is collected (to let guests on the network, to answer "why can't this guest get
  online", to keep a record of sessions), how long it is kept, and you answer the guest
  who asks.
- **Foyer is software you run.** It stores what it needs to authenticate and account for
  guests, enforces the periods you set, and gives you an erasure that is complete for
  what it stores. It does not decide your legal basis or your retention obligations.
- **Nothing here is legal advice.** Where a law obliges you to keep records longer than
  the defaults below, raise the period. The citations are what the authors of this page found
  and checked when it was written; the ones marked *secondary source* should be read
  against the official gazette text before you rely on them.

## The law this is written against

| Instrument | What it says that matters here |
|---|---|
| **Law on Personal Data Protection No. 91/2025/QH15** (passed 26 June 2025, in force 1 January 2026 — Article 38) | Article 3 (principles), clause 3: personal data is stored *for a period appropriate to the purpose of processing, unless otherwise prescribed by law*. Article 4 clause 1: the data subject may *request provision, deletion, or restriction of processing*. Article 14 (*deletion, destruction and de-identification*): data is deleted when the subject asks, when the purpose is fulfilled, or when the storage period has expired — subject to the exceptions in Article 14(2). Article 9: consent. *Secondary source* (LuatVietnam's English translation; EY Vietnam legal alert) — the article and clause numbers were not checked against the official Vietnamese gazette text by this author. |
| **Decree 356/2025/ND-CP** (issued 31 December 2025, in force 1 January 2026, replaces Decree 13/2023/ND-CP) | Detailed guidance for Law 91/2025. Article 5 covers how data-subject requests are handled; per the published summaries, a deletion request is acknowledged within 2 working days and carried out within 20 days, extendable. *Secondary source* (LuatVietnam summary; EY Vietnam legal alert, March 2026) — **UNVERIFIED** against the gazette text by this author. |
| **GDPR (Regulation (EU) 2016/679)**, if you serve EU residents or run a deployment in the EU | Article 5(1)(e) storage limitation; Article 5(2) accountability; Article 17 right to erasure, with Article 17(3)(b) (a legal obligation to keep the data) as the exception; Article 12(3) — answer within one month; Article 30 records of processing. Recital 30: device identifiers are personal data. |

Other Vietnamese rules can oblige a venue that provides public Internet access to keep
some records — review, with counsel, at least Decree 147/2024/ND-CP (Internet services
and public Internet access points) and Decree 53/2022/ND-CP (Cybersecurity Law). The
author did **not** find a specific retention figure in either that applies to a guest
Wi-Fi portal and makes no claim that one does or does not.

---

## What is stored, and where

Every table below is in the deployment's PostgreSQL database. A column marked *identifies*
is what an erasure matches on; one marked *with its row* describes the guest and is
removed when its row is; *not guest data* is listed because it looks like personal data
and says why an erasure does not touch it.

| Table | Column | What it holds | Erased by |
|---|---|---|---|
| `radacct` | `callingstationid` | client MAC (Calling-Station-Id) | identifies: MAC |
| `radacct` | `username` | guest username, or the MAC under MAC authentication | identifies: MAC, account, phone |
| `radacct` | `calledstationid` | access point MAC and SSID | with its row |
| `radacct` | `framedipaddress` | IP address the guest was given | with its row |
| `radpostauth` | `callingstationid` | client MAC (Calling-Station-Id) | identifies: MAC |
| `radpostauth` | `username` | username offered at sign-in, or the MAC under MAC authentication | identifies: MAC, account, phone |
| `radpostauth` | `calledstationid` | access point MAC and SSID | with its row |
| `authorization_events` | `client_mac` | client MAC | identifies: MAC |
| `authorization_events` | `username` | username offered at sign-in (a phone guest's number) | identifies: account, phone |
| `authorization_events` | `ap_mac` | access point MAC | with its row |
| `portal_sessions` | `client_mac` | client MAC | identifies: MAC |
| `portal_sessions` | `ap_mac` | access point MAC | with its row |
| `portal_sessions` | `ssid` | network name | with its row |
| `portal_sessions` | `original_url` | page the guest was trying to reach | with its row |
| `portal_grants` | `client_mac` | client MAC | identifies: MAC |
| `portal_grants` | `account_id` | guest account that signed in | identifies: account |
| `portal_grants` | `ap_mac` | access point MAC | with its row |
| `portal_grants` | `phone` | phone number a phone guest verified | identifies: phone |
| `portal_addresses` | `client_mac` | client MAC last seen from an address (RFC 8908) | identifies: MAC |
| `portal_addresses` | `address` | IP address a device reached the portal from | with its row |
| `phone_codes` | `client_mac` | client MAC that asked for a sign-in code | identifies: MAC |
| `phone_codes` | `phone` | phone number a sign-in code was sent to | identifies: phone |
| `phone_codes` | `code_mac` | keyed hash (HMAC) of a sign-in code | not guest data — a message authentication code, not a MAC address; goes with its code row |
| `guest_details` | `name` | name a guest gave at sign-in | with its row (deleted with its grant) |
| `guest_details` | `phone` | phone number a guest gave at sign-in | identifies: phone (and deleted with its grant) |
| `guest_details` | `email` | e-mail address a guest gave at sign-in | identifies: e-mail (and deleted with its grant) |
| `marketing_consents` | `client_mac` | device the marketing consent was given from | identifies: MAC (and an account, through its grants) |
| `marketing_consents` | `name` | name given with a marketing consent | with its row; cleared when the consent is withdrawn |
| `marketing_consents` | `phone` | phone number given with a marketing consent | identifies: phone; cleared when the consent is withdrawn |
| `marketing_consents` | `email` | e-mail address given with a marketing consent | identifies: e-mail; cleared when the consent is withdrawn |
| `marketing_consents` | `account_id` | guest account the consent was given under | identifies: account — kept on the consent itself, so an account erasure finds it after the grant has aged out |
| `accounts` | `username` | guest account username | identifies: account |
| `accounts` | `display_name` | guest account display name | with its row |
| `account_pap_verifiers` | `username` | 802.1X username | identifies: account (deleted with the account) |
| `radius_pap_verifiers` | `username` | 802.1X username, as FreeRADIUS reads it | identifies: account (a view; empty once the account is gone) |
| `operators` | `username` | operator sign-in name | not guest data — an operator; removed by deleting the operator |
| `operators` | `display_name` | operator display name | not guest data — an operator; removed by deleting the operator |
| `audit_log` | `actor_username` | operator who made a change | not guest data — kept for accountability until the audit period ends |
| `audit_log` | `subject_name` | what an entry is about, by name: a session's or a grant's MAC, an account's username | **pseudonymised**: the entry is kept, the identifier is replaced (see below) |
| `audit_log` | `subject_id` | what an entry is about, by id: an account's id | **pseudonymised** |
| `audit_log` | `params` | the details of the change, which may name the guest it was about | **pseudonymised** |
| `controllers` | `username` | controller API account | not guest data — a credential for the venue's own controller |
| `access_points` | `mac` | access point MAC | not guest data — network equipment |

`radacct` and `radpostauth` are written by FreeRADIUS, not by Foyer. Everything else is
Foyer's own. A MAC on a modern phone is usually a *private* address the phone chose for
this one network, which makes it a pseudonym — still personal data, because it
identifies one person's device on your network.

### Guest details and marketing consent

Under **Guest data** in the console you choose what the sign-in forms ask for: a name, a
phone number and an e-mail address, each **optional unless you mark it required** to get
online. A guest's details are stored **with the grant they were given for**
(`guest_details`) and go when it goes; they are your record of who you let on and are
**not** a mailing list, and no export carries them.

Marketing consent is a **separate checkbox, never ticked in advance and never required**
to get online. Accepting your terms is not consent, and Foyer never reads it as one. A row
in `marketing_consents` exists only for a guest who ticked the box — it records when, the
device, the contact details they gave, and the exact sentence they were shown (kept under
its SHA-256, so it stays readable after you reword it). A guest who does not tick it
leaves **no marketing record at all**. Vietnam's Decree 356/2025/ND-CP forbids pre-ticked
boxes and treating silence as consent and asks that consent be verifiable as to its time
and content, separated by purpose, and easy to withdraw; Purple, the product most venues
compare against, ships the same separate, unticked checkbox.

A consent is withdrawn by the guest themselves — the connected page carries a *Stop
marketing messages* button — or by an administrator on their behalf (**Guest data →
Withdraw**). Withdrawal **clears the name, phone number and e-mail address** and keeps
when it was given, which text, and when and by whom it was withdrawn: your evidence that
you stopped. The **marketing-consent export** (administrators only, audited as
`data-exported`) carries every consent given *or withdrawn* in the dates you choose, so a
mailing tool loaded from last month's export learns this month who to stop writing to.

**What this list does not reach**, and what you have to handle yourself:

- **Your mailing tool.** A consent exported and loaded into a mailing service is a copy
  Foyer cannot reach. Import each export's withdrawn rows as unsubscribes, and erase the
  person there too when they ask to be erased.

- **Backups.** `deploy/backup.sh` writes a full database backup (`pg_dump`) under
  `/opt/foyer/state/backups/` — daily once its timer is installed, and before every
  upgrade — and keeps the newest fourteen by default (`deploy/README.md`, *Backups and
  restore*). Each is a complete copy of every table above as it was that day. An erasure does not reach a backup, and restoring
  one brings back every guest erased since it was taken — so after a restore, run
  again every erasure you carried out after the backup's date (keep a list of them, by
  the date and masked subject in the audit log's `data-subject-erased` entries).
  Keep no more backups than your longest retention period justifies, and remember
  that copies you move off the host are outside Foyer's reach too. The sealed columns
  in a backup are ciphertext without the deployment's key (`deploy/README.md`).
- **Process logs.** Each container's log is **rotated by size**: at most 5 files of
  10 MB — 50 MB per container, the oldest discarded (`deploy/compose.yaml`, the
  `x-logging` block). That is a bound by size, not by time: days of history on a busy
  venue, weeks on a quiet one. An erasure does not reach a log line, so what the logs
  carry is kept small at the source:
  - the control plane writes a guest's MAC **masked** to its last two octets
    (`••:••:••:••:11:22`, the audit log's form) and a guest's IP address truncated to
    its network (`/24`, or `/48` for IPv6) — in the stale-session reaper's lines, a
    failed portal authorization, and the web server's own handshake errors;
  - **FreeRADIUS writes one line per accept and reject with the username and the
    client's MAC in full** (`Login OK: [user] (from client … cli AA-BB-…)`), which is
    what `radpostauth` already holds and the one diagnostic line its operators rely on.
    It is not masked; the 50 MB rotation is what bounds it.
- **Your controller and your access points.** UniFi, Omada and others keep their own
  client history. Foyer cannot erase it; use the vendor's own tools.

---

## How long it is kept

Each category has a default. Change any of them in the admin console under **Privacy**
(operation `updateRetentionPolicy`, administrators only); the change is recorded in the
audit log as `retention-policy-changed`. A sweep runs in the maintenance service
(`foyer maintenance`, its own container in a `deploy/` installation), by default every
five minutes, and removes what is older than the period, 500 rows per
statement. Both figures are set on the same screen, under **Maintenance** (operation
`updateMaintenanceSettings`, audited as `maintenance-settings-changed`): the interval
from one minute to one day, the batch from 50 to 10 000 rows. A change takes effect on
the running maintenance jobs without a restart. The console shows when each category was
last swept and how many rows it removed.

An interval longer than the default makes every period in the table below longer by
up to that interval: a row becomes eligible at its period and is removed on the next
sweep after that.

| Category | Default (days) | Allowed | Tables | What "older than the period" means |
|---|---|---|---|---|
| `accounting` | 90 | 1–3650 | `radacct` | a session that **ended** that long ago; a session still open is never removed |
| `authAttempts` | 30 | 1–3650 | `radpostauth`, `authorization_events` | the sign-in attempt happened that long ago |
| `portalGrants` | 90 | 1–3650 | `portal_grants` | the grant **ended** that long ago: it expired or was revoked, or the same device was granted again at your venue, or — for a grant with no end — the device was last heard from at your venue (its newest accounting record there), or, never heard from, it was granted. A guest online on an unlimited grant keeps it. Its authorization events go with it |
| `audit` | 730 | 30–3650 | `audit_log` | the change was made that long ago. No entry is ever removed one by one; an erasure pseudonymises entries, it does not delete them |
| `guestAccounts` | 0 | 0–3650 | `accounts` | 0 means **kept until you delete or erase it**; otherwise, not used to sign in (or never used, and created) that long ago |
| `marketingConsents` | 730 | 30–3650 | `marketing_consents` | a consent **withdrawn** that long ago, or one still active that was **given** that long ago — a consent is not a licence forever, and two years is the refresh interval the UK ICO's guidance names. A guest's details given *without* consent (`guest_details`) go with their grant, under `portalGrants` |

**The lower bounds are held by the database, not only by the console.** A period below
a category's minimum — one day of audit log, say — is refused by PostgreSQL itself
(migration `00051`), whatever wrote it, and the sweep re-applies the minimum as well. So
the shortest the audit log can ever be is 30 days, which also means the entry recording
a shortening outlives it by at least that long. Foyer does **not** ask a second
administrator to approve a shorter period: many venues have exactly one, and none of the
products surveyed (PacketFence, authentik, Keycloak, UniFi, Omada) documents one for its log retention; what they and
Foyer do instead is restrict the change to administrators and audit it. A change of
period **fails if its audit entry cannot be written**.

Why these numbers: `accounting` and `portalGrants` keep a quarter, enough to answer a
question about last month's sessions; `authAttempts` is for diagnosing "why can't this
guest get online", which is a question about this week (PacketFence keeps its auth log
one week); `audit` is an operator record that should outlive guest data (PacketFence keeps
its admin audit six months); guest accounts are identities you created, and removing
them on a timer is a decision for you — PacketFence's equivalent task ships disabled.

These are **not fixed by the product** and are **not** removed by the sweep:

- *portal sessions* (`portal_sessions`) — the short-lived binding of a browser to a
  device before sign-in. Removed as soon as it expires, by the maintenance sweep, because
  its lifetime is a security bound rather than a retention choice.
- *the address book* (`portal_addresses`, which device and grant were last seen from an
  IP address, so a phone's own captive-portal check — RFC 8908 — can be told it is
  online) — removed a day after the address was last seen, by the maintenance sweep; an
  erasure by MAC removes it at once, and the grant's removal takes it with it.
- *console sessions* (`operator_sessions`, which records an operator's browser user
  agent) — removed when they expire.
- *phone sign-in codes* (`phone_codes`, the number a code was sent to and the device
  that asked) — removed a day after they were sent, by the maintenance sweep, because
  the per-number limit (5 a day) is the only thing that reads them after five minutes.
  The per-site daily message count (`phone_code_sends`) names nobody and is kept.
- *vouchers* — a voucher code does not identify anyone. Expired vouchers are marked
  expired by the same sweep; batches stay until you delete them.

---

## How to erase one person's data

When a guest asks to be erased, you need one of four things from them:

- **The device's MAC address**, in any spelling (`AA-BB-CC-00-11-22`, `aa:bb:cc:00:11:22`,
  `aabb.cc00.1122`, `aabbcc001122`). On a phone it is under *Wi-Fi → (this network) →
  details*; tell the guest to read the address shown **for your network**, because a
  private address differs per network. Or
- **Their guest account**, if you created one for them. Or
- **Their e-mail address**, if they gave one on your sign-in form or with a marketing
  consent. It erases the sign-in form details and the marketing consents that give it
  (matched without regard to case); the device's sign-in itself is found by its MAC. An
  account whose *username* is an e-mail address is erased as the account. Or
- **Their phone number** — what a guest who signed in with a code by SMS, or typed a
  number into your sign-in form, can tell you. Any spelling (`0912 345 678`,
  `+84 912 345 678`). It erases every code sent to that number, every sign-in it was
  verified for — with the portal sessions, address-book entries and marketing consents
  bound to them — every sign-in form and marketing consent that gives it, and every
  sign-in and accounting record whose username is it, **at every venue**: a number is
  one person's wherever they used it. A voucher guest who only typed the number into
  your form loses the form's details; their voucher sign-in itself is found by their
  device's MAC.

Then, as an administrator: **Privacy → Erase one person's data** in the console, or the
`eraseDataSubject` operation (`POST /api/v0/privacy/erasures`). Foyer:

1. deletes every row in every table above that the subject identifies — every spelling
   of the MAC, the MAC used as a username under MAC authentication, or the account with
   its verifiers, its grants, the portal sessions bound to them, and every
   FreeRADIUS row carrying its username **at the account's venue**. A guest account's
   username is unique per venue, not per deployment, so `room101` at another venue is
   a different guest and is left alone; a row Foyer cannot attribute to any venue (its
   NAS matches no RADIUS client, its access point is unknown) is erased with the
   subject's;
2. answers with the count removed from each table, including the tables that held
   nothing, so you can tell the guest what was found;
3. **keeps the audit log's earlier entries about the person and replaces the
   identifier in them** — the MAC in any spelling, the username, the account id, the
   phone number, the e-mail address — with a pseudonym, `pseudonym:` and sixteen hex
   digits. The entry still says that somebody on your staff disconnected a guest on
   3 May; it no longer says who the guest was. The pseudonym is a keyed hash under a
   key derived from the deployment's `FOYER_KEK`, so the same person gets the same
   pseudonym in every entry (you can still see that three entries were about one
   guest) and nobody holding only the database can reverse it. The answer lists
   `audit_log` under **pseudonymised**, with how many entries were rewritten;
4. writes an audit entry, `data-subject-erased`, naming you, the time, the counts, and
   the subject **masked** (`••:••:••:••:11:22`, `••••••••678`, `m•••@•••.vn`) — a log
   that kept the identifier it records erasing would not have erased it. **If that
   entry cannot be written, the erasure fails** and Foyer's own tables are left as they
   were: an irreversible change with no record of who made it is not allowed.

The erasure is safe to repeat. If it fails part-way (the FreeRADIUS tables and Foyer's
own are erased in two steps, because they are written by different roles — the
FreeRADIUS step comes first and is not undone), the console says so; run it again, and
the second run is the one the audit log records.

**Verify it yourself:** search **Sessions** and **Sign-in attempts** for the MAC
afterwards — both should show nothing for it.

---

## How the deletion is controlled

Foyer never writes the two tables FreeRADIUS owns (`radacct`, `radpostauth`), with one
standing exception: the stale-session reaper may close a session nobody closed.
Retention and erasure need a second exception, and it is made **deliberately and
narrowly**:

- it is **DELETE only** — never insert, never rewrite, never truncate;
- it runs as its own PostgreSQL role, `foyer_retention` (migration `00020`), which holds
  only SELECT and DELETE on those tables, and the code refuses to start the sweep on a
  connection that could do more;
- it lives in **one file**, `internal/store/retention.go`, and the repository's CI
  checks refuse a DELETE on those tables anywhere else.

That is also why an erasure of FreeRADIUS data cannot be done by hand from the console's
database role: the application role can still only read those tables.
