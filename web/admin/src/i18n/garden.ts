/**
 * Copy for `/garden`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const garden = {
  "garden.effective.none.title": "Nothing is enforcing a walled garden in this site.",
  "garden.effective.none.body":
    "At least one device here cannot restrict traffic before sign-in, so a guest on that device reaches the whole internet without signing in. Every entry below is decoration for those devices.",
  /*
   * The verdict must not disappear.
   *
   * With the backend down, `garden` is `undefined`, so `garden?.effectiveKind ===
   * "none"` is false and the urgent notice above would simply not render — while
   * `garden.preAuthQuota`, a permanent structural fact needing no data, stays. **A
   * failing /garden would look calmer than a healthy one**, which is the dashboard's
   * own *absence is not zero* defect one screen along.
   *
   * Same voice as `dashboard.alarmsFailed`, deliberately: it is the same sentence
   * about the same failure, and this console has two ways of saying it already.
   */
  "garden.unknown.title": "Cannot load what this site allows",
  "garden.unknown.body":
    "Foyer cannot reach its own backend, so neither the entries below nor the verdict above them is current. Read the empty space as unknown — a site with no walled garden and a site nobody can ask look identical from here.",
  // Beside the control it disables, not in a tooltip: on a phone a tooltip is a
  // sentence nobody can reach, and this is the reason a control stopped working.
  "garden.unknown.cannotAdd":
    "A destination cannot be allowed until the backend answers again.",

  "garden.effective.ipRange.title": "This site can only allow destinations by address.",
  "garden.effective.ipRange.body":
    "Address ranges are shared: a CDN range hosts many customers, so allowing one service allows everything else behind the same edge.",

  "garden.col.destination": "Destination",
  "garden.col.kind": "Matched by",
  "garden.col.honoured": "Enforced by",
  "garden.col.note": "Note",
  "garden.col.added": "Added time",

  "garden.kind.fqdn": "Name",
  "garden.kind.ip-range": "Address range",
  "garden.kind.fqdnWhy": "Matched by name via SNI. One allowance means one destination.",
  "garden.kind.ipRangeWhy":
    "Matched by address. A range hosts many services, so this allows more than it names.",

  "garden.origin.required": "Required",
  "garden.origin.requiredWhy":
    "The portal cannot serve itself without this. It has no delete control for that reason.",

  "garden.honoured": (n: number, total: number) => `${n} of ${total}`,
  "garden.honouredNone": "No device",

  // Not "add one" — this state is a fault, not a starting point. The four required
  // entries are seeded for every site, so a garden with nothing in it means they did
  // not arrive, and the portal in that site cannot serve itself to a guest.
  "garden.empty.title": "No walled garden at all.",
  "garden.empty.description": "The portal's own address is missing, so guests see nothing.",

  "garden.noDevices.title": "No access points in this site.",
  "garden.noDevices.body":
    "Nothing enforces these entries because there is nothing here to enforce them. Register an access point first.",

  "garden.add.open": "Allow a destination",
  "garden.add.title": "Allow a destination",
  "garden.add.kind": "Matched by",
  "garden.add.value": "Destination",
  "garden.add.valueHintFqdn": "A hostname, for example payments.example.com",
  "garden.add.valueHintIpRange": "A CIDR range, for example 192.0.2.0/24",
  "garden.add.note": "Note",
  "garden.add.noteHint": "Why this is here. Read by whoever inherits this site.",
  "garden.add.submit": "Allow destination",
  "garden.add.failed.title": "Destination not added",
  // Two sentences, because the two reasons are not the same fact and the fix for
  // each is different. A device that cannot garden at all needs replacing or
  // accepting; a device that can only match addresses needs the entry rewritten as
  // a range. Collapsing them into "cannot restrict pre-sign-in traffic" says
  // something false about the second group.
  "garden.add.ignoredNone": (names: string) =>
    `This will do nothing on ${names}, which cannot restrict traffic before sign-in at all.`,
  "garden.add.ignoredByName": (names: string) =>
    `${names} can only match destinations by address, so a name entry does nothing there.`,
  "garden.ignoredNoneBy": (names: string) => `Not enforced by ${names} — no walled garden.`,
  "garden.ignoredByNameBy": (names: string) =>
    `Not enforced by ${names} — matches destinations by address only.`,

  "garden.delete.title": "Stop allowing this destination",
  "garden.delete.verb": "Stop allowing it",
  "garden.delete.consequence": (value: string) =>
    `Guests can no longer reach ${value} before signing in.`,
  "garden.delete.recovery":
    "It can be added again. A guest part-way through something on that destination loses it immediately.",

  "garden.preAuthQuota.title": "Traffic before sign-in is not capped.",
  "garden.preAuthQuota.body":
    "There is no identity yet, so nothing bounds what an unauthenticated device sends through these destinations. Per-device caps are not built.",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/garden.js"));

export const t = makeT({ ...core, ...garden });
