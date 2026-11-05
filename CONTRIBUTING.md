# Adding another VOICSKY keyboard (or fixing something)

The tool is written for the **PULSE 96** (`0c45:8006`). Other VOICSKY boards use the same protocol as the Aether web console
(<https://aether.voicsky.com>), so adding one is mostly *finding out its constants* and *generating its layout*. This guide
is the order I would do it in. Open a pull request with what you have, even if only part of it works.

## Safety rules (please keep them)

- **Read first, write later.** `info`, `led`, `keymap` and `backup` only read. Take a backup before any write.
- **Only send commands that are in the table in the README / `dev.go`.** Never send the OTA / firmware range (`0x80`-`0x86`),
  the factory reset (`0x0F`) or anything you have not seen Aether send.
- **One program talks to the keyboard at a time.** Do not run the Aether web console and this tool together: two writers on one
  HID stream corrupt frames, and on a PULSE 96 that once made the board reset and ignore writes.
- Do not commit firmware images, the Windows helper, or copied Aether source. This repo only records how to *talk* to the
  hardware, plus data you generated (layouts).

## 1. Identify the device

```sh
lsusb | grep -i 0c45                         # VOICSKY uses vendor 0c45 (Sonix). Note the product id.
for d in /sys/class/hidraw/hidraw*; do echo "$d $(grep HID_ID $d/device/uevent)"; od -An -tx1 -v $d/device/report_descriptor | tr -d '\n '; echo; done
```

Find the **vendor-defined** interface: a report descriptor starting `06 xx ff` (a usage page `0xFFxx`) with 64-byte
(`0x95 0x40`) input / output reports. On the PULSE 96 that is usage page `0xFF68`, usage `0x61`. Other families use other
pages (Aether lists `0xFF68`, `0xFF80`, `0xFF60`, `0xFF00`, `0xFF01`, `0xFF1B` for control and `0xFF67` for a bulk interface)
and the report size can be 32 instead of 64 bytes, which changes the packet size (`pkt` / `hdr` in `dev.go`).

Known product ids (from Aether's device table, unverified here): PULSE 96 `0x8006`, ECHO 75 / ECHO 96 / ATMOS 87 `0x8009`,
PULSE 75 `0x8801`, wireless dongle `0xFEFE`. Boards sharing a product id are told apart by the reply to command `0x10`
(data bytes 12-13 manufacturer, 14-15 product, counted after the 8-byte header: `12:21` PULSE 96, `12:23` PULSE 75, `12:19` ECHO 96, `12:20` ECHO 75, `12:30` ATMOS 87).

## 2. Capture what Aether does

`tools/aether-browser.js` launches a managed Chromium on the Aether site and logs every WebHID report to `tools/hid.jsonl`:

```sh
PLAYWRIGHT_MODULE=/path/to/node_modules/playwright node tools/aether-browser.js
# click "Connect device", pick your keyboard, then use the console (change an effect, remap a key, ...)
```

Read the log: outgoing frames start `aa <cmd> <len> <addr lo> <addr hi> 00 <last> 00`, replies start `55 <cmd> ...`.
Compare with the table in the README. Things to confirm for the new board:

| Check | How |
|---|---|
| Info reply layout | `aa 10 30 ...`; in the data after the 8-byte header, bytes 8-9 are the firmware, 12-15 the model ids (see `Open` in `dev.go`) |
| Keymap and Fn layer size | `aa 12` / `aa 16` reads (`512` bytes = 128 slots x 4 on the PULSE 96) |
| LED effect struct | `aa 13` read and `aa 23` write, and the **custom (per-key) mode id** for this firmware (`CustomMode()` in `dev.go`; it was 253 on PULSE 96 firmware 1.05 and 128 on 1.07) |
| Per-key table | `aa 14` read, `aa 24` write, entry layout `[slot r g b]` |
| Live frames | `aa 32` packets, see whether the board accepts partial frames and how long they last (`Stream` in `dev.go`) |

Aether's JS bundle is the best reference for everything else: `https://aether.voicsky.com/_nuxt/` (the big `LoadingScreen-*.js`
chunk holds the device drivers, command table, mode tables and layouts). Prettify it first: `npx prettier --parser babel file.js`.

## 3. Generate the layout

```sh
python3 tools/genlayout.py bundle.pretty.js echo75 > layout_echo75.go
```

It emits slot, x (px), row, width, height, label and HID usage per key from Aether's own layout and key list. Shorten labels
that do not fit (a key is about 5 terminal columns) and check the geometry in the TUI. Verified to reproduce the PULSE 96 table
(geometry and key codes) exactly.

## 4. Wire it in

Today the code is single-model, so a first PR can be small. The places that hold PULSE 96 specifics:

| What | Where |
|---|---|
| USB ids, vendor usage page | `findHidraw` in `dev.go` and `udev/*.rules` |
| Packet size / header | `pkt`, `hdr` in `dev.go` |
| Custom LED mode id per firmware | `CustomMode` in `dev.go` |
| Key table | `layout.go` (`Keys`) |
| Slots used for lighting | everything goes through `Keys`, so a new table is picked up by the TUI, `color` and `music` |

A clean refactor would be a `Model` struct (`Name`, `ProductID`, `UsagePage`, `Keys`, `CustomMode(fw)`) and a table of them,
chosen by the product id and the `0x10` reply. If you do that, keep the PULSE 96 behaviour byte-for-byte the same.

## 5. Test and send it

- `gofmt -l .`, `go vet ./...`, `make test` must be clean.
- Run read-only commands first (`aetherctl info`, `led`, `keymap`, `backup`), then `color`, then `music`, then `setkey` on a
  harmless key and restore it.
- In the PR say: **model, firmware version, what you tested, and what you did not**. Attach the relevant lines of
  `tools/hid.jsonl` (they contain only protocol bytes) if you found something that differs from the PULSE 96.

## Out of scope for now

Screens and animations (ECHO line), magnetic-switch settings (rapid trigger, DKS, calibration), macros and SOCD, firmware
update. They exist in the protocol (see Aether's command table) and would make good follow-up work, one feature per PR.
