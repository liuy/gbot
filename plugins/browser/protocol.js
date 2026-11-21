// Browser plugin bridge: transport channel <-> CDP browser endpoint.
// The card (ui.js) speaks CDP end-to-end with chromium; this file is a dumb
// text-frame pump. Discovery runs on demand: the first client frame after the
// pipe is down fetches /json/version (the browser id changes whenever
// playwright-mcp relaunches chromium) and dials the WS. No idle retry loop —
// a frame is the only reason to reconnect.
//
// When chromium is not running (playwright-mcp launches it lazily on the
// first tool call), discovery fails: every pending request gets a CDP-shaped
// error reply ("browser idle") and one __status:'idle' control frame tells
// the card to show its waiting overlay. ui.js drives the re-probe, so this
// file stays stateless with no timers. On a successful dial, __status:
// 'attached' precedes the pending flush so the card can (re)attach.
var ch = host.channel('browser')
var conn = null
var pending = []
var connecting = false
function connect() {
  connecting = true
  host
    .fetch('http://127.0.0.1:9333/json/version')
    .then(function (r) {
      if (r.status !== 200) throw new Error('json/version status ' + r.status)
      return host.dial(JSON.parse(r.body).webSocketDebuggerUrl)
    })
    .then(function (c) {
      connecting = false
      conn = c
      c.onMessage(function (frame) {
        ch.send(frame)
      })
      c.onClose(function () {
        conn = null
      })
      ch.send(JSON.stringify({ __status: 'attached' }))
      var q = pending
      pending = []
      for (var i = 0; i < q.length; i++) conn.send(q[i])
      host.log('CDP attached')
    })
    .catch(function (e) {
      connecting = false
      host.log('CDP connect failed: ' + e)
      var q = pending
      pending = []
      for (var i = 0; i < q.length; i++) {
        var req
        try {
          req = JSON.parse(q[i])
        } catch (bad) {
          continue
        }
        if (req && req.id !== undefined) {
          ch.send(JSON.stringify({ id: req.id, error: { code: -32000, message: 'browser idle' } }))
        }
      }
      ch.send(JSON.stringify({ __status: 'idle' }))
    })
}
ch.onMessage(function (m) {
  if (conn) {
    conn.send(m)
    return
  }
  pending.push(m)
  if (!connecting) connect()
})
