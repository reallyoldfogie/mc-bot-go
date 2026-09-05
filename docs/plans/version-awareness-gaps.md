# Plan: Close remaining version-awareness gaps

Status legend: `[ ]` not started · `[~]` in progress · `[x]` done

Source: gap analysis done in-session (see summary below). Most of the
codebase already resolves packet IDs/behavior dynamically through
`models.PacketMgr` (one instance per Minecraft version). The gaps below are
the places that still assume a single version, found while auditing every
`packetMgr` call site plus everything under `bot/screen`, `bot/pinglist.go`,
and `utils/checkServerVersion.go`.

## Gap summary (baseline, before this plan)

1. `bot/screen/generic_container.go`'s `containerTypeRegistry` is a
   hardcoded snapshot of one version's container-type protocol IDs (docs say
   "Protocol version: 1.21.5 (770)"). `LoadContainerTypesFromRegistry` can
   refresh it from a `registries.json` file, but that's opt-in and nothing
   wires it to the version actually being played.
2. Slot component (de)serialization (`bot/screen/screen.go`'s
   `Slot.WriteTo`/`ReadFrom`, `bot/screen/hashops.go`) always uses
   `mc-protocol-go/data/1.21.5/basetypes.SlotComponentTypeMappings`, even
   though `ContainerClick` (same file) already has a deliberate pre/post
   1.21.5 split for the surrounding `Slot` vs `HashedSlot` wire format. The
   component-name table was never given the same split.
3. `bot/pinglist.go` (`PingAndList`/`PingAndListTimeout`/`PingAndListContext`)
   always sends `bot.ProtocolVersion` (767 / 1.21.1) as the handshake's
   declared protocol version, with no way for a caller to ping as a
   different version.
4. (Reviewed, not a gap) `utils/checkServerVersion.go` and `bot/pinglist.go`
   hardcode the Handshake/Status packet IDs as literals instead of resolving
   them via `packetMgr`. This matches `bot/mcbot.go`'s own `join()`, which
   does the same for the real handshake — the status-protocol packet IDs
   (`Handshake=0x00`, `StatusRequest=0x00`, `PingRequest=0x01`) have never
   changed across any Minecraft version, so there's nothing for a
   per-version `packetMgr` to resolve here. No code change; just documenting
   why this isn't in scope.

## Phase 1 — Container type registry: source from live server data ✅

- [x] Add a pure helper (`buildContainerTypesFromRegistryEntries`) that maps
      `"minecraft:menu"` registry entries (`name -> protocol ID`) to
      `ContainerTypeInfo` using the existing `containerMetadata` shape table,
      returning names it couldn't map (forward-compat: a future container
      type the running binary doesn't know the shape of yet).
- [x] Add `(*manager) populateContainerTypesFromLiveRegistry()`, called once
      per manager (via `sync.Once`) from `onOpenScreen` — by the time any
      `ClientboundOpenScreen` packet can arrive, configuration (and thus
      registry-data parsing in `bot/configuration.go`) has already
      completed, so this needs no dependency on construction order relative
      to `JoinServer`.
- [x] Add `(*manager) getContainerTypeInfo(id)`, checking the per-manager
      live-registry map first and falling back to the existing package-level
      `GetContainerTypeInfo` (hardcoded defaults / anything registered via
      `RegisterContainerType` or `LoadContainerTypesFromRegistry`). Wire
      `onOpenScreen` to use it instead of the package-level function
      directly.
- [x] Add a mutex around the package-level `containerTypeRegistry` map
      (`RegisterContainerType`/`GetContainerTypeInfo` were unsynchronized —
      a pre-existing latent data race, now relevant since population can
      happen at runtime instead of only at start-of-day file loading).
- [x] Update `bot/screen/CONTAINER_TYPES.md` to describe live-registry data
      as the primary source and the hardcoded map as a fallback.

