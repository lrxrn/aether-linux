package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Wire protocol, taken from aether.voicsky.com and checked against its live traffic:
// 64-byte reports, request 0xAA / reply 0x55, 8-byte header [hdr cmd len addrLo addrHi _ last _], 56 data bytes per packet.
const (
	pkt = 64
	hdr = 8

	cInfo, cGetSettings, cGetKey, cGetLed, cGetCustom, cGetFn = 0x10, 0x11, 0x12, 0x13, 0x14, 0x16
	cSetSettings, cSetKey, cSetLed, cSetCustom, cSetFn        = 0x21, 0x22, 0x23, 0x24, 0x26
	cLive                                                     = 0x33
)

type Dev struct {
	f    *os.File
	Info Info
	last []byte // last per-key table we wrote on this handle, so Paint can send only what changed
}

type Info struct {
	Version  float64
	Battery  int
	Charging bool
	Profile  int
}

var ErrBusy = errors.New("another aetherctl process is using the keyboard (stop the music service / TUI first)")

func findHidraw() (string, error) {
	dirs, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
	for _, d := range dirs {
		ue, _ := os.ReadFile(d + "/device/uevent")
		desc, _ := os.ReadFile(d + "/device/report_descriptor")
		if strings.Contains(string(ue), "HID_ID=0003:00000C45:00008006") && len(desc) > 2 && desc[0] == 0x06 && desc[1] == 0x68 && desc[2] == 0xFF {
			return "/dev/" + filepath.Base(d), nil
		}
	}
	return "", errors.New("Pulse 96 config interface not found (USB-C or 2.4G only; Bluetooth is unsupported)")
}

// Open takes an exclusive lock: hidraw is one shared stream and two writers corrupt it (that reset the board once).
func Open() (*Dev, error) {
	p, err := findHidraw()
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return nil, ErrBusy
	}
	d := &Dev{f: os.NewFile(uintptr(fd), p)}
	b, err := d.Read(cInfo, 48)
	if err != nil {
		d.Close()
		return nil, err
	}
	d.Info = Info{Version: float64(int(b[8]&15)+int(b[8]>>4)*10+int(b[9])*100) / 100, Battery: int(b[17]), Charging: b[18] != 0, Profile: int(b[19])}
	return d, nil
}

func (d *Dev) Close() { d.f.Close() }

func (d *Dev) drain() {
	buf := make([]byte, pkt)
	for {
		d.f.SetReadDeadline(time.Now().Add(time.Millisecond))
		if _, err := d.f.Read(buf); err != nil {
			return
		}
	}
}

func (d *Dev) xfer(cmd byte, size int, data []byte) ([]byte, error) {
	var out []byte
	const chunk = pkt - hdr
	for off := 0; off < size; off += chunk {
		n := min(chunk, size-off)
		f := make([]byte, pkt)
		f[0], f[1], f[2], f[3], f[4] = 0xAA, cmd, byte(n), byte(off), byte(off>>8)
		if off+n >= size {
			f[6] = 1
		}
		if data != nil {
			copy(f[hdr:hdr+n], data[off:])
		}
		d.drain()
		if _, err := d.f.Write(f); err != nil {
			return nil, err
		}
		d.f.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			r := make([]byte, pkt)
			if _, err := d.f.Read(r); err != nil {
				return nil, fmt.Errorf("no reply to cmd 0x%02x: %w", cmd, err)
			}
			if r[0] == 0x55 && r[1] == cmd {
				out = append(out, r[hdr:hdr+n]...)
				break
			}
		}
	}
	return out, nil
}

func (d *Dev) Read(cmd byte, size int) ([]byte, error) { return d.xfer(cmd, size, nil) }
func (d *Dev) Write(cmd byte, data []byte) error       { _, err := d.xfer(cmd, len(data), data); return err }

// CustomMode is the "per-key custom" LED mode id. It is firmware dependent: 253 on 1.05, 128 on 1.07.
func (d *Dev) CustomMode() byte {
	if d.Info.Version >= 1.07 {
		return 128
	}
	return 253
}

