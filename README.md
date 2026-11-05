# aether-linux

A native Linux **terminal UI and CLI for the VOICSKY PULSE 96** keyboard: lighting, per-key colours, key remapping and a
**music-reactive mode** that VOICSKY's browser console (Aether) does not have. One static Go binary, no browser needed.

> Unofficial. Not affiliated with VOICSKY. The protocol was worked out by reading what the Aether web console sends over
> WebHID, for interoperability. Use at your own risk (see [Safety](#safety)).

```
▌AETHER linux · VOICSKY PULSE 96   fw 1.07
[ LIGHTING ]   REMAP     MUSIC     DEVICE
 Esc  F1  F2 ... live on-screen keyboard that mirrors the colours the board is really showing ...
┤ colour ├ palette widgets   brush ██ #ff5a5a   (g apply gradient · p palette · c brush · space/click paint key)
┤ effect ├ 1/19 Static Bright   bright ■■■■■■   speed ■■□□□□   (e/E effect · +/- bright · [ ] speed · a apply)
```

## Features

| | |
|---|---|
| **Lighting** | the 19 built-in effects with brightness and speed, solid colour, per-key custom colours (click a key or move with the arrows and press space) |
| **Palettes** | `widgets` (your wallpaper palette), `theme` (Omarchy theme), `rainbow`, or your own `RRGGBB,RRGGBB,...` |
| **Music mode** | keys react to whatever your default audio output is playing (`bars` or `pulse`), palette-aware, runs as a **background systemd user service** so it keeps going after you close the TUI |
| **Remap** | Main and Fn layer, by key name or HID usage, restore to the factory layout |
| **Live view** | the TUI reads the real LED colours back from the keyboard, so effects show up on screen too |
| **Backup** | dump settings, both keymaps and the LED state to JSON |

Not included (use the Aether web console for these): macros, SOCD / advanced keys, factory reset, firmware update.

## Requirements

- Linux with `hidraw` (any current kernel), the keyboard on **USB-C or the 2.4 GHz dongle** (Bluetooth mode does not expose the config interface, same as Aether).
- Go **1.24+** to build.
- For music mode: `parec` and `pactl` (package `libpulse`, or `pipewire-pulse`), and systemd for the background service.
- A truecolor terminal for the TUI (kitty, alacritty, ghostty, foot, wezterm...), at least 120 columns wide.

## Install

### 1. Build and install the binary

The binary is called **`aetherctl`** because Arch's `aether` package (the theme generator) already owns `/usr/bin/aether`.

```sh
git clone <this repo> aether-linux && cd aether-linux
make install                     # builds and copies `aetherctl` to ~/.local/bin (override with PREFIX=/usr/local)
```

or without make: `go install .` (puts `aetherctl` in `$(go env GOBIN)` / `~/go/bin`). Make sure that directory is on your `PATH`.

### 2. Let your user talk to the keyboard (one time, needs sudo)

```sh
make udev
```

That installs `udev/99-voicsky.rules` (a `uaccess` rule) and reloads udev. **Unplug and replug the keyboard.**
Check: `ls -l /dev/hidraw*` should show an ACL (`+`) on the PULSE 96 node, and `aetherctl info` should print the firmware.

If you still get `permission denied` (no active logind seat, some tiling setups), use the group rule instead:

```sh
sudo install -Dm644 udev/99-voicsky-group.rules /etc/udev/rules.d/99-voicsky.rules
sudo usermod -aG input "$USER"   # then log out and back in
sudo udevadm control --reload && sudo udevadm trigger --subsystem-match=hidraw --action=add
```

(Any group works; edit `GROUP=` in the rule. On Omarchy/Arch `wheel` or `input` are typical.)

### 3. Run it

```sh
aetherctl            # the TUI
aetherctl help       # all commands
```

## Usage

### TUI keys

`tab` / `shift+tab` switch tabs · arrows or `hjkl` or the mouse select a key · `q` quits.

| Tab | Keys |
|---|---|
| **LIGHTING** | `g` apply gradient of the palette · `p` next palette · `e`/`E` effect · `+`/`-` brightness · `[`/`]` speed · `a` apply the effect · `b` solid brush colour · `c` type a hex brush colour · `space`/`enter`/click paint the selected key |
| **REMAP** | `f` Main/Fn layer · `r` remap (type a key name like `Caps`, or a HID usage like `0x39`) · `x` restore the factory key |
| **MUSIC** | `p` palette · `y` style · `enter` start/stop the background service |
| **DEVICE** | `b` backup to `./aether-backup-*.json` |

### CLI

```sh
aetherctl info                                  # firmware, battery, profile
aetherctl led                                   # current effect, colour, brightness, speed
aetherctl color widgets|theme|rainbow|ff0000,0000ff   # per-key gradient
aetherctl setled -mode 8 -bright 4 -speed 2     # built-in effect 1..19
aetherctl setkey 94 0x62                        # remap key slot 94 (numpad .) to usage 0x62 (add -fn for the Fn layer)
aetherctl keymap                                # dump the keymap
aetherctl backup > backup.json
aetherctl music -palette rainbow -style pulse -bright 4   # foreground, Ctrl-C stops and leaves a plain gradient
aetherctl bg start -palette widgets -style bars [-bright 0-6] # background service (systemd user unit `aether-music`)
aetherctl bg status | aetherctl bg stop
```

Autostart music at login: `systemctl --user enable aether-music` after the first `aetherctl bg start`.

### Palettes

- `widgets` reads `~/.cache/desktop-widgets/colors.json` (`{"bg","fg","accent"}`, the palette an Omarchy desktop-widget setup derives from the wallpaper) and builds the same dim → accent → light ramp a cava gradient uses. If the file is missing it falls back to `theme`.
- `theme` uses `accent` and `color1..6` from `~/.local/state/omarchy/current/theme/colors.toml`.
- Muted desktop colours look pink/white on RGB LEDs (the green and blue LEDs are much brighter than the red), so `theme` and `widgets` get a saturation boost, gamma 2.0 and a channel white-balance (`ledGain` in `pal.go`, green x0.65, blue x0.3), which turns a muted brown into amber-brown. Colours you type as hex, and `rainbow`, are used as given. Tune `ledGain` if your keyboard's LEDs differ.

## Safety

- **Only one program may talk to the keyboard at a time.** `aetherctl` takes an exclusive lock and refuses to start if another instance (or the music service) holds it. Do **not** use the Aether web console at the same time: two writers on the one HID stream corrupt frames, and during development that made the board reset itself and ignore writes until its firmware was updated.
- **Music mode writes to the keyboard up to `-fps` (default 20) times a second.** Only the packets that changed are sent (~8 ms each, a full table is ~120 ms), and audio is read on its own thread so the lights never lag behind the music. It is still not known whether the firmware commits each write to flash, so try short sessions first.
- Key remaps and LED changes are written to the keyboard's own memory like Aether's. Take a `aetherctl backup` first.
- No firmware update or factory reset is implemented, on purpose.

## Firmware notes

The per-key "Custom" LED mode id depends on the firmware: **253 on 1.05, 128 on 1.07+**. `aetherctl` picks it from the firmware version.
Developed and tested on a PULSE 96 on firmware **1.07** (and 1.05 for the lighting basics). The PULSE 75 and other VOICSKY boards share much of
the protocol but are untested and not in the udev rule.

## How it works

The board exposes a vendor HID interface (usage page `0xFF68`, usage `0x61`, 64-byte reports, no report id), usually `/dev/hidraw2`.
Requests start with `0xAA`, replies with `0x55`:

```
[0xAA][cmd][len][addr lo][addr hi][0][last][0][... 56 data bytes ...]     # 64 bytes; bulk data is split in 56-byte packets
```

| cmd (get/set) | what | size |
|---|---|---|
| `0x10` | device info (firmware, battery, profile) | 48 |
| `0x11` / `0x21` | settings (sleep time, report rate...) | 56 |
| `0x12` / `0x22` | main keymap, 128 slots x `[type, 0, usage, 0]` (`type 2` = keyboard key) | 512 |
| `0x16` / `0x26` | Fn layer keymap | 512 |
| `0x13` / `0x23` | LED effect `[mode, r, g, b, 0xFF, ..., brightness@9, speed@10, ..., 0xAA, 0x55]` | 16 |
| `0x14` / `0x24` | per-key colours, 128 x `[slot, r, g, b]`; a single 56-byte packet at an offset may be written on its own | 512 |
| `0x33` | live LED colours (what is displayed now, after brightness) | 512 |

`tools/` has the Playwright scripts used to launch a managed Chromium on the Aether site and log every WebHID report to `tools/hid.jsonl`, which
is how the frames were verified (`PLAYWRIGHT_MODULE=/path/to/playwright node tools/aether-browser.js`).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `Pulse 96 config interface not found` | use USB-C or the dongle, not Bluetooth; check `lsusb` shows `0c45:8006` |
| `permission denied` on `/dev/hidraw*` | install the udev rule and **replug**; or use the group rule above |
| `another aetherctl process is using the keyboard` | `aetherctl bg stop`, close other TUI / Aether tabs |
| writes are ignored, everything reads fine | power-cycle by unplugging; update the firmware with Aether (that fixed it once) |
| colours look white | pick a more saturated palette or hex colours; check brightness |
| TUI looks garbled | needs a truecolor terminal at least 120 columns wide |

## Development

```sh
make test          # palette and layout tests
go vet ./...
```

`layout.go` is generated from Aether's PULSE 96 layout (slot, pixel position, label, HID usage).