Landed: `generic_container.go` (mutex), `registry_loader.go` (new helper +
manager methods), `screen.go` (struct fields + `onOpenScreen` wiring),
`screen_test.go` (`TestBuildContainerTypesFromRegistryEntries`,
`TestManagerGetContainerTypeInfo_PrefersLiveRegistry`), `CONTAINER_TYPES.md`.

Known limitation kept as-is: the package-level `containerTypeRegistry`
fallback is still process-wide, so two concurrently-running bots against
different server versions in one process can still cross-contaminate each
other's *fallback* entries (this only matters for container types the live
registry didn't provide, e.g. an unrecognized/very new type on one of the
two connections). Making the fallback per-manager too would mean copying the
full default table into every manager instance for no benefit in the common
case; not worth it unless it's actually hit in practice.

## Phase 2 — Slot component type mapping: version-split like `ContainerClick` ✅

- [x] Split `bot/screen/hashops.go`'s component name/ID tables into a
      pre-1.21.5 table (built from
      `mc-protocol-go/data/1.21.1/basetypes.SlotComponentTypeMappings`) and
      the existing post-1.21.5 table, matching the exact representative-version
      choice `screen.go` already documents for `ContainerClick`.
- [x] Select between them with a process-wide flag
      (`SetCurrentSlotComponentEncoding` / default: hashed=true), mirroring
      `mc-protocol-go/models.SetCurrentNBTVersion`'s `atomic.Pointer` +
      nil-means-default pattern — needed because `Slot.WriteTo`/`ReadFrom`
      implement `pk.Field` and have no way to receive a `packetMgr` through
      their `io.Reader`/`io.Writer`-only signatures.
- [x] Set it from `NewManager`, right next to the existing
      `models.SetCurrentNBTVersion(packetMgr.Name())` call, using the same
      `hashedSlotProtocolThreshold` already used by `usesHashedItemSlots()`.
- [x] Add tests exercising both tables.

Landed: `hashops.go` (split tables + atomic selector), `screen.go` (wired
into `NewManager`), `screen_test.go`
(`TestComponentTypeMapping_VersionSplit`,
`TestSlotComponentUsesHashedTable_DefaultsToHashed`). Confirmed the split
actually matters: component ID 10 is `can_place_on` pre-1.21.5 but
`enchantments` post-1.21.5 — the two tables genuinely disagree, not just a
formality.

Known limitation, accepted rather than solved here (matches the accepted
scope of the existing `ContainerClick` fix): this only distinguishes two
buckets (pre-1.21.5 / 1.21.5+), same as `ContainerClick`. If a slot
component is ever added, renamed, or renumbered *within* the 1.21.5+ range
(e.g. present in 1.21.9 but not 1.21.5), this table won't see it — it'll
fail loudly with "unknown slot component type" rather than silently
mis-encoding, which is the existing, deliberate failure mode elsewhere in
this file. A real fix would be `mc-protocol-go`'s `PacketMgr` exposing a
`GetSlotComponentTypeName`/`ID` pair per version, the same way it already
does for `GetEntityTypeID`; that's a sibling-repo change and out of scope
here.

## Phase 3 — `bot/pinglist.go`: parameterize protocol version ✅

- [x] Add `PingAndListVersion`, `PingAndListVersionTimeout`,
      `PingAndListVersionContext` taking an explicit `protocolVersion int32`.
- [x] Make the existing `PingAndList`/`PingAndListTimeout`/
      `PingAndListContext` thin wrappers around the new functions using
      `bot.ProtocolVersion` as before, so behavior for existing callers is
      unchanged.
- [x] Add a short comment on the `Handshake`/status packet-ID literals
      (here and in `utils/checkServerVersion.go`) noting they're
      protocol-invariant by design, addressing gap #4 above with
      documentation rather than a code change.

Landed: `bot/pinglist.go` (new version-parameterized entry points + doc
comments), `utils/checkServerVersion.go` (comment only, no functional
change — it already took an explicit `protocolVersion` parameter). No new
tests here: pinglist.go has no unit tests today either (only an
`ExamplePingAndList` requiring a live server at `localhost:25565`), and the
new functions are thin, easily-inspectable wrappers.