type RGB [3]byte

// SetMode changes the LED mode, keeping every other field; rgb/bright are optional (nil / <0).
func (d *Dev) SetMode(mode byte, rgb *RGB, bright, speed int) error {
	e, err := d.Read(cGetLed, 16)
	if err != nil {
		return err
	}
	e[0] = mode
	if rgb != nil {
		copy(e[1:4], rgb[:])
	}
	if bright >= 0 {
		e[9] = byte(bright)
	}
	if speed >= 0 {
		e[10] = byte(speed)
	}
	e[4], e[14], e[15] = 255, 0xAA, 0x55 // constants Aether sends
	return d.Write(cSetLed, e)
}

// Paint writes the per-key layer (512 bytes = 128 x [id r g b]); keys not in the layout stay black.
func (d *Dev) Paint(colorOf func(Key) RGB) error {
	buf := make([]byte, 512)
	for n := 0; n < 128; n++ {
		buf[n*4] = byte(n)
	}
	for _, k := range Keys {
		c := colorOf(k)
		copy(buf[k.Slot*4+1:k.Slot*4+4], c[:])
	}
	if d.last == nil { // first write on this handle: send everything
		d.last = buf
		return d.Write(cSetCustom, buf)
	}
	// A full table is 10 acked packets (~120 ms); a packet is ~8 ms and the board accepts them one at a time.
	for off := 0; off < 512; off += pkt - hdr {
		n := min(pkt-hdr, 512-off)
		if !bytes.Equal(buf[off:off+n], d.last[off:off+n]) {
			if err := d.xferAt(cSetCustom, off, buf[off:off+n]); err != nil {
				d.last = nil
				return err
			}
		}
	}
	d.last = buf
	return nil
}

// xferAt writes one packet at a byte offset of a table.
func (d *Dev) xferAt(cmd byte, off int, data []byte) error {
	f := make([]byte, pkt)
	f[0], f[1], f[2], f[3], f[4], f[6] = 0xAA, cmd, byte(len(data)), byte(off), byte(off>>8), 1
	copy(f[hdr:], data)
	d.drain()
	if _, err := d.f.Write(f); err != nil {
		return err
	}
	d.f.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		r := make([]byte, pkt)
		if _, err := d.f.Read(r); err != nil {
			return fmt.Errorf("no reply to cmd 0x%02x: %w", cmd, err)
		}
		if r[0] == 0x55 && r[1] == cmd {
			return nil
		}
	}
}

// Stream shows a live frame: entries are [slot r g b]..., in a command the Windows music helper used (0x32). The board
// shows it for ~2 s and then falls back to the stored per-key table, never writes flash, and a frame may hold just the
// keys that changed. It bypasses the brightness setting. Packets are sent without waiting for their acks (a full frame is ~20 ms).
func (d *Dev) Stream(entries []byte) error {
	payload := append(append([]byte{}, entries...), 0xAA, 0x55) // trailer
	for off := 0; off < len(payload); off += pkt - hdr {
		n := min(pkt-hdr, len(payload)-off)
		f := make([]byte, pkt)
		f[0], f[1], f[2], f[3], f[4] = 0xAA, 0x32, byte(n), byte(off), byte(off>>8)
		copy(f[hdr:], payload[off:off+n])
		if _, err := d.f.Write(f); err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	d.drain() // drop the acks
	return nil
}

// Live returns what the keys are showing right now (after brightness), also for built-in effects.
func (d *Dev) Live() (map[int]RGB, error) {
	q := make([]byte, 512)
	for n := 0; n < 128; n++ {
		q[n*4] = byte(n)
	}
	l, err := d.xfer(cLive, 512, q)
	if err != nil {
		return nil, err
	}
	m := map[int]RGB{}
	for n := 0; n < 128; n++ {
		m[n] = RGB{l[n*4+1], l[n*4+2], l[n*4+3]}
	}
	return m, nil
}
