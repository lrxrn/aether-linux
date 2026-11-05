// Managed Chromium for aether.voicsky.com: logs every WebHID report to hid.jsonl, CDP on :9333 for inspection.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs = require('fs');
const LOG = __dirname + '/hid.jsonl';
(async () => {
  const ctx = await chromium.launchPersistentContext(process.env.HOME + '/.cache/aether-managed-profile', {
    executablePath: '/usr/bin/chromium', headless: false, viewport: null,
    args: ['--remote-debugging-port=9333', '--ozone-platform=wayland'], ignoreDefaultArgs: ['--enable-automation'],
  });
  await ctx.exposeFunction('__hid', (o) => fs.appendFileSync(LOG, JSON.stringify(o) + '\n'));
  await ctx.addInitScript(() => {
    const hex = (d) => { const u = d instanceof DataView ? new Uint8Array(d.buffer, d.byteOffset, d.byteLength) : new Uint8Array(d.buffer ?? d); return Array.from(u, b => b.toString(16).padStart(2, '0')).join(''); };
    const log = (o) => window.__hid({ t: Date.now(), ...o });
    const P = HIDDevice.prototype;
    for (const m of ['sendReport', 'sendFeatureReport']) {
      const f = P[m]; P[m] = function (id, data) { log({ dir: 'out', m, id, hex: hex(data) }); return f.apply(this, arguments); };
    }
    const rf = P.receiveFeatureReport;
    P.receiveFeatureReport = async function (id) { const r = await rf.apply(this, arguments); log({ dir: 'in', m: 'feature', id, hex: hex(r) }); return r; };
    const seen = new WeakSet(), hook = (d) => { if (seen.has(d)) return; seen.add(d); d.addEventListener('inputreport', (e) => log({ dir: 'in', m: 'input', id: e.reportId, hex: hex(e.data) })); };
    const gd = navigator.hid.getDevices.bind(navigator.hid), rd = navigator.hid.requestDevice.bind(navigator.hid);
    navigator.hid.getDevices = async () => { const r = await gd(); r.forEach(hook); return r; };
    navigator.hid.requestDevice = async (o) => { const r = await rd(o); r.forEach(hook); log({ dir: 'meta', m: 'requestDevice', devices: r.map(d => d.productName) }); return r; };
  });
  const page = ctx.pages()[0] ?? await ctx.newPage();
  await page.goto('https://aether.voicsky.com');
  console.log('ready'); 
  await new Promise(() => {});
})();