## Phase 4 — Wrap-up ✅

- [x] `go build ./...`, `go vet ./...`, `go test ./...` — all clean.
      (Also ran `go test -race ./bot/screen/...` given the new mutex/
      `sync.Once`/`atomic.Pointer` usage — clean.)
- [x] Update this document's statuses to reflect what actually landed.

Note: this repo sits under a parent Go workspace
(`/home/reallyoldfogie/src/github.com/reallyoldfogie/go.work`) whose vendor
directory is out of sync with its own `go.mod` (a pre-existing issue,
unrelated to this work — `go: inconsistent vendoring ... github.com/
reallyoldfogie/mc-bot-go@v0.2.0: is explicitly required in go.mod, but not
marked as explicit in vendor/modules.txt`). All commands above were run with
`GOWORK=off` to build this module standalone via its own `go.mod` replace
directives, per this repo's own CLAUDE.md. Worth fixing the workspace vendor
dir (`go work vendor` from the workspace root) at some point, but that's
outside this module and outside this plan's scope.

## Phase 5 — Optional per-connection SlotCodec (added after Phase 4, supersedes Phase 2's global for callers who supply one) ✅

Added after further discussion post-wrap-up: Phase 2's atomic-flag/
two-bucket approach is a reasonable built-in default, but it's still
process-wide state and only distinguishes two version buckets. mc-agent
already solves exactly this shape of problem for login/configuration —
`mc-agent/handler_versions` has one wrapper package per supported version
implementing `mc-agent/models.VersionHandler`, registered via `init()` into
`common.RegisterVersionHandler`, and bridged into `bot.VersionHandler` via
`mc-agent/agent/version_handler_adapter.go` + `Client.SetVersionHandler`.
This phase extends that same, already-proven seam to slots — a plain
interface + optional-interface detection, no generics (per instruction: the
version dispatch here is a runtime decision, which is what interfaces are
for; generics would only apply to code *inside* per-version wrapper bodies,
and Go's structural-constraint generics can't paper over content that's
genuinely different per version anyway — e.g. the window/container ID
field's own wire type differs, plain byte pre-1.21.5 vs VarInt from 1.21.5
on, confirmed directly against `mc-protocol-go/data/1.21.1/basetypes.ContainerID`
vs `data/1.21.5/basetypes.ContainerID`).

- [x] Add `bot.Client.VersionHandler() VersionHandler` getter (`bot/client.go`)
      — it only had a setter before.
- [x] Define `screen.SlotCodec` interface (`bot/screen/slot_codec.go`):
      `DecodeSlot(io.Reader) (Slot, int64, error)` and
      `SendContainerClick(conn bot.PacketWriter, windowID int, stateID int32, slot int16, button byte, mode int32, changedSlots ChangedSlots, cursor *Slot) error`
      — the latter builds the *entire* packet (not just per-item encoding)
      since more than the item format varies by version.
- [x] `NewManager` detects one via the same optional-interface pattern
      `bot/version_handler.go` already uses: `if sc, ok :=
      c.VersionHandler().(SlotCodec); ok { m.slotCodec = sc }`. Stored once,
      per manager instance — no new global.
- [x] `ContainerClick` uses `m.slotCodec.SendContainerClick(...)` when set,
      falling back to the existing `buildHashedContainerClick`/
      `buildPlainContainerClick` (Phase 2's heuristic) otherwise, unchanged.
- [x] `onSetContentPacket`, `OnSetSlot`, `onSetPlayerInventory` rewritten
      from `p.Scan(..., pk.Array(&slotData), &carriedItem)` to manual
      `bytes.NewReader(p.Data)` + per-slot `m.decodeSlot(r)` calls (uses
      `m.slotCodec` when set, `Slot.ReadFrom` otherwise) — this was
      necessary because `Slot.ReadFrom` has no way to reach `m.slotCodec`
      through `pk.Field`'s `io.Reader`-only signature; the rewrite is
      verified equivalent to `p.Scan`'s own implementation (a single
      `bytes.NewReader(p.Data)` + sequential `ReadFrom` calls — confirmed by
      reading go-mc's `Packet.Scan` and `Ary[LEN].ReadFrom` directly) and
      covered by a regression test with the codec absent.
- [x] Tests: `TestNewManager_PicksUpSlotCodecFromVersionHandler`,
      `TestNewManager_NoSlotCodecWhenNotProvided`,
      `TestContainerClick_UsesSlotCodecWhenSet`,
      `TestOnSetContentPacket_DecodesEmptySlotsWithoutCodec` (regression),
      `TestOnSetContentPacket_UsesSlotCodecWhenSet`,
      `TestOnSetSlot_UsesSlotCodecWhenSet`,
      `TestOnSetPlayerInventory_UsesSlotCodecWhenSet`.
- [x] `go build`/`go vet`/`go test`/`go test -race` all clean.

This resolves both residual limitations noted under Phase 2: no more
process-wide global for callers who supply a `SlotCodec` (it's on `m`,
scoped to that connection), and no more two-bucket approximation (a real
per-version implementation can be exact). Phase 2's built-in heuristic is
untouched and remains the default for callers that don't supply one.

**Follow-up, not done here (separate repo):** implementing `SlotCodec` for
each of mc-agent's 11 supported versions. mc-agent's existing per-version
container code (`handler_versions/v1_21_1/containers.go`) and its
`models.InventorySlot` type are currently less complete than what's needed
here (`InventorySlot` carries raw `NBT []byte` rather than decoded
components, and that file's own comments note simplified/stubbed component
handling) — so this is real implementation work on mc-agent's side, not
just wiring an adapter.

## All phases complete

Every phase above landed. Summary of what changed:

- **Container types** (`bot/screen`): now sourced from the connected
  server's live `"minecraft:menu"` registry data per-manager, with the old
  hardcoded single-version map demoted to an (now thread-safe) fallback.
- **Slot component encoding** (`bot/screen/hashops.go`): split into
  pre/post-1.21.5 tables selected by the same protocol-version threshold
  already used for `ContainerClick`'s `Slot`/`HashedSlot` wire-format
  switch, closing the inconsistency flagged in the original gap analysis.
- **Server-list ping** (`bot/pinglist.go`): protocol version is now an
  explicit parameter via `PingAndListVersion`/`*Timeout`/`*Context`, with
  the original functions kept as back-compat wrappers.
- Documented (no code change) why the Handshake/status packet IDs in
  `bot/pinglist.go` and `utils/checkServerVersion.go` are intentionally
  literal rather than `packetMgr`-resolved.
- **`SlotCodec` optional-interface seam** (Phase 5, `bot/screen/slot_codec.go`
  + `bot.Client.VersionHandler()`): a caller that already maintains real
  per-version protocol code (like mc-agent's `handler_versions`) can now
  plug it in via the same `Client.SetVersionHandler` mechanism already used
  for login/configuration, getting exact per-version Slot decode and
  `ServerboundContainerClick` encode instead of the Phase 2 heuristic, with
  no process-wide state for the connections that use it.

Remaining known limitations (accepted, documented inline above and in the
relevant source files, not fixed here):
- The container-type *fallback* map is still process-wide (Phase 1's "Known
  limitation" note) — matters only for container types a connection's live
  registry data didn't cover.
- Phase 2's built-in slot-component heuristic (still the default when no
  `SlotCodec` is supplied) is both process-wide and only two buckets
  (pre/post-1.21.5) — Phase 5's `SlotCodec` is the escape hatch for a caller
  that needs better than that, but nothing requires supplying one.
- No `SlotCodec` implementations exist yet for any real version — that's
  the mc-agent-side follow-up noted under Phase 5, not done here.
